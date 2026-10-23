// Package transfers moves money between users. The server is the only
// arbiter of the balance rule: a transfer that would overdraw fails at
// COMMIT on the deferred constraint and is recorded as rejected.
package transfers

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/spiderocious/devfest-akure-2026/wallet/backend/internal/db"
	"github.com/spiderocious/devfest-akure-2026/wallet/backend/internal/features/events"
	"github.com/spiderocious/devfest-akure-2026/wallet/backend/internal/features/ledger"
	"github.com/spiderocious/devfest-akure-2026/wallet/backend/internal/httpx"
	"github.com/spiderocious/devfest-akure-2026/wallet/backend/internal/ids"
	"github.com/spiderocious/devfest-akure-2026/wallet/backend/internal/middleware"
	"github.com/spiderocious/devfest-akure-2026/wallet/backend/internal/money"
	"github.com/spiderocious/devfest-akure-2026/wallet/backend/internal/pagination"
	"github.com/spiderocious/devfest-akure-2026/wallet/backend/internal/validation"
)

// Transfer is the wire shape.
type Transfer struct {
	ID              string     `json:"id"`
	FromUserID      string     `json:"from_user_id"`
	FromName        string     `json:"from_name"`
	ToUserID        string     `json:"to_user_id"`
	ToName          string     `json:"to_name"`
	AmountKobo      money.Kobo `json:"amount_kobo"`
	Status          string     `json:"status"`
	IdempotencyKey  *string    `json:"idempotency_key"`
	ClientCreatedAt *time.Time `json:"client_created_at"`
	CreatedAt       time.Time  `json:"created_at"`
}

// CreateRequest is the body for POST /transfers.
type CreateRequest struct {
	ToUsername      string     `json:"to_username" binding:"required,username"`
	AmountKobo      money.Kobo `json:"amount_kobo" binding:"required,gt=0"`
	ClientCreatedAt *time.Time `json:"client_created_at"`
}

// CreateResponse carries the posted transfer and the sender's new balance.
type CreateResponse struct {
	Transfer    Transfer   `json:"transfer"`
	BalanceKobo money.Kobo `json:"balance_kobo"`
}

// WalletResponse is GET /wallet.
type WalletResponse struct {
	ID          string     `json:"id"`
	BalanceKobo money.Kobo `json:"balance_kobo"`
}

// ReceivedEvent is the SSE payload for the recipient.
type ReceivedEvent struct {
	Transfer    Transfer   `json:"transfer"`
	BalanceKobo money.Kobo `json:"balance_kobo"`
}

type repo struct{ pool *pgxpool.Pool }

type party struct{ userID, walletID, name string }

func (r repo) partyByUser(ctx context.Context, userID string) (party, error) {
	var p party
	err := r.pool.QueryRow(ctx, `SELECT u.id, w.id, u.name FROM users u JOIN wallets w ON w.user_id = u.id WHERE u.id = $1`, userID).
		Scan(&p.userID, &p.walletID, &p.name)
	return p, err
}

func (r repo) partyByUsername(ctx context.Context, username string) (party, error) {
	var p party
	err := r.pool.QueryRow(ctx, `SELECT u.id, w.id, u.name FROM users u JOIN wallets w ON w.user_id = u.id WHERE u.username = $1`, username).
		Scan(&p.userID, &p.walletID, &p.name)
	return p, err
}

const transferColumns = `
	t.id, fu.id, fu.name, tu.id, tu.name, t.amount_kobo, t.status, t.idempotency_key, t.client_created_at, t.created_at`

func scanTransfer(row pgx.Row) (Transfer, error) {
	var t Transfer
	var amt int64
	err := row.Scan(&t.ID, &t.FromUserID, &t.FromName, &t.ToUserID, &t.ToName, &amt, &t.Status, &t.IdempotencyKey, &t.ClientCreatedAt, &t.CreatedAt)
	t.AmountKobo = money.Kobo(amt)
	return t, err
}

func (r repo) insert(ctx context.Context, tx pgx.Tx, id string, from, to party, amount money.Kobo, status, reason string, key *string, clientAt *time.Time) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO transfers (id, from_wallet_id, to_wallet_id, amount_kobo, status, reject_reason, idempotency_key, client_created_at)
		VALUES ($1, $2, $3, $4, $5, NULLIF($6, ''), $7, $8)`,
		id, from.walletID, to.walletID, int64(amount), status, reason, key, clientAt)
	return err
}

func (r repo) get(ctx context.Context, id string) (Transfer, error) {
	return scanTransfer(r.pool.QueryRow(ctx, `
		SELECT `+transferColumns+` FROM transfers t
		JOIN wallets fw ON fw.id = t.from_wallet_id JOIN users fu ON fu.id = fw.user_id
		JOIN wallets tw ON tw.id = t.to_wallet_id JOIN users tu ON tu.id = tw.user_id
		WHERE t.id = $1`, id))
}

// list pages a user's posted transfers, sent and received, newest first.
func (r repo) list(ctx context.Context, walletID string, cur pagination.Cursor, hasCur bool, limit int) ([]Transfer, bool, error) {
	base := `
		SELECT ` + transferColumns + ` FROM transfers t
		JOIN wallets fw ON fw.id = t.from_wallet_id JOIN users fu ON fu.id = fw.user_id
		JOIN wallets tw ON tw.id = t.to_wallet_id JOIN users tu ON tu.id = tw.user_id
		WHERE t.status = 'posted' AND (t.from_wallet_id = $1 OR t.to_wallet_id = $1)`
	var rows pgx.Rows
	var err error
	if hasCur {
		rows, err = r.pool.Query(ctx, base+` AND (t.created_at, t.id) < ($2::timestamptz, $3) ORDER BY t.created_at DESC, t.id DESC LIMIT $4`,
			walletID, cur.LastSortKey, cur.LastID, limit+1)
	} else {
		rows, err = r.pool.Query(ctx, base+` ORDER BY t.created_at DESC, t.id DESC LIMIT $2`, walletID, limit+1)
	}
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	out := make([]Transfer, 0, limit)
	for rows.Next() {
		t, err := scanTransfer(rows)
		if err != nil {
			return nil, false, err
		}
		out = append(out, t)
	}
	if rows.Err() != nil {
		return nil, false, rows.Err()
	}
	more := len(out) > limit
	if more {
		out = out[:limit]
	}
	return out, more, nil
}

// Service owns transfer logic.
type Service struct {
	pool   *pgxpool.Pool
	repo   repo
	ledger ledger.Repo
	hub    *events.Hub
}

// NewService wires the service.
func NewService(pool *pgxpool.Pool, hub *events.Hub) *Service {
	return &Service{pool: pool, repo: repo{pool}, ledger: ledger.Repo{Pool: pool}, hub: hub}
}

const createEndpoint = "POST /transfers"

// Create posts a transfer under the caller's idempotency key. On an overdraw
// the posting transaction rolls back; the rejected transfer is then recorded
// in a fresh transaction so the attempt is visible to ops, and the domain
// error carries the real balance back to the device.
func (s *Service) Create(ctx context.Context, in CreateRequest) (middleware.Stored, bool, error) {
	from, err := s.repo.partyByUser(ctx, middleware.UserIDFrom(ctx))
	if err != nil {
		return middleware.Stored{}, false, err
	}
	to, err := s.repo.partyByUsername(ctx, validation.Username(in.ToUsername))
	if errors.Is(err, pgx.ErrNoRows) {
		return middleware.Stored{}, false, httpx.Domain(httpx.ReasonRecipientNotFound, httpx.MsgRecipientNotFound, http.StatusNotFound)
	}
	if err != nil {
		return middleware.Stored{}, false, err
	}
	if to.userID == from.userID {
		return middleware.Stored{}, false, httpx.Domain(httpx.ReasonSelfTransfer, httpx.MsgSelfTransfer, http.StatusUnprocessableEntity)
	}

	id := ids.New(ids.Transfer)
	var key *string
	if k, ok := middleware.IdempotencyFrom(ctx); ok {
		key = &k.Key
	}

	stored, replayed, err := middleware.Execute(ctx, s.pool, createEndpoint, func(tx pgx.Tx) (int, any, error) {
		if err := s.repo.insert(ctx, tx, id, from, to, in.AmountKobo, "posted", "", key, in.ClientCreatedAt); err != nil {
			return 0, nil, err
		}
		if err := ledger.Post(ctx, tx, from.walletID, to.walletID, in.AmountKobo, ledger.KindTransfer, id); err != nil {
			return 0, nil, err
		}
		// The envelope needs the post-commit balance; compute it from what
		// we know rather than reading the cache before the constraint ran.
		bal, err := s.ledger.Balance(ctx, from.walletID)
		if err != nil {
			return 0, nil, err
		}
		t := Transfer{ID: id, FromUserID: from.userID, FromName: from.name, ToUserID: to.userID, ToName: to.name,
			AmountKobo: in.AmountKobo, Status: "posted", IdempotencyKey: key, ClientCreatedAt: in.ClientCreatedAt, CreatedAt: time.Now().UTC()}
		return http.StatusCreated, CreateResponse{Transfer: t, BalanceKobo: bal - in.AmountKobo}, nil
	})
	if err == nil {
		if !replayed {
			s.notify(ctx, id, to)
		}
		return stored, replayed, nil
	}
	if errors.Is(ledger.MapCommit(err), ledger.ErrOverdraw) {
		bal, berr := s.ledger.Balance(ctx, from.walletID)
		if berr != nil {
			return middleware.Stored{}, false, berr
		}
		_ = db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
			return s.repo.insert(ctx, tx, id, from, to, in.AmountKobo, "rejected", "insufficient_balance", key, in.ClientCreatedAt)
		})
		return middleware.Stored{}, false, httpx.Domain(httpx.ReasonInsufficientBalance, httpx.MsgInsufficientBalance, http.StatusUnprocessableEntity).
			WithData(WalletResponse{ID: from.walletID, BalanceKobo: bal}).
			WithDiagnostic("balance_non_negative violated at commit")
	}
	return middleware.Stored{}, false, err
}

// notify publishes to the recipient after COMMIT, never inside the
// transaction, so a rolled-back transfer is never announced.
func (s *Service) notify(ctx context.Context, id string, to party) {
	t, err := s.repo.get(ctx, id)
	if err != nil {
		return
	}
	bal, err := s.ledger.Balance(ctx, to.walletID)
	if err != nil {
		return
	}
	s.hub.Publish(to.userID, events.Event{Type: "transfer.received", Data: ReceivedEvent{Transfer: t, BalanceKobo: bal}})
}

// Wallet returns the caller's wallet and balance.
func (s *Service) Wallet(ctx context.Context) (WalletResponse, error) {
	p, err := s.repo.partyByUser(ctx, middleware.UserIDFrom(ctx))
	if errors.Is(err, pgx.ErrNoRows) {
		return WalletResponse{}, httpx.NotFound(httpx.MsgWalletNotFound)
	}
	if err != nil {
		return WalletResponse{}, err
	}
	bal, err := s.ledger.Balance(ctx, p.walletID)
	if err != nil {
		return WalletResponse{}, err
	}
	return WalletResponse{ID: p.walletID, BalanceKobo: bal}, nil
}

// Page is a page of transfers.
type Page struct {
	Items []Transfer
	Meta  pagination.Meta
}

// List pages the caller's transfers.
func (s *Service) List(ctx context.Context, cursor string, limit int) (Page, error) {
	p, err := s.repo.partyByUser(ctx, middleware.UserIDFrom(ctx))
	if err != nil {
		return Page{}, err
	}
	cur, has := pagination.Decode(cursor)
	items, more, err := s.repo.list(ctx, p.walletID, cur, has, limit)
	if err != nil {
		return Page{}, err
	}
	out := Page{Items: items, Meta: pagination.Meta{HasMore: more}}
	if more {
		last := items[len(items)-1]
		next := pagination.Encode(pagination.Cursor{LastID: last.ID, LastSortKey: last.CreatedAt.Format(time.RFC3339Nano)})
		out.Meta.NextCursor = &next
	}
	return out, nil
}

type handler struct{ svc *Service }

func (h handler) create(c *gin.Context) {
	var in CreateRequest
	if raw, ok := middleware.RawBody(c); ok {
		if err := bindRaw(raw, &in); err != nil {
			httpx.Fail(c, err)
			return
		}
	} else if !httpx.Bind(c, &in) {
		return
	}
	stored, _, err := h.svc.Create(c.Request.Context(), in)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.Raw(c, stored.Status, stored.Body)
}

func (h handler) wallet(c *gin.Context) {
	w, err := h.svc.Wallet(c.Request.Context())
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, w)
}

func (h handler) list(c *gin.Context) {
	p, err := h.svc.List(c.Request.Context(), c.Query("cursor"), pagination.Limit(c.Query("limit")))
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OKMeta(c, p.Items, p.Meta)
}

// Register mounts the feature on the authenticated group.
func Register(rg *gin.RouterGroup, svc *Service) {
	h := handler{svc}
	rg.GET("/wallet", h.wallet)
	rg.POST("/transfers", h.create)
	rg.GET("/transactions", h.list)
}

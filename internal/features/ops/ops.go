// Package ops exposes read-only views for the stage screen and for proving
// the ledger reconciles. Users, user lookup lives here too.
package ops

import (
	"context"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/spiderocious/devfest-akure-2026/wallet/backend/internal/features/ledger"
	"github.com/spiderocious/devfest-akure-2026/wallet/backend/internal/httpx"
	"github.com/spiderocious/devfest-akure-2026/wallet/backend/internal/middleware"
	"github.com/spiderocious/devfest-akure-2026/wallet/backend/internal/money"
	"github.com/spiderocious/devfest-akure-2026/wallet/backend/internal/validation"
)

// Reconcile is GET /ops/reconcile.
type Reconcile struct {
	Clean  bool           `json:"clean"`
	Drifts []ledger.Drift `json:"drifts"`
}

// StageRow is one received transfer on the stage screen.
type StageRow struct {
	ID         string     `json:"id"`
	FromName   string     `json:"from_name"`
	AmountKobo money.Kobo `json:"amount_kobo"`
	KeyTail    string     `json:"key_tail"`
	CreatedAt  time.Time  `json:"created_at"`
}

// Stage is GET /ops/stage: the presenter's view of their own wallet.
type Stage struct {
	Name          string     `json:"name"`
	BalanceKobo   money.Kobo `json:"balance_kobo"`
	ReceivedToday int        `json:"received_today"`
	DistinctKeys  int        `json:"distinct_keys"`
	Recent        []StageRow `json:"recent"`
}

// Lookup is GET /users?username=.
type Lookup struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Service reads ops views.
type Service struct {
	pool   *pgxpool.Pool
	ledger ledger.Repo
}

// NewService wires the service.
func NewService(pool *pgxpool.Pool) *Service {
	return &Service{pool: pool, ledger: ledger.Repo{Pool: pool}}
}

// Reconcile returns every wallet whose cache disagrees with the ledger.
func (s *Service) Reconcile(ctx context.Context) (Reconcile, error) {
	d, err := s.ledger.Reconcile(ctx)
	if err != nil {
		return Reconcile{}, err
	}
	return Reconcile{Clean: len(d) == 0, Drifts: d}, nil
}

// Stage returns the caller's wallet as the big-screen view.
func (s *Service) Stage(ctx context.Context) (Stage, error) {
	userID := middleware.UserIDFrom(ctx)
	var st Stage
	var walletID string
	var bal int64
	err := s.pool.QueryRow(ctx, `
		SELECT u.name, w.id, COALESCE(wb.balance_kobo, 0)
		FROM users u JOIN wallets w ON w.user_id = u.id
		LEFT JOIN wallet_balances wb ON wb.wallet_id = w.id WHERE u.id = $1`, userID).Scan(&st.Name, &walletID, &bal)
	if err != nil {
		return Stage{}, err
	}
	st.BalanceKobo = money.Kobo(bal)
	if err := s.pool.QueryRow(ctx, `
		SELECT count(*)::int, count(DISTINCT idempotency_key)::int FROM transfers
		WHERE to_wallet_id = $1 AND status = 'posted' AND created_at >= date_trunc('day', now())`, walletID).
		Scan(&st.ReceivedToday, &st.DistinctKeys); err != nil {
		return Stage{}, err
	}
	rows, err := s.pool.Query(ctx, `
		SELECT t.id, fu.name, t.amount_kobo, COALESCE(right(t.idempotency_key, 6), ''), t.created_at
		FROM transfers t JOIN wallets fw ON fw.id = t.from_wallet_id JOIN users fu ON fu.id = fw.user_id
		WHERE t.to_wallet_id = $1 AND t.status = 'posted' ORDER BY t.created_at DESC LIMIT 10`, walletID)
	if err != nil {
		return Stage{}, err
	}
	defer rows.Close()
	st.Recent = []StageRow{}
	for rows.Next() {
		var r StageRow
		var amt int64
		if err := rows.Scan(&r.ID, &r.FromName, &amt, &r.KeyTail, &r.CreatedAt); err != nil {
			return Stage{}, err
		}
		r.AmountKobo = money.Kobo(amt)
		st.Recent = append(st.Recent, r)
	}
	return st, rows.Err()
}

// Lookup finds a recipient by username and returns only id and name.
func (s *Service) Lookup(ctx context.Context, username string) (Lookup, error) {
	var l Lookup
	err := s.pool.QueryRow(ctx, `SELECT id, name FROM users WHERE username = $1`, username).Scan(&l.ID, &l.Name)
	if err != nil {
		return Lookup{}, httpx.Domain(httpx.ReasonRecipientNotFound, httpx.MsgRecipientNotFound, 404)
	}
	return l, nil
}

type handler struct{ svc *Service }

func (h handler) reconcile(c *gin.Context) {
	out, err := h.svc.Reconcile(c.Request.Context())
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, out)
}

func (h handler) stage(c *gin.Context) {
	out, err := h.svc.Stage(c.Request.Context())
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, out)
}

func (h handler) lookup(c *gin.Context) {
	username := c.Query("username")
	if username == "" {
		httpx.Fail(c, httpx.Validation(map[string][]string{"username": {"required"}}))
		return
	}
	out, err := h.svc.Lookup(c.Request.Context(), validation.Username(username))
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, out)
}

// Register mounts the feature on the authenticated group.
func Register(rg *gin.RouterGroup, svc *Service) {
	h := handler{svc}
	rg.GET("/ops/reconcile", h.reconcile)
	rg.GET("/ops/stage", h.stage)
	rg.GET("/users", h.lookup)
}

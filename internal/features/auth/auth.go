// Package auth signs users up and in. Sign-up creates the user, their wallet,
// and posts the welcome credit through the ledger in one transaction.
package auth

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"

	"github.com/spiderocious/devfest-akure-2026/wallet/backend/internal/db"
	"github.com/spiderocious/devfest-akure-2026/wallet/backend/internal/features/ledger"
	"github.com/spiderocious/devfest-akure-2026/wallet/backend/internal/httpx"
	"github.com/spiderocious/devfest-akure-2026/wallet/backend/internal/ids"
	"github.com/spiderocious/devfest-akure-2026/wallet/backend/internal/middleware"
	"github.com/spiderocious/devfest-akure-2026/wallet/backend/internal/money"
)

// SignupBonus is what every new wallet receives: ₦5,000.
const SignupBonus money.Kobo = 500_000

// User is the wire shape. The PIN hash never leaves the repo.
type User struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Phone string `json:"phone"`
}

// Wallet is the wire shape of a wallet with its balance.
type Wallet struct {
	ID          string     `json:"id"`
	BalanceKobo money.Kobo `json:"balance_kobo"`
}

// SignupRequest is the sign-up body.
type SignupRequest struct {
	Phone string `json:"phone" binding:"required,min=7,max=20"`
	Name  string `json:"name" binding:"required,min=2,max=80"`
	PIN   string `json:"pin" binding:"required,len=4,numeric"`
}

// LoginRequest is the sign-in body.
type LoginRequest struct {
	Phone string `json:"phone" binding:"required,min=7,max=20"`
	PIN   string `json:"pin" binding:"required,len=4,numeric"`
}

// SessionResponse is returned by both sign-up and sign-in.
type SessionResponse struct {
	Token  string `json:"token"`
	User   User   `json:"user"`
	Wallet Wallet `json:"wallet"`
}

type repo struct{ pool *pgxpool.Pool }

type userRow struct {
	User
	PINHash  string
	WalletID string
}

func (r repo) findByPhone(ctx context.Context, phone string) (userRow, error) {
	var u userRow
	err := r.pool.QueryRow(ctx, `
		SELECT u.id, u.name, u.phone, u.pin_hash, w.id
		FROM users u JOIN wallets w ON w.user_id = u.id WHERE u.phone = $1`, phone).
		Scan(&u.ID, &u.Name, &u.Phone, &u.PINHash, &u.WalletID)
	return u, err
}

func (r repo) findByID(ctx context.Context, id string) (userRow, error) {
	var u userRow
	err := r.pool.QueryRow(ctx, `
		SELECT u.id, u.name, u.phone, u.pin_hash, w.id
		FROM users u JOIN wallets w ON w.user_id = u.id WHERE u.id = $1`, id).
		Scan(&u.ID, &u.Name, &u.Phone, &u.PINHash, &u.WalletID)
	return u, err
}

func (r repo) treasury(ctx context.Context, tx pgx.Tx) (string, error) {
	var id string
	err := tx.QueryRow(ctx, `SELECT id FROM wallets WHERE kind = 'treasury'`).Scan(&id)
	return id, err
}

// Service holds auth logic.
type Service struct {
	pool   *pgxpool.Pool
	repo   repo
	ledger ledger.Repo
	secret string
	ttl    time.Duration
}

// NewService wires the service.
func NewService(pool *pgxpool.Pool, secret string) *Service {
	return &Service{pool: pool, repo: repo{pool}, ledger: ledger.Repo{Pool: pool}, secret: secret, ttl: 30 * 24 * time.Hour}
}

// Signup creates user, wallet and the welcome credit atomically. The credit
// is a journal post from the treasury with a fixed reference, so running the
// same sign-up twice can never credit twice.
func (s *Service) Signup(ctx context.Context, in SignupRequest) (SessionResponse, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(in.PIN), bcrypt.DefaultCost)
	if err != nil {
		return SessionResponse{}, err
	}
	userID := ids.New(ids.User)
	walletID := ids.New(ids.Wallet)

	err = db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO users (id, phone, name, pin_hash) VALUES ($1, $2, $3, $4)`,
			userID, in.Phone, in.Name, string(hash)); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO wallets (id, user_id, kind) VALUES ($1, $2, 'user')`, walletID, userID); err != nil {
			return err
		}
		treasury, err := s.repo.treasury(ctx, tx)
		if err != nil {
			return err
		}
		return ledger.Post(ctx, tx, treasury, walletID, SignupBonus, ledger.KindSignupBonus, "signup:"+userID)
	})
	if db.IsUniqueViolation(err, "users_phone_key") {
		return SessionResponse{}, httpx.Domain(httpx.ReasonAlreadyExists, httpx.MsgPhoneTaken, http.StatusConflict)
	}
	if err != nil {
		return SessionResponse{}, ledger.MapCommit(err)
	}
	return s.session(ctx, userID)
}

// Login verifies the PIN and issues a token. Unknown phone and wrong PIN are
// the same error so phones cannot be enumerated.
func (s *Service) Login(ctx context.Context, in LoginRequest) (SessionResponse, error) {
	u, err := s.repo.findByPhone(ctx, in.Phone)
	if errors.Is(err, pgx.ErrNoRows) {
		return SessionResponse{}, invalidCredentials()
	}
	if err != nil {
		return SessionResponse{}, err
	}
	if bcrypt.CompareHashAndPassword([]byte(u.PINHash), []byte(in.PIN)) != nil {
		return SessionResponse{}, invalidCredentials()
	}
	return s.session(ctx, u.ID)
}

// Me returns the current user and wallet.
func (s *Service) Me(ctx context.Context) (SessionResponse, error) {
	out, err := s.session(ctx, middleware.UserIDFrom(ctx))
	if errors.Is(err, pgx.ErrNoRows) {
		return SessionResponse{}, httpx.Domain(httpx.ReasonTokenInvalid, httpx.MsgTokenInvalid, http.StatusUnauthorized)
	}
	return out, err
}

func (s *Service) session(ctx context.Context, userID string) (SessionResponse, error) {
	u, err := s.repo.findByID(ctx, userID)
	if err != nil {
		return SessionResponse{}, err
	}
	bal, err := s.ledger.Balance(ctx, u.WalletID)
	if err != nil {
		return SessionResponse{}, err
	}
	tok, err := middleware.SignToken(s.secret, u.ID, s.ttl)
	if err != nil {
		return SessionResponse{}, err
	}
	return SessionResponse{Token: tok, User: u.User, Wallet: Wallet{ID: u.WalletID, BalanceKobo: bal}}, nil
}

func invalidCredentials() *httpx.DomainError {
	return httpx.Domain(httpx.ReasonInvalidCredentials, httpx.MsgInvalidCredentials, http.StatusUnauthorized)
}

type handler struct{ svc *Service }

func (h handler) signup(c *gin.Context) {
	var in SignupRequest
	if !httpx.Bind(c, &in) {
		return
	}
	out, err := h.svc.Signup(c.Request.Context(), in)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.Created(c, out)
}

func (h handler) login(c *gin.Context) {
	var in LoginRequest
	if !httpx.Bind(c, &in) {
		return
	}
	out, err := h.svc.Login(c.Request.Context(), in)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, out)
}

func (h handler) me(c *gin.Context) {
	out, err := h.svc.Me(c.Request.Context())
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, out)
}

// RegisterPublic mounts the routes that need no token.
func RegisterPublic(rg *gin.RouterGroup, svc *Service) {
	h := handler{svc}
	rg.POST("/auth/signup", h.signup)
	rg.POST("/auth/login", h.login)
}

// RegisterProtected mounts the routes that need a token.
func RegisterProtected(rg *gin.RouterGroup, svc *Service) {
	rg.GET("/auth/me", handler{svc}.me)
}

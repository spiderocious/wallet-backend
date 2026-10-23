// Package app assembles the Gin engine: middleware in a deliberate order,
// then each feature's Register on the right group.
package app

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/spiderocious/devfest-akure-2026/wallet/backend/internal/env"
	"github.com/spiderocious/devfest-akure-2026/wallet/backend/internal/features/auth"
	"github.com/spiderocious/devfest-akure-2026/wallet/backend/internal/features/events"
	"github.com/spiderocious/devfest-akure-2026/wallet/backend/internal/features/ops"
	"github.com/spiderocious/devfest-akure-2026/wallet/backend/internal/features/telemetry"
	"github.com/spiderocious/devfest-akure-2026/wallet/backend/internal/features/transfers"
	"github.com/spiderocious/devfest-akure-2026/wallet/backend/internal/middleware"
)

// Deps is everything the engine needs.
type Deps struct {
	Config env.Config
	Pool   *pgxpool.Pool
	Log    *slog.Logger
	Hub    *events.Hub
}

// Engine builds the router.
func Engine(d Deps) *gin.Engine {
	if d.Config.IsProduction() {
		gin.SetMode(gin.ReleaseMode)
	}
	gin.EnableJsonDecoderDisallowUnknownFields()

	e := gin.New()
	e.Use(middleware.Recover())
	e.Use(middleware.RequestID())
	e.Use(middleware.Logger(d.Log))
	e.Use(cors.New(cors.Config{
		AllowOrigins:  strings.Split(d.Config.CORSOrigin, ","),
		AllowMethods:  []string{"GET", "POST", "OPTIONS"},
		AllowHeaders:  []string{"Authorization", "Content-Type", "Idempotency-Key", "X-Request-Id"},
		ExposeHeaders: []string{"X-Request-Id", "Retry-After"},
	}))

	// The online probe: no auth, no body.
	e.GET("/api/v1/ping", func(c *gin.Context) { c.Status(http.StatusNoContent) })
	e.GET("/health", func(c *gin.Context) { c.String(http.StatusOK, "ok") })

	api := e.Group("/api/v1")
	authSvc := auth.NewService(d.Pool, d.Config.JWTSecret)
	auth.RegisterPublic(api, authSvc)

	// Auth before rate limit so the limit is per user. Idempotency captures
	// the header and body; the transfer service writes the key in its own
	// transaction.
	protected := api.Group("")
	protected.Use(middleware.Auth(d.Config.JWTSecret))
	protected.Use(middleware.RateLimit(120, 30))
	protected.Use(middleware.Idempotency())

	auth.RegisterProtected(protected, authSvc)
	transfers.Register(protected, transfers.NewService(d.Pool, d.Hub))
	ops.Register(protected, ops.NewService(d.Pool))
	telemetry.Register(protected, telemetry.NewService(d.Pool))
	// The event stream is long-lived; it is on the same group so it is
	// authenticated, and rate limiting only counts the one connect.
	events.Register(protected, d.Hub)
	return e
}

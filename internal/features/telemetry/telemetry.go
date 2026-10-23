// Package telemetry accepts batched device events. A bad event is logged and
// skipped; the batch never fails the client, because the client only sends
// when it has a rare moment of connectivity.
package telemetry

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/spiderocious/devfest-akure-2026/wallet/backend/internal/httpx"
	"github.com/spiderocious/devfest-akure-2026/wallet/backend/internal/ids"
	"github.com/spiderocious/devfest-akure-2026/wallet/backend/internal/middleware"
)

// MaxBatch caps one request.
const MaxBatch = 200

// Event is one device event.
type Event struct {
	Kind       string          `json:"kind" binding:"required,max=64"`
	Payload    json.RawMessage `json:"payload"`
	OccurredAt time.Time       `json:"occurred_at" binding:"required"`
}

// BatchRequest is the body for POST /telemetry.
type BatchRequest struct {
	Events []Event `json:"events" binding:"required,min=1,max=200,dive"`
}

// BatchResponse reports how many events were stored.
type BatchResponse struct {
	Accepted int `json:"accepted"`
	Skipped  int `json:"skipped"`
}

// Service stores events.
type Service struct{ pool *pgxpool.Pool }

// NewService wires the service.
func NewService(pool *pgxpool.Pool) *Service { return &Service{pool: pool} }

// Ingest writes the batch in one round trip.
func (s *Service) Ingest(ctx context.Context, in BatchRequest) (BatchResponse, error) {
	userID := middleware.UserIDFrom(ctx)
	batch := &pgx.Batch{}
	var out BatchResponse
	for _, e := range in.Events {
		payload := e.Payload
		if len(payload) == 0 {
			payload = json.RawMessage(`{}`)
		}
		if !json.Valid(payload) {
			out.Skipped++
			slog.Warn("telemetry: invalid payload skipped", "kind", e.Kind, "user_id", userID)
			continue
		}
		batch.Queue(`INSERT INTO telemetry_events (id, user_id, kind, payload, occurred_at) VALUES ($1, $2, $3, $4, $5)`,
			ids.New(ids.Telemetry), userID, e.Kind, payload, e.OccurredAt)
		out.Accepted++
	}
	if out.Accepted == 0 {
		return out, nil
	}
	res := s.pool.SendBatch(ctx, batch)
	defer func() { _ = res.Close() }()
	for i := 0; i < out.Accepted; i++ {
		if _, err := res.Exec(); err != nil {
			return BatchResponse{}, err
		}
	}
	return out, nil
}

type handler struct{ svc *Service }

func (h handler) ingest(c *gin.Context) {
	var in BatchRequest
	if !httpx.Bind(c, &in) {
		return
	}
	out, err := h.svc.Ingest(c.Request.Context(), in)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.Accepted(c, out)
}

// Register mounts the feature on the authenticated group.
func Register(rg *gin.RouterGroup, svc *Service) {
	rg.POST("/telemetry", handler{svc}.ingest)
}

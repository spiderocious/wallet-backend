package middleware

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/spiderocious/devfest-akure-2026/wallet/backend/internal/db"
	"github.com/spiderocious/devfest-akure-2026/wallet/backend/internal/httpx"
	"github.com/spiderocious/devfest-akure-2026/wallet/backend/internal/ids"
)

// Idempotency support. The key row is written in the same transaction as
// the resource, so a crash between the two cannot lose the dedupe. A repeat
// with the same key and the same body replays the stored envelope; the same
// key with a different body is rejected as idempotency_mismatch.

// IdempotencyKey carries the header value and the body hash for one request.
type IdempotencyKey struct {
	Key  string
	Hash string
}

type ctxIdem struct{}

// IdempotencyFrom returns the key captured by the middleware, if any.
func IdempotencyFrom(ctx context.Context) (IdempotencyKey, bool) {
	k, ok := ctx.Value(ctxIdem{}).(IdempotencyKey)
	return k, ok && k.Key != ""
}

// WithIdempotency is used by tests to build a context as the middleware would.
func WithIdempotency(ctx context.Context, k IdempotencyKey) context.Context {
	return context.WithValue(ctx, ctxIdem{}, k)
}

// HashBody hashes a request body for mismatch detection.
func HashBody(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Idempotency captures the Idempotency-Key header and the body hash. It does
// not touch the database; the service does, inside its transaction, through
// Execute below.
func Idempotency() gin.HandlerFunc {
	return func(c *gin.Context) {
		key := c.GetHeader("Idempotency-Key")
		if key == "" {
			c.Next()
			return
		}
		body, err := c.GetRawData()
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		// Hand the body back to the handler untouched: JSON handlers bind from
		// RawBody, multipart handlers read the stream again.
		c.Request.Body = io.NopCloser(bytes.NewReader(body))
		c.Set("raw_body", body)
		c.Request = c.Request.WithContext(WithIdempotency(c.Request.Context(), IdempotencyKey{Key: key, Hash: HashBody(body)}))
		c.Next()
	}
}

// RawBody returns the body captured by Idempotency, for handlers that bind
// after the middleware consumed it.
func RawBody(c *gin.Context) ([]byte, bool) {
	v, ok := c.Get("raw_body")
	if !ok {
		return nil, false
	}
	b, ok := v.([]byte)
	return b, ok
}

// Stored is a replayed response.
type Stored struct {
	Status int
	Body   []byte
}

// Execute runs fn inside one transaction and records its result under the
// idempotency key, if the request carried one. On a repeat it returns the
// stored envelope instead of running fn.
//
// fn returns the HTTP status and the envelope data for the success case. The
// envelope is marshalled here so the stored body is byte-identical to what
// the first response sent.
func Execute(ctx context.Context, pool *pgxpool.Pool, endpoint string, fn func(tx pgx.Tx) (int, any, error)) (Stored, bool, error) {
	key, has := IdempotencyFrom(ctx)
	user := UserIDFrom(ctx)

	if has {
		if s, found, err := lookup(ctx, pool, key, user, endpoint); err != nil {
			return Stored{}, false, err
		} else if found {
			return s, true, nil
		}
	}

	var out Stored
	err := db.WithTx(ctx, pool, func(tx pgx.Tx) error {
		status, data, err := fn(tx)
		if err != nil {
			return err
		}
		body, err := json.Marshal(struct {
			Data any `json:"data"`
		}{Data: data})
		if err != nil {
			return err
		}
		out = Stored{Status: status, Body: body}
		if !has {
			return nil
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO idempotency_keys (id, key, user_id, endpoint, request_hash, response_status, response_body, expires_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
			ids.New(ids.Idem), key.Key, user, endpoint, key.Hash, status, body, time.Now().Add(24*time.Hour))
		return err
	})
	if err == nil {
		return out, false, nil
	}
	// Two requests with the same key raced past the lookup: the second one
	// hits the unique index. Treat it as a replay.
	if has && db.IsUniqueViolation(err, "idempotency_keys_key_user_key") {
		s, found, lerr := lookup(ctx, pool, key, user, endpoint)
		if lerr != nil {
			return Stored{}, false, lerr
		}
		if found {
			return s, true, nil
		}
	}
	return Stored{}, false, err
}

func lookup(ctx context.Context, pool *pgxpool.Pool, key IdempotencyKey, user, endpoint string) (Stored, bool, error) {
	var hash, ep string
	var s Stored
	err := pool.QueryRow(ctx, `
		SELECT request_hash, endpoint, response_status, response_body
		FROM idempotency_keys WHERE key = $1 AND user_id = $2`, key.Key, user).Scan(&hash, &ep, &s.Status, &s.Body)
	if errors.Is(err, pgx.ErrNoRows) {
		return Stored{}, false, nil
	}
	if err != nil {
		return Stored{}, false, err
	}
	if hash != key.Hash || ep != endpoint {
		return Stored{}, false, httpx.Domain(httpx.ReasonIdempotencyMismatch, httpx.MsgIdempotencyMismatch, http.StatusUnprocessableEntity).
			WithDiagnostic("same key, different body or endpoint")
	}
	return s, true, nil
}

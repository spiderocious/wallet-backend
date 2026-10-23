// Package middleware holds every gin.HandlerFunc installed by app.Engine.
// Order of installation is decided there and commented there.
package middleware

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/time/rate"

	"github.com/spiderocious/devfest-akure-2026/wallet/backend/internal/httpx"
	"github.com/spiderocious/devfest-akure-2026/wallet/backend/internal/ids"
)

// ---- request id ----

// RequestID echoes an incoming X-Request-Id or generates one, stores it on the
// gin context and on the request context so services can log it.
func RequestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.GetHeader("X-Request-Id")
		if id == "" {
			id = ids.New("req_")
		}
		httpx.SetRequestID(c, id)
		c.Header("X-Request-Id", id)
		c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), ctxRequestID{}, id))
		c.Next()
	}
}

type ctxRequestID struct{}

// RequestIDFrom reads the request id from a context (for services).
func RequestIDFrom(ctx context.Context) string {
	v, _ := ctx.Value(ctxRequestID{}).(string)
	return v
}

// ---- recover ----

// Recover turns a panic into the standard 500 envelope instead of Gin's
// plain-text response, and logs it with the request id.
func Recover() gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if r := recover(); r != nil {
				httpx.Fail(c, fmt.Errorf("panic: %v", r))
			}
		}()
		c.Next()
	}
}

// ---- logging ----

// Logger writes one line per request after it completes.
func Logger(log *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		log.Info("http",
			"method", c.Request.Method,
			"path", c.FullPath(),
			"status", c.Writer.Status(),
			"ms", time.Since(start).Milliseconds(),
			"request_id", httpx.RequestID(c),
		)
	}
}

// ---- auth ----

// Claims is the JWT payload. The subject is the user id.
type Claims struct {
	jwt.RegisteredClaims
}

type ctxUserID struct{}

// UserIDFrom reads the authenticated user id from a context. Services use
// this; they never receive the Gin context.
func UserIDFrom(ctx context.Context) string {
	v, _ := ctx.Value(ctxUserID{}).(string)
	return v
}

// WithUserID is used by tests to build a context as auth would.
func WithUserID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, ctxUserID{}, id)
}

// SignToken issues a bearer token for a user.
func SignToken(secret, userID string, ttl time.Duration) (string, error) {
	now := time.Now()
	t := jwt.NewWithClaims(jwt.SigningMethodHS256, Claims{RegisteredClaims: jwt.RegisteredClaims{
		Subject:   userID,
		IssuedAt:  jwt.NewNumericDate(now),
		ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
	}})
	return t.SignedString([]byte(secret))
}

// Auth requires a valid bearer token and places the user id on the context.
func Auth(secret string) gin.HandlerFunc {
	return func(c *gin.Context) {
		h := c.GetHeader("Authorization")
		if !strings.HasPrefix(h, "Bearer ") {
			httpx.Fail(c, httpx.Domain(httpx.ReasonTokenInvalid, httpx.MsgTokenInvalid, http.StatusUnauthorized))
			return
		}
		var claims Claims
		_, err := jwt.ParseWithClaims(strings.TrimPrefix(h, "Bearer "), &claims, func(t *jwt.Token) (any, error) {
			if t.Method != jwt.SigningMethodHS256 {
				return nil, fmt.Errorf("unexpected signing method")
			}
			return []byte(secret), nil
		})
		if err != nil || claims.Subject == "" {
			httpx.Fail(c, httpx.Domain(httpx.ReasonTokenInvalid, httpx.MsgTokenInvalid, http.StatusUnauthorized))
			return
		}
		c.Request = c.Request.WithContext(WithUserID(c.Request.Context(), claims.Subject))
		c.Next()
	}
}

// ---- rate limit ----

// RateLimit is a token bucket per authenticated user (falling back to the
// client IP). On 429 it sets Retry-After so the client can back off honestly.
func RateLimit(perMinute int, burst int) gin.HandlerFunc {
	var mu sync.Mutex
	buckets := map[string]*rate.Limiter{}
	every := rate.Every(time.Minute / time.Duration(perMinute))
	return func(c *gin.Context) {
		key := UserIDFrom(c.Request.Context())
		if key == "" {
			key = c.ClientIP()
		}
		mu.Lock()
		l, ok := buckets[key]
		if !ok {
			l = rate.NewLimiter(every, burst)
			buckets[key] = l
		}
		mu.Unlock()
		if !l.Allow() {
			e := httpx.Domain(httpx.ReasonRateLimited, httpx.MsgRateLimited, http.StatusTooManyRequests)
			e.RetryAfter = int(time.Minute.Seconds()) / perMinute
			if e.RetryAfter < 1 {
				e.RetryAfter = 1
			}
			httpx.Fail(c, e)
			return
		}
		c.Next()
	}
}

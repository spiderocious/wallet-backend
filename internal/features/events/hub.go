// Package events pushes server-sent events to a user's open sessions. The
// hub is in-process; one replica is enough for the demo and the code says so.
package events

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/spiderocious/devfest-akure-2026/wallet/backend/internal/middleware"
)

// Event is one message on the stream.
type Event struct {
	Type string `json:"type"`
	Data any    `json:"data"`
}

// Hub fans events out to subscribers keyed by user id.
type Hub struct {
	mu   sync.RWMutex
	subs map[string]map[chan Event]struct{}
}

// NewHub returns an empty hub.
func NewHub() *Hub { return &Hub{subs: map[string]map[chan Event]struct{}{}} }

// Subscribe registers a channel for a user. The returned function removes it.
func (h *Hub) Subscribe(userID string) (<-chan Event, func()) {
	ch := make(chan Event, 16)
	h.mu.Lock()
	if h.subs[userID] == nil {
		h.subs[userID] = map[chan Event]struct{}{}
	}
	h.subs[userID][ch] = struct{}{}
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		delete(h.subs[userID], ch)
		if len(h.subs[userID]) == 0 {
			delete(h.subs, userID)
		}
		h.mu.Unlock()
		close(ch)
	}
}

// Publish delivers an event to every open session for the user. A slow
// subscriber is skipped rather than blocking the publisher.
func (h *Hub) Publish(userID string, e Event) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for ch := range h.subs[userID] {
		select {
		case ch <- e:
		default:
		}
	}
}

// Subscribers reports how many sessions a user has open, for tests and ops.
func (h *Hub) Subscribers(userID string) int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.subs[userID])
}

// keepAlive is the comment-frame interval; proxies close idle streams.
const keepAlive = 20 * time.Second

// Stream is the SSE handler. It is long-lived by design and exits when the
// client disconnects. http.Server must have WriteTimeout 0 for this route.
func Stream(hub *Hub) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID := middleware.UserIDFrom(c.Request.Context())
		ch, unsubscribe := hub.Subscribe(userID)
		defer unsubscribe()

		c.Writer.Header().Set("Content-Type", "text/event-stream")
		// no-transform tells compressing proxies to leave the stream alone;
		// a gzip buffer in front of SSE delays every event until it fills.
		c.Writer.Header().Set("Cache-Control", "no-cache, no-transform")
		c.Writer.Header().Set("Connection", "keep-alive")
		c.Writer.Header().Set("X-Accel-Buffering", "no")
		c.Status(http.StatusOK)
		// A first comment frame gives every proxy on the path a byte to
		// forward, so the client sees the stream open now, not at the first
		// event or keep-alive.
		if _, err := fmt.Fprint(c.Writer, ": connected\n\n"); err != nil {
			return
		}
		c.Writer.Flush()

		ctx := c.Request.Context()
		tick := time.NewTicker(keepAlive)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				// A write error means the client went away; the context
				// cancels on the next loop, so there is nothing to do here.
				if _, err := fmt.Fprint(c.Writer, ": ping\n\n"); err != nil {
					return
				}
				c.Writer.Flush()
			case e, ok := <-ch:
				if !ok {
					return
				}
				b, err := json.Marshal(e.Data)
				if err != nil {
					continue
				}
				if _, err := fmt.Fprintf(c.Writer, "event: %s\ndata: %s\n\n", e.Type, b); err != nil {
					return
				}
				c.Writer.Flush()
			}
		}
	}
}

// Register mounts the stream on the authenticated group.
func Register(rg *gin.RouterGroup, hub *Hub) {
	rg.GET("/events", Stream(hub))
}

// WaitFor is a test helper: it blocks until an event of the type arrives or
// the context ends.
func WaitFor(ctx context.Context, ch <-chan Event, typ string) (Event, bool) {
	for {
		select {
		case <-ctx.Done():
			return Event{}, false
		case e, ok := <-ch:
			if !ok {
				return Event{}, false
			}
			if e.Type == typ {
				return e, true
			}
		}
	}
}

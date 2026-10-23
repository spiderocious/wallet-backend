// Package pagination implements opaque cursor pagination. Offset pagination
// is not offered.
package pagination

import (
	"encoding/base64"
	"encoding/json"
	"strconv"
)

// Meta is the pagination block of a list response. Wire names are fixed
// and copied by clients, never retyped.
type Meta struct {
	NextCursor *string `json:"next_cursor"`
	HasMore    bool    `json:"has_more"`
}

// Cursor is what the client echoes back. Opaque on the wire.
type Cursor struct {
	LastID      string `json:"last_id"`
	LastSortKey string `json:"last_sort_key"`
}

// Encode serialises a cursor as base64url.
func Encode(c Cursor) string {
	b, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(b)
}

// Decode parses a cursor. A bad cursor is treated as no cursor.
func Decode(s string) (Cursor, bool) {
	if s == "" {
		return Cursor{}, false
	}
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return Cursor{}, false
	}
	var c Cursor
	if err := json.Unmarshal(b, &c); err != nil || c.LastID == "" {
		return Cursor{}, false
	}
	return c, true
}

// Limit clamps a client-supplied limit. Default 20, max 50.
func Limit(raw string) int {
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return 20
	}
	if n > 50 {
		return 50
	}
	return n
}

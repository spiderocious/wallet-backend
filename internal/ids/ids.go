// Package ids generates resource-prefixed ULIDs. IDs are opaque strings to
// clients; nothing parses them.
package ids

import (
	"crypto/rand"
	"time"

	"github.com/oklog/ulid/v2"
)

// Prefixes are fixed per resource so an id reveals its type in logs.
const (
	User     = "u_"
	Wallet   = "w_"
	Transfer = "tx_"
	Ledger   = "le_"

	Idem      = "ik_"
	Telemetry = "te_"
)

// New returns a prefixed ULID, monotonic within a millisecond.
func New(prefix string) string {
	return prefix + ulid.MustNew(ulid.Timestamp(time.Now()), rand.Reader).String()
}

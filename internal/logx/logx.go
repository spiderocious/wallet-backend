// Package logx builds the process logger. PII is redacted here, by attribute
// name, so callers cannot forget.
package logx

import (
	"log/slog"
	"os"
	"strings"
)

// redacted lists attribute keys whose values are never logged. Add to it when
// a new sensitive field appears; do not rely on callers to remember.
var redacted = map[string]struct{}{
	"pin":           {},
	"pin_hash":      {},
	"phone":         {},
	"authorization": {},
	"token":         {},
}

// New returns a JSON logger at the given level with redaction applied.
func New(level string) *slog.Logger {
	var lvl slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	h := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: lvl,
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			if _, ok := redacted[strings.ToLower(a.Key)]; ok {
				return slog.String(a.Key, "[REDACTED]")
			}
			return a
		},
	})
	return slog.New(h)
}

package observability

import (
	"log/slog"
	"os"
	"strings"
)

// NewLogger creates a structured JSON slog.Logger configured from LOG_LEVEL.
func NewLogger(levelStr string) *slog.Logger {
	var lvl slog.Level
	switch strings.ToLower(strings.TrimSpace(levelStr)) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn", "warning":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}

	handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: lvl,
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			if a.Key == slog.TimeKey {
				a.Key = "timestamp"
			}
			// Belt-and-suspenders redaction for common sensitive attribute keys.
			lowerKey := strings.ToLower(a.Key)
			if strings.Contains(lowerKey, "secret") ||
				strings.Contains(lowerKey, "password") ||
				strings.Contains(lowerKey, "authorization") ||
				lowerKey == "token" ||
				lowerKey == "database_url" {
				return slog.String(a.Key, "[REDACTED]")
			}
			return a
		},
	})

	return slog.New(handler)
}

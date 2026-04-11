// Package logutil provides shared logging utilities.
package logutil

import "log/slog"

// ParseLevel converts a string level name to slog.Level.
// Unknown values default to slog.LevelInfo.
func ParseLevel(level string) slog.Level {
	switch level {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

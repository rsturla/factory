// Package logging configures the global slog default logger.
//
// The FACTORY_LOG_FORMAT environment variable controls the output format:
//
//	"json" (default) — structured JSON, suitable for production log aggregators.
//	"text"           — human-readable text, useful during local development.
package logging

import (
	"log/slog"
	"os"
)

// Init sets the global slog default based on FACTORY_LOG_FORMAT.
// It should be called once at the top of main().
func Init() {
	format := os.Getenv("FACTORY_LOG_FORMAT")

	var handler slog.Handler
	switch format {
	case "text":
		handler = slog.NewTextHandler(os.Stderr, nil)
	default: // "json" or unset
		handler = slog.NewJSONHandler(os.Stderr, nil)
	}
	slog.SetDefault(slog.New(handler))
}

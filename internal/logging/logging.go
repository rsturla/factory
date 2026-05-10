// Package logging configures application and audit loggers.
//
// Environment variables:
//
//	FACTORY_LOG_FORMAT — "json" (default) or "text"
//	FACTORY_LOG_LEVEL  — "debug", "info" (default), "warn", "error"
//	FACTORY_AUDIT_LOG  — path to audit log file (default: stderr alongside app logs)
//
// The application logger (slog.Default) is level-gated for performance.
// At extreme scale, set FACTORY_LOG_LEVEL=warn to eliminate hot-path
// INFO logs (enqueue, claim, dispatch) while keeping warnings and errors.
//
// The audit logger (logging.Audit) always writes at INFO level regardless
// of FACTORY_LOG_LEVEL. It records authorization decisions and is intended
// for compliance. It writes to a separate destination when FACTORY_AUDIT_LOG
// is set, avoiding mutex contention with the application logger.
package logging

import (
	"io"
	"log/slog"
	"os"
)

// Audit is the audit logger for authorization and security events.
// Always writes at INFO level, independent of FACTORY_LOG_LEVEL.
// Defaults to slog.Default() if Init() has not been called.
var Audit *slog.Logger = slog.Default()

// Init configures the global application logger and the audit logger.
// Call once at the top of main().
func Init() {
	format := os.Getenv("FACTORY_LOG_FORMAT")

	var level slog.Level
	if v := os.Getenv("FACTORY_LOG_LEVEL"); v != "" {
		level.UnmarshalText([]byte(v))
	}

	opts := &slog.HandlerOptions{Level: level}
	var appHandler slog.Handler
	switch format {
	case "text":
		appHandler = slog.NewTextHandler(os.Stderr, opts)
	default:
		appHandler = slog.NewJSONHandler(os.Stderr, opts)
	}
	slog.SetDefault(slog.New(appHandler))

	var auditWriter io.Writer = os.Stderr
	if path := os.Getenv("FACTORY_AUDIT_LOG"); path != "" {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o640)
		if err != nil {
			slog.Error("failed to open audit log file, falling back to stderr", "path", path, "error", err)
		} else {
			auditWriter = f
		}
	}

	auditOpts := &slog.HandlerOptions{Level: slog.LevelInfo}
	switch format {
	case "text":
		Audit = slog.New(slog.NewTextHandler(auditWriter, auditOpts))
	default:
		Audit = slog.New(slog.NewJSONHandler(auditWriter, auditOpts))
	}
}

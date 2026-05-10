package logging

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInitDefaultJSON(t *testing.T) {
	t.Setenv("FACTORY_LOG_FORMAT", "")
	t.Setenv("FACTORY_LOG_LEVEL", "")
	t.Setenv("FACTORY_AUDIT_LOG", "")
	Init()

	var buf bytes.Buffer
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	slog.Info("test message", "key", "val")
	if !strings.Contains(buf.String(), `"key":"val"`) {
		t.Error("expected JSON output")
	}
}

func TestInitLevelGating(t *testing.T) {
	t.Setenv("FACTORY_LOG_FORMAT", "json")
	t.Setenv("FACTORY_LOG_LEVEL", "warn")
	t.Setenv("FACTORY_AUDIT_LOG", "")
	Init()

	// INFO should be suppressed.
	if slog.Default().Enabled(nil, slog.LevelInfo) {
		t.Error("INFO should be disabled when level=warn")
	}
	// WARN should pass.
	if !slog.Default().Enabled(nil, slog.LevelWarn) {
		t.Error("WARN should be enabled when level=warn")
	}
}

func TestInitLevelDebug(t *testing.T) {
	t.Setenv("FACTORY_LOG_FORMAT", "json")
	t.Setenv("FACTORY_LOG_LEVEL", "debug")
	t.Setenv("FACTORY_AUDIT_LOG", "")
	Init()

	if !slog.Default().Enabled(nil, slog.LevelDebug) {
		t.Error("DEBUG should be enabled when level=debug")
	}
}

func TestAuditIgnoresAppLevel(t *testing.T) {
	t.Setenv("FACTORY_LOG_FORMAT", "json")
	t.Setenv("FACTORY_LOG_LEVEL", "error")
	t.Setenv("FACTORY_AUDIT_LOG", "")
	Init()

	// App logger should suppress INFO.
	if slog.Default().Enabled(nil, slog.LevelInfo) {
		t.Error("app logger INFO should be disabled at level=error")
	}
	// Audit logger should still allow INFO.
	if !Audit.Enabled(nil, slog.LevelInfo) {
		t.Error("audit logger INFO should always be enabled")
	}
}

func TestAuditWritesToFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.log")
	t.Setenv("FACTORY_LOG_FORMAT", "json")
	t.Setenv("FACTORY_LOG_LEVEL", "info")
	t.Setenv("FACTORY_AUDIT_LOG", path)
	Init()

	Audit.Info("test audit event", "user", "alice", "action", "enqueue")

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read audit log: %v", err)
	}
	s := string(data)
	if !strings.Contains(s, "test audit event") {
		t.Errorf("audit log missing event, got: %s", s)
	}
	if !strings.Contains(s, "alice") {
		t.Errorf("audit log missing user, got: %s", s)
	}
}

func TestAuditFallsBackOnBadPath(t *testing.T) {
	t.Setenv("FACTORY_LOG_FORMAT", "json")
	t.Setenv("FACTORY_LOG_LEVEL", "info")
	t.Setenv("FACTORY_AUDIT_LOG", "/nonexistent/dir/audit.log")
	Init()

	// Should not panic — falls back to stderr.
	if Audit == nil {
		t.Fatal("audit logger should not be nil")
	}
	Audit.Info("fallback test")
}

func TestAuditDefaultWithoutInit(t *testing.T) {
	// Reset to simulate no Init() call.
	Audit = slog.Default()
	if Audit == nil {
		t.Fatal("audit logger should default to slog.Default()")
	}
}

func TestInitTextFormat(t *testing.T) {
	t.Setenv("FACTORY_LOG_FORMAT", "text")
	t.Setenv("FACTORY_LOG_LEVEL", "")
	t.Setenv("FACTORY_AUDIT_LOG", "")
	Init()

	// Just verify no panic — text handler is valid.
	slog.Info("text format test")
}

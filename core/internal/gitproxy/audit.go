package gitproxy

import (
	"context"
	"fmt"
	"time"

	v1 "gitlab.com/redhat/hummingbird/experimental/factory/core/pkg/api/v1"
	"gitlab.com/redhat/hummingbird/experimental/factory/core/internal/runstore"
)

// AuditLogger records git-proxy operations to run store.
type AuditLogger struct {
	store runstore.Store
}

// NewAuditLogger creates an audit logger.
func NewAuditLogger(store runstore.Store) *AuditLogger {
	return &AuditLogger{store: store}
}

// GitOperation represents a git operation through the proxy.
type GitOperation struct {
	RunID      string
	StageID    string
	Operation  string   // "clone", "fetch", "push"
	Repository string   // resource URL
	Ref        string   // branch/tag
	Paths      []string // files modified (for push)
	LineCount  int      // diff lines (for push)
	FileCount  int      // diff files (for push)
	Success    bool
	Error      string
	Timestamp  time.Time
}

// Log records a git operation to the audit trail.
func (a *AuditLogger) Log(ctx context.Context, op GitOperation) error {
	detail := map[string]any{
		"operation":  op.Operation,
		"repository": op.Repository,
		"ref":        op.Ref,
		"success":    op.Success,
		"timestamp":  op.Timestamp.Format(time.RFC3339),
	}

	if op.Operation == "push" {
		detail["paths"] = op.Paths
		detail["line_count"] = op.LineCount
		detail["file_count"] = op.FileCount
	}

	if !op.Success {
		detail["error"] = op.Error
	}

	event := &v1.AuditEvent{
		ID:        fmt.Sprintf("audit-%d", time.Now().UnixNano()),
		RunID:     op.RunID,
		StageID:   op.StageID,
		EventType: "git_operation",
		Detail:    detail,
		CreatedAt: op.Timestamp,
	}

	return a.store.CreateAuditEvent(ctx, event)
}

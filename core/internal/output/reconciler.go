package output

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/hummingbird-org/factory-workqueue/sdk/go/reconciler"
	v1 "gitlab.com/redhat/hummingbird/experimental/factory/core/pkg/api/v1"
	"gitlab.com/redhat/hummingbird/experimental/factory/core/internal/artifact"
	"gitlab.com/redhat/hummingbird/experimental/factory/core/internal/runstore"
	"gitlab.com/redhat/hummingbird/experimental/factory/core/internal/verification"
)

const (
	maxOutputSizeBytes = 50 * 1024 * 1024 // 50MB max output size
	maxOutputDepth     = 10                // max nesting depth for maps/arrays
)

// Reconciler processes output from completed stages.
type Reconciler struct {
	store         runstore.Store
	registry      *Registry
	verification  *verification.Runner
	artifactStore artifact.Store
	logger        *slog.Logger
}

// NewReconciler creates an output processor reconciler.
func NewReconciler(store runstore.Store, artifactStore artifact.Store, logger *slog.Logger) *Reconciler {
	return &Reconciler{
		store:         store,
		registry:      DefaultRegistry(),
		verification:  verification.DefaultRunner(),
		artifactStore: artifactStore,
		logger:        logger,
	}
}

// Reconcile processes a single output.
// Key format: "output:{stage-id}"
func (r *Reconciler) Reconcile(ctx context.Context, req reconciler.ProcessRequest) (reconciler.ProcessResponse, error) {
	// Extract stage ID from key
	if !strings.HasPrefix(req.Key, "output:") {
		return reconciler.ProcessResponse{}, fmt.Errorf("invalid key format: %s", req.Key)
	}
	stageID := req.Key[7:] // strip "output:" prefix

	// Fetch stage
	stage, err := r.store.GetStage(ctx, stageID)
	if err != nil {
		return reconciler.ProcessResponse{}, fmt.Errorf("get stage: %w", err)
	}

	r.logger.Info("processing output", "stage_id", stageID, "run_id", stage.RunID)

	// Stage should have output collected
	if stage.Output == nil {
		return reconciler.ProcessResponse{}, fmt.Errorf("stage has no output")
	}

	// Validate output size
	outputJSON, err := json.Marshal(stage.Output)
	if err != nil {
		return reconciler.ProcessResponse{}, fmt.Errorf("marshal output: %w", err)
	}
	if len(outputJSON) > maxOutputSizeBytes {
		r.logger.Error("output too large", "stage_id", stageID, "size", len(outputJSON), "max", maxOutputSizeBytes)
		stage.Phase = "failed"
		stage.Output = map[string]any{"error": "output exceeds size limit"}
		if err := r.store.UpdateStage(ctx, stage); err != nil {
			return reconciler.ProcessResponse{}, fmt.Errorf("update stage: %w", err)
		}
		return reconciler.Completed(), nil
	}

	// Validate output depth
	if err := validateDepth(stage.Output, 0); err != nil {
		r.logger.Error("output validation failed", "stage_id", stageID, "error", err)
		stage.Phase = "failed"
		stage.Output = map[string]any{"error": err.Error()}
		if err := r.store.UpdateStage(ctx, stage); err != nil {
			return reconciler.ProcessResponse{}, fmt.Errorf("update stage: %w", err)
		}
		return reconciler.Completed(), nil
	}

	// Get output type from output config
	outputType := stage.OutputConfig.Type
	if outputType == "" {
		r.logger.Warn("no output type specified, defaulting to report", "stage_id", stageID)
		outputType = "report"
	}

	// Run verification gates
	if err := r.verification.Run(ctx, stage); err != nil {
		r.logger.Error("verification failed", "stage_id", stageID, "error", err)
		stage.Phase = "failed"
		stage.Output = map[string]any{"error": fmt.Sprintf("verification failed: %v", err)}
		if err := r.store.UpdateStage(ctx, stage); err != nil {
			return reconciler.ProcessResponse{}, fmt.Errorf("update stage: %w", err)
		}
		return reconciler.Completed(), nil
	}

	// Get handler
	handler, err := r.registry.Get(outputType)
	if err != nil {
		return reconciler.ProcessResponse{}, fmt.Errorf("get handler: %w", err)
	}

	// Download artifacts if present
	output := stage.Output
	if artifactURL, ok := stage.Output["_artifact_url"].(string); ok && r.artifactStore != nil {
		r.logger.Info("downloading artifact", "stage_id", stageID, "url", artifactURL)

		// Download artifact
		artifactReader, err := r.artifactStore.Download(ctx, artifactURL)
		if err != nil {
			r.logger.Error("artifact download failed", "stage_id", stageID, "error", err)
			stage.Phase = "failed"
			stage.Output = map[string]any{"error": fmt.Sprintf("artifact download failed: %v", err)}
			if err := r.store.UpdateStage(ctx, stage); err != nil {
				return reconciler.ProcessResponse{}, fmt.Errorf("update stage: %w", err)
			}
			return reconciler.Completed(), nil
		}
		defer artifactReader.Close()

		// Extract files from tar.gz into memory
		files, err := artifact.ReadFilesFromTarGz(artifactReader)
		if err != nil {
			r.logger.Error("artifact extraction failed", "stage_id", stageID, "error", err)
			stage.Phase = "failed"
			stage.Output = map[string]any{"error": fmt.Sprintf("artifact extraction failed: %v", err)}
			if err := r.store.UpdateStage(ctx, stage); err != nil {
				return reconciler.ProcessResponse{}, fmt.Errorf("update stage: %w", err)
			}
			return reconciler.Completed(), nil
		}

		// Convert files map to output format expected by handlers
		var fileList []map[string]any
		for path, content := range files {
			fileList = append(fileList, map[string]any{
				"path":    path,
				"content": string(content),
			})
		}

		// Merge artifact files into output
		output = make(map[string]any)
		for k, v := range stage.Output {
			// Skip artifact metadata fields
			if k == "_artifact_url" || k == "_artifact_type" || k == "_artifact_size" {
				continue
			}
			output[k] = v
		}
		output["files"] = fileList

		r.logger.Info("artifact extracted", "stage_id", stageID, "file_count", len(fileList))
	}

	// Validate output format
	if err := handler.Validate(ctx, output); err != nil {
		r.logger.Error("output validation failed", "stage_id", stageID, "error", err)
		// Mark stage as failed
		stage.Phase = "failed"
		if err := r.store.UpdateStage(ctx, stage); err != nil {
			return reconciler.ProcessResponse{}, fmt.Errorf("update stage: %w", err)
		}
		return reconciler.Completed(), nil
	}

	// Execute output action
	result, err := handler.Execute(ctx, ExecuteParams{
		StageRun: stage,
		Output:   output,
		RunID:    stage.RunID,
	})
	if err != nil {
		r.logger.Error("output execution failed", "stage_id", stageID, "error", err)
		stage.Phase = "failed"
		if err := r.store.UpdateStage(ctx, stage); err != nil {
			return reconciler.ProcessResponse{}, fmt.Errorf("update stage: %w", err)
		}
		return reconciler.Completed(), nil
	}

	if !result.Success {
		r.logger.Warn("output execution unsuccessful", "stage_id", stageID, "message", result.Message)
		stage.Phase = "failed"
		if err := r.store.UpdateStage(ctx, stage); err != nil {
			return reconciler.ProcessResponse{}, fmt.Errorf("update stage: %w", err)
		}
		return reconciler.Completed(), nil
	}

	// Success - audit the result
	r.logger.Info("output processed successfully", "stage_id", stageID, "message", result.Message)

	auditEvent := &v1.AuditEvent{
		ID:        fmt.Sprintf("audit-%s-output", stageID),
		RunID:     stage.RunID,
		StageID:   stageID,
		EventType: "output_processed",
		Detail: map[string]any{
			"output_type": outputType,
			"message":     result.Message,
			"details":     result.Details,
		},
	}
	if err := r.store.CreateAuditEvent(ctx, auditEvent); err != nil {
		r.logger.Warn("failed to create audit event", "error", err)
		// Non-fatal, continue
	}

	// Output processing complete
	return reconciler.Completed(), nil
}

// validateDepth checks if a value's nesting depth exceeds maximum.
func validateDepth(v any, depth int) error {
	if depth > maxOutputDepth {
		return fmt.Errorf("output nesting too deep (max %d levels)", maxOutputDepth)
	}

	switch val := v.(type) {
	case map[string]any:
		for _, item := range val {
			if err := validateDepth(item, depth+1); err != nil {
				return err
			}
		}
	case []any:
		for _, item := range val {
			if err := validateDepth(item, depth+1); err != nil {
				return err
			}
		}
	}

	return nil
}

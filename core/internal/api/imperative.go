package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	v1 "gitlab.com/redhat/hummingbird/experimental/factory/core/pkg/api/v1"
	"gitlab.com/redhat/hummingbird/experimental/factory/core/internal/runstore"
)

// createImperativeRun handles POST /api/v1/runs (imperative mode).
// Creates run without pipeline spec — stages added via createImperativeStage.
func (s *Server) createImperativeRun(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)

	var req v1.ImperativeCreateRunRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, fmt.Sprintf("parse request: %v", err))
		return
	}

	runID := fmt.Sprintf("run-%d", time.Now().UnixNano())
	now := time.Now().UTC()

	// Create minimal run record (no pipeline spec)
	run := &v1.PipelineRun{
		ID:        runID,
		Phase:     "pending",
		CreatedAt: now,
		UpdatedAt: now,
	}

	if err := s.store.CreateRun(r.Context(), run); err != nil {
		s.logger.Error("create run", "error", err)
		s.errorResponse(w, http.StatusInternalServerError, "create run failed")
		return
	}

	resp := v1.ImperativeRunResponse{
		ID:        run.ID,
		Status:    run.Phase,
		CreatedAt: run.CreatedAt,
		UpdatedAt: run.UpdatedAt,
	}

	s.jsonResponse(w, http.StatusCreated, resp)
}

// createImperativeStage handles POST /api/v1/runs/{id}/stages (imperative mode).
// Creates stage and enqueues it for execution.
func (s *Server) createImperativeStage(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("id")
	if runID == "" {
		s.errorResponse(w, http.StatusBadRequest, "missing run ID")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 10<<20) // 10MB for inputs

	var req v1.ImperativeCreateStageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, fmt.Sprintf("parse request: %v", err))
		return
	}

	// Validate
	if req.Name == "" {
		s.errorResponse(w, http.StatusBadRequest, "name required")
		return
	}
	if req.Image == "" {
		s.errorResponse(w, http.StatusBadRequest, "image required")
		return
	}
	if len(req.Command) == 0 {
		s.errorResponse(w, http.StatusBadRequest, "command required")
		return
	}

	// Convert resources to named bindings
	resourceNames := make([]string, len(req.Resources))
	for i := range req.Resources {
		resourceNames[i] = fmt.Sprintf("resource-%d", i)
	}

	// Convert to AgentConfig
	agentConfig := v1.AgentConfig{
		Image:   req.Image,
		Command: req.Command,
		Prompt:  req.Prompt,
		Model:   req.Model,
		Credentials: func() []v1.CredentialBinding {
			var creds []v1.CredentialBinding
			for _, c := range req.Credentials {
				creds = append(creds, v1.CredentialBinding{
					Name:     c.Name,
					Provider: c.Provider,
				})
			}
			return creds
		}(),
		Resources:       resourceNames,
		Environment:     req.Environment,
		Timeout:         req.Timeout,
		ChangeDetection: req.ChangeDetection,
	}

	// Convert to OutputConfig
	outputConfig := v1.OutputConfig{
		Type: "report", // default
	}
	if req.Output != nil {
		outputConfig.Type = req.Output.Type
		outputConfig.BranchPrefix = req.Output.Branch
		// TODO: labels, reviewers, draft
	}

	stageID := fmt.Sprintf("stage-%d", time.Now().UnixNano())
	now := time.Now()

	stage := &v1.StageRun{
		ID:           stageID,
		RunID:        runID,
		StageName:    req.Name,
		Phase:        "pending",
		AgentConfig:  agentConfig,
		OutputConfig: outputConfig,
	}

	if err := s.store.CreateStage(r.Context(), stage); err != nil {
		s.logger.Error("create stage", "error", err, "run_id", runID, "stage", req.Name)
		s.errorResponse(w, http.StatusInternalServerError, "create stage failed")
		return
	}

	// Enqueue stage for execution (outbox pattern)
	outboxEntry := runstore.OutboxEntry{
		Queue:    "sf-stage",
		Key:      stageID,
		Priority: 0,
	}
	if err := s.store.OutboxEnqueue(r.Context(), outboxEntry); err != nil {
		s.logger.Error("enqueue stage", "error", err)
	}

	resp := v1.ImperativeStageResponse{
		ID:        stage.ID,
		RunID:     stage.RunID,
		Name:      stage.StageName,
		Status:    stage.Phase,
		CreatedAt: now,
		UpdatedAt: now,
	}

	s.jsonResponse(w, http.StatusCreated, resp)
}

// getImperativeStage handles GET /api/v1/runs/{run_id}/stages/{stage_id}.
// Returns stage status + output for SDK polling.
func (s *Server) getImperativeStage(w http.ResponseWriter, r *http.Request) {
	stageID := r.PathValue("stage_id")

	if stageID == "" {
		s.errorResponse(w, http.StatusBadRequest, "missing stage_id")
		return
	}

	stage, err := s.store.GetStage(r.Context(), stageID)
	if err != nil {
		s.logger.Error("get stage", "error", err, "stage_id", stageID)
		s.errorResponse(w, http.StatusNotFound, "stage not found")
		return
	}

	duration := 0.0
	if stage.StartedAt != nil && stage.CompletedAt != nil {
		duration = stage.CompletedAt.Sub(*stage.StartedAt).Seconds()
	}

	resp := v1.ImperativeStageResponse{
		ID:        stage.ID,
		RunID:     stage.RunID,
		Name:      stage.StageName,
		Status:    stage.Phase,
		Output:    stage.Output,
		ExitCode:  0, // TODO: store exit code
		Duration:  duration,
		CreatedAt: *stage.StartedAt,
		UpdatedAt: *stage.CompletedAt,
	}

	s.jsonResponse(w, http.StatusOK, resp)
}

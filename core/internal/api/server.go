// Package api implements the factory HTTP API service.
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	v1 "gitlab.com/redhat/hummingbird/experimental/factory/core/pkg/api/v1"
	"gitlab.com/redhat/hummingbird/experimental/factory/core/internal/runstore"
)

// Server implements factory HTTP API.
type Server struct {
	store  runstore.Store
	logger *slog.Logger
}

// NewServer creates API server.
func NewServer(store runstore.Store, logger *slog.Logger) *Server {
	return &Server{
		store:  store,
		logger: logger,
	}
}

// Handler returns HTTP handler with all routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// Imperative SDK routes
	mux.HandleFunc("POST /api/v1/imperative/runs", s.createImperativeRun)
	mux.HandleFunc("POST /api/v1/imperative/runs/{id}/stages", s.createImperativeStage)
	mux.HandleFunc("GET /api/v1/imperative/runs/{run_id}/stages/{stage_id}", s.getImperativeStage)

	// Legacy read-only routes
	mux.HandleFunc("GET /api/v1/runs", s.listRuns)
	mux.HandleFunc("GET /api/v1/runs/{id}", s.getRun)
	mux.HandleFunc("GET /api/v1/runs/{id}/stages", s.listStages)
	mux.HandleFunc("GET /api/v1/runs/{id}/events", s.listEvents)

	return mux
}

// listRuns handles GET /api/v1/runs.
func (s *Server) listRuns(w http.ResponseWriter, r *http.Request) {
	phase := r.URL.Query().Get("phase")
	pageToken := r.URL.Query().Get("page_token")

	filters := runstore.ListRunsFilters{
		Phase:     phase,
		Limit:     20,
		PageToken: pageToken,
	}

	runs, nextToken, err := s.store.ListRuns(r.Context(), filters)
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, fmt.Sprintf("list runs: %v", err))
		return
	}

	resp := v1.ListRunsResponse{
		Runs:          runs,
		NextPageToken: nextToken,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// getRun handles GET /api/v1/runs/{id}.
func (s *Server) getRun(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("id")

	run, err := s.store.GetRun(r.Context(), runID)
	if err != nil {
		s.errorResponse(w, http.StatusNotFound, fmt.Sprintf("get run: %v", err))
		return
	}

	stages, err := s.store.ListStages(r.Context(), runID)
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, fmt.Sprintf("list stages: %v", err))
		return
	}

	resp := v1.GetRunResponse{
		Run:    *run,
		Stages: stages,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// listStages handles GET /api/v1/runs/{id}/stages.
func (s *Server) listStages(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("id")

	stages, err := s.store.ListStages(r.Context(), runID)
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, fmt.Sprintf("list stages: %v", err))
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(stages)
}

// listEvents handles GET /api/v1/runs/{id}/events.
func (s *Server) listEvents(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("id")

	events, err := s.store.ListAuditEvents(r.Context(), runID)
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, fmt.Sprintf("list events: %v", err))
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(events)
}

// errorResponse writes error JSON response.
func (s *Server) errorResponse(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": message})
}

// jsonResponse writes success JSON response.
func (s *Server) jsonResponse(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

// StartOutboxPoller starts background outbox polling.
func (s *Server) StartOutboxPoller(ctx context.Context, enqueueEndpoint string) {
	go s.outboxPollerLoop(ctx, enqueueEndpoint)
}

// outboxPollerLoop polls outbox with exponential backoff on errors.
func (s *Server) outboxPollerLoop(ctx context.Context, enqueueEndpoint string) {
	// Import EnqueueClient from workqueue SDK
	// For Phase 1: simplified polling without actual enqueueing
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	s.logger.Info("outbox poller started")

	consecutiveErrors := 0
	maxBackoff := 30 * time.Second

	for {
		select {
		case <-ctx.Done():
			s.logger.Info("outbox poller stopped")
			return
		case <-ticker.C:
			if err := s.pollOutbox(ctx, enqueueEndpoint); err != nil {
				consecutiveErrors++
				s.logger.Error("outbox poll failed", "error", err, "consecutive_errors", consecutiveErrors)

				// Exponential backoff: 1s, 2s, 4s, 8s, 16s, max 30s
				backoff := time.Duration(1<<uint(consecutiveErrors-1)) * time.Second
				if backoff > maxBackoff {
					backoff = maxBackoff
				}

				s.logger.Info("backing off", "duration", backoff)
				ticker.Reset(backoff)
			} else {
				// Success - reset backoff
				if consecutiveErrors > 0 {
					consecutiveErrors = 0
					ticker.Reset(1 * time.Second)
				}
			}
		}
	}
}

// pollOutbox processes outbox entries.
func (s *Server) pollOutbox(ctx context.Context, enqueueEndpoint string) error {
	entries, err := s.store.OutboxPoll(ctx, 100)
	if err != nil {
		return fmt.Errorf("poll outbox: %w", err)
	}

	if len(entries) == 0 {
		return nil
	}

	s.logger.Debug("processing outbox entries", "count", len(entries))

	// Phase 1: mark as sent without actually enqueueing (workqueue not running)
	// Phase 2: use EnqueueClient to send to workqueue receiver
	ids := make([]int64, 0, len(entries))
	for _, entry := range entries {
		ids = append(ids, entry.ID)
		s.logger.Info("would enqueue", "queue", entry.Queue, "key", entry.Key)
	}

	if err := s.store.OutboxMarkSent(ctx, ids); err != nil {
		return fmt.Errorf("mark sent: %w", err)
	}

	return nil
}


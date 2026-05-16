package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"log/slog"
	"os"

	v1 "gitlab.com/redhat/hummingbird/experimental/factory/core/pkg/api/v1"
	"gitlab.com/redhat/hummingbird/experimental/factory/core/internal/runstore"
	"gitlab.com/redhat/hummingbird/experimental/factory/core/internal/runstore/inmem"
)

func TestCreateRun(t *testing.T) {
	store := inmem.New()
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))

	// Point to example pipeline
	srv := NewServer(store, "../../examples/simple-test/.factory", logger)

	req := v1.CreateRunRequest{
		PipelineRepo: "github.com/test/test",
		PipelinePath: "test",
		PipelineRef:  "main",
		Parameters: map[string]string{
			"resource.test-repo.url": "github.com/test/repo",
		},
		Priority: 5,
	}

	body, _ := json.Marshal(req)
	httpReq := httptest.NewRequest("POST", "/api/v1/runs", bytes.NewReader(body))
	w := httptest.NewRecorder()

	srv.createRun(w, httpReq)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	var run v1.PipelineRun
	if err := json.NewDecoder(w.Body).Decode(&run); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if run.ID == "" {
		t.Error("expected run ID to be set")
	}
	if run.Phase != "pending" {
		t.Errorf("expected phase pending, got %s", run.Phase)
	}
	if run.PipelineSpec.Name != "simple-test" {
		t.Errorf("expected pipeline name simple-test, got %s", run.PipelineSpec.Name)
	}

	// Verify run stored
	fetchedRun, err := store.GetRun(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("get run from store: %v", err)
	}
	if fetchedRun.ID != run.ID {
		t.Errorf("run ID mismatch: %s vs %s", fetchedRun.ID, run.ID)
	}

	// Verify outbox entry
	entries, err := store.OutboxPoll(context.Background(), 10)
	if err != nil {
		t.Fatalf("poll outbox: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 outbox entry, got %d", len(entries))
	}
	if entries[0].Queue != "sf-pipeline" {
		t.Errorf("expected queue sf-pipeline, got %s", entries[0].Queue)
	}
}

func TestListRuns(t *testing.T) {
	store := inmem.New()
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	srv := NewServer(store, "../../examples/simple-test/.factory", logger)

	// Create a run first via API
	req := v1.CreateRunRequest{
		PipelineRepo: "github.com/test/test",
		PipelinePath: "test",
		PipelineRef:  "main",
		Parameters:   map[string]string{},
		Priority:     5,
	}

	body, _ := json.Marshal(req)
	httpReq := httptest.NewRequest("POST", "/api/v1/runs", bytes.NewReader(body))
	w := httptest.NewRecorder()
	srv.createRun(w, httpReq)

	// List runs
	httpReq = httptest.NewRequest("GET", "/api/v1/runs", nil)
	w = httptest.NewRecorder()
	srv.listRuns(w, httpReq)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp v1.ListRunsResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if len(resp.Runs) != 1 {
		t.Errorf("expected 1 run, got %d", len(resp.Runs))
	}
}

func TestGetRun(t *testing.T) {
	store := inmem.New()
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	srv := NewServer(store, "../../examples/simple-test/.factory", logger)

	// Create run
	req := v1.CreateRunRequest{
		PipelineRepo: "github.com/test/test",
		PipelinePath: "test",
		PipelineRef:  "main",
		Parameters:   map[string]string{},
		Priority:     5,
	}

	body, _ := json.Marshal(req)
	httpReq := httptest.NewRequest("POST", "/api/v1/runs", bytes.NewReader(body))
	w := httptest.NewRecorder()
	srv.createRun(w, httpReq)

	var run v1.PipelineRun
	json.NewDecoder(w.Body).Decode(&run)

	// Get run
	httpReq = httptest.NewRequest("GET", "/api/v1/runs/"+run.ID, nil)
	httpReq.SetPathValue("id", run.ID)
	w = httptest.NewRecorder()
	srv.getRun(w, httpReq)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp v1.GetRunResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if resp.Run.ID != run.ID {
		t.Errorf("run ID mismatch: %s vs %s", resp.Run.ID, run.ID)
	}
	if len(resp.Stages) != 0 {
		t.Errorf("expected 0 stages (not created yet), got %d", len(resp.Stages))
	}
}

func TestOutboxPoller(t *testing.T) {
	store := inmem.New()
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	srv := NewServer(store, "../../examples/simple-test/.factory", logger)

	// Create outbox entry manually
	entry := runstore.OutboxEntry{
		Queue:    "sf-pipeline",
		Key:      "run:test-123",
		Priority: 5,
	}
	if err := store.OutboxEnqueue(context.Background(), entry); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	// Poll once
	if err := srv.pollOutbox(context.Background(), "http://localhost:8081"); err != nil {
		t.Fatalf("poll outbox: %v", err)
	}

	// Verify marked as sent
	entries, err := store.OutboxPoll(context.Background(), 10)
	if err != nil {
		t.Fatalf("poll after processing: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected 0 unsent entries after poll, got %d", len(entries))
	}
}

func TestCreateRun_Validation(t *testing.T) {
	store := inmem.New()
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	srv := NewServer(store, "../../examples/simple-test/.factory", logger)

	tests := []struct {
		name       string
		req        v1.CreateRunRequest
		expectCode int
	}{
		{
			name: "valid request",
			req: v1.CreateRunRequest{
				PipelineRepo: "github.com/test/test",
				PipelinePath: "test",
				PipelineRef:  "main",
				Parameters:   map[string]string{},
				Priority:     5,
			},
			expectCode: http.StatusCreated,
		},
		{
			name: "path traversal blocked",
			req: v1.CreateRunRequest{
				PipelineRepo: "github.com/test/test",
				PipelinePath: "../../etc/passwd",
				PipelineRef:  "main",
				Parameters:   map[string]string{},
				Priority:     5,
			},
			expectCode: http.StatusBadRequest,
		},
		{
			name: "absolute path blocked",
			req: v1.CreateRunRequest{
				PipelineRepo: "github.com/test/test",
				PipelinePath: "/etc/passwd",
				PipelineRef:  "main",
				Parameters:   map[string]string{},
				Priority:     5,
			},
			expectCode: http.StatusBadRequest,
		},
		{
			name: "priority out of range",
			req: v1.CreateRunRequest{
				PipelineRepo: "github.com/test/test",
				PipelinePath: "test",
				PipelineRef:  "main",
				Parameters:   map[string]string{},
				Priority:     99,
			},
			expectCode: http.StatusBadRequest,
		},
		{
			name: "missing repo",
			req: v1.CreateRunRequest{
				PipelineRepo: "",
				PipelinePath: "test",
				PipelineRef:  "main",
				Parameters:   map[string]string{},
				Priority:     5,
			},
			expectCode: http.StatusBadRequest,
		},
		{
			name: "embedded path traversal",
			req: v1.CreateRunRequest{
				PipelineRepo: "github.com/test/test",
				PipelinePath: "foo/../../../etc/passwd",
				PipelineRef:  "main",
				Parameters:   map[string]string{},
				Priority:     5,
			},
			expectCode: http.StatusBadRequest,
		},
		{
			name: "backslash traversal",
			req: v1.CreateRunRequest{
				PipelineRepo: "github.com/test/test",
				PipelinePath: "..\\..\\windows\\system32",
				PipelineRef:  "main",
				Parameters:   map[string]string{},
				Priority:     5,
			},
			expectCode: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body, _ := json.Marshal(tt.req)
			httpReq := httptest.NewRequest("POST", "/api/v1/runs", bytes.NewReader(body))
			w := httptest.NewRecorder()

			srv.createRun(w, httpReq)

			if w.Code != tt.expectCode {
				t.Errorf("expected code %d, got %d: %s", tt.expectCode, w.Code, w.Body.String())
			}
		})
	}
}

func TestCreateRun_Atomicity(t *testing.T) {
	store := inmem.New()
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	srv := NewServer(store, "../../examples/simple-test/.factory", logger)

	req := v1.CreateRunRequest{
		PipelineRepo: "github.com/test/test",
		PipelinePath: "test",
		PipelineRef:  "main",
		Parameters:   map[string]string{},
		Priority:     5,
	}

	body, _ := json.Marshal(req)
	httpReq := httptest.NewRequest("POST", "/api/v1/runs", bytes.NewReader(body))
	w := httptest.NewRecorder()

	srv.createRun(w, httpReq)

	if w.Code != http.StatusCreated {
		t.Fatalf("create run failed: %d %s", w.Code, w.Body.String())
	}

	var run v1.PipelineRun
	if err := json.NewDecoder(w.Body).Decode(&run); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	// Verify both run and outbox entry created atomically
	fetchedRun, err := store.GetRun(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if fetchedRun.ID != run.ID {
		t.Errorf("run ID mismatch")
	}

	entries, err := store.OutboxPoll(context.Background(), 10)
	if err != nil {
		t.Fatalf("poll outbox: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 outbox entry, got %d", len(entries))
	}
	if entries[0].Key != "run:"+run.ID {
		t.Errorf("outbox key mismatch: expected run:%s, got %s", run.ID, entries[0].Key)
	}
}

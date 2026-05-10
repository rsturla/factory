package client_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/hummingbird-org/factory-workqueue/internal/authz/noop"
	"github.com/hummingbird-org/factory-workqueue/internal/store/inmem"
	"github.com/hummingbird-org/factory-workqueue/internal/wqapi"
	"github.com/hummingbird-org/factory-workqueue/pkg/client"
	"github.com/hummingbird-org/factory-workqueue/pkg/types"
)

func setupTestServer(t *testing.T) (*httptest.Server, *inmem.Store) {
	t.Helper()
	s := inmem.New()
	if err := s.EnsureQueue(context.Background(), "test", types.QueueConfig{MaxConcurrency: 10, MaxRetry: 3}); err != nil {
		t.Fatalf("EnsureQueue: %v", err)
	}
	handler := wqapi.NewHandler(s, noop.Authorizer{})
	mux := http.NewServeMux()
	handler.Register(mux)
	return httptest.NewServer(mux), s
}

func TestEnqueue(t *testing.T) {
	srv, s := setupTestServer(t)
	defer srv.Close()

	c := client.NewWorkqueueClient(srv.URL)
	ctx := context.Background()

	if err := c.Enqueue(ctx, "test", "key-1", 10); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	// Verify via the backing store.
	counts, err := s.CountByStatus(ctx, "test")
	if err != nil {
		t.Fatalf("CountByStatus: %v", err)
	}
	if counts[types.StatusPending] != 1 {
		t.Errorf("expected 1 pending, got %d", counts[types.StatusPending])
	}
}

func TestClaimBatch(t *testing.T) {
	srv, s := setupTestServer(t)
	defer srv.Close()

	c := client.NewWorkqueueClient(srv.URL)
	ctx := context.Background()

	s.Enqueue(ctx, "test", "key-1", 10)
	s.Enqueue(ctx, "test", "key-2", 20)

	items, err := c.ClaimBatch(ctx, "test", 2, "worker-1", time.Hour)
	if err != nil {
		t.Fatalf("ClaimBatch: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 claimed, got %d", len(items))
	}

	// Higher priority first.
	if items[0].Key != "key-2" {
		t.Errorf("expected key-2 first (higher priority), got %s", items[0].Key)
	}
}

func TestComplete(t *testing.T) {
	srv, s := setupTestServer(t)
	defer srv.Close()

	c := client.NewWorkqueueClient(srv.URL)
	ctx := context.Background()

	s.Enqueue(ctx, "test", "key-1", 0)
	s.ClaimBatch(ctx, "test", 1, "w", time.Hour)

	if err := c.Complete(ctx, "test", "key-1"); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	counts, _ := s.CountByStatus(ctx, "test")
	if counts[types.StatusSucceeded] != 1 {
		t.Errorf("expected 1 succeeded, got %d", counts[types.StatusSucceeded])
	}
}

func TestCountByStatus(t *testing.T) {
	srv, s := setupTestServer(t)
	defer srv.Close()

	c := client.NewWorkqueueClient(srv.URL)
	ctx := context.Background()

	for i := range 5 {
		s.Enqueue(ctx, "test", fmt.Sprintf("key-%d", i), 0)
	}
	s.ClaimBatch(ctx, "test", 2, "w", time.Hour)

	counts, err := c.CountByStatus(ctx, "test")
	if err != nil {
		t.Fatalf("CountByStatus: %v", err)
	}
	if counts[types.StatusPending] != 3 {
		t.Errorf("expected 3 pending, got %d", counts[types.StatusPending])
	}
	if counts[types.StatusClaimed] != 2 {
		t.Errorf("expected 2 claimed, got %d", counts[types.StatusClaimed])
	}
}

func TestListQueues(t *testing.T) {
	srv, _ := setupTestServer(t)
	defer srv.Close()

	c := client.NewWorkqueueClient(srv.URL)
	ctx := context.Background()

	queues, err := c.ListQueues(ctx)
	if err != nil {
		t.Fatalf("ListQueues: %v", err)
	}
	if len(queues) < 1 {
		t.Fatal("expected at least 1 queue")
	}
	found := false
	for _, q := range queues {
		if q.Name == "test" {
			found = true
		}
	}
	if !found {
		t.Error("queue 'test' not found in ListQueues")
	}
}

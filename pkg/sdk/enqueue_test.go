package sdk_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hummingbird-org/factory-workqueue/pkg/sdk"
)

func TestEnqueueSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/enqueue" {
			t.Errorf("expected /enqueue, got %s", r.URL.Path)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("expected Content-Type application/json, got %s", ct)
		}

		var req sdk.EnqueueRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("failed to decode request body: %v", err)
		}
		if req.Queue != "build-queue" {
			t.Errorf("expected queue build-queue, got %s", req.Queue)
		}
		if req.Key != "test-key" {
			t.Errorf("expected key test-key, got %s", req.Key)
		}
		if req.Priority != 42 {
			t.Errorf("expected priority 42, got %d", req.Priority)
		}

		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := sdk.NewEnqueueClient(srv.URL)
	err := client.Enqueue(context.Background(), "build-queue", "test-key", 42)
	if err != nil {
		t.Fatal(err)
	}
}

func TestEnqueueCreated(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	client := sdk.NewEnqueueClient(srv.URL)
	err := client.Enqueue(context.Background(), "q", "k", 0)
	if err != nil {
		t.Fatalf("expected 201 to be accepted: %v", err)
	}
}

func TestEnqueueServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	client := sdk.NewEnqueueClient(srv.URL)
	err := client.Enqueue(context.Background(), "q", "k", 0)
	if err == nil {
		t.Fatal("expected error for HTTP 500")
	}
}

func TestEnqueueContextCancelled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	client := sdk.NewEnqueueClient(srv.URL)
	err := client.Enqueue(ctx, "q", "k", 0)
	if err == nil {
		t.Fatal("expected error for cancelled context")
	}
}

func TestEnqueueUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close() // close immediately

	client := sdk.NewEnqueueClient(srv.URL)
	err := client.Enqueue(context.Background(), "q", "k", 0)
	if err == nil {
		t.Fatal("expected error for unreachable server")
	}
}

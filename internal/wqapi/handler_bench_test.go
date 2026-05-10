package wqapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/hummingbird-org/factory-workqueue/internal/authz/noop"
	"github.com/hummingbird-org/factory-workqueue/internal/store"
	"github.com/hummingbird-org/factory-workqueue/internal/store/inmem"
	"github.com/hummingbird-org/factory-workqueue/internal/wqapi"
)

func TestMain(m *testing.M) {
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	os.Exit(m.Run())
}

func setupBenchHandler(b *testing.B) http.Handler {
	b.Helper()
	s := inmem.New()
	s.EnsureQueue(context.Background(), "bench", store.QueueConfig{MaxConcurrency: 100000, MaxRetry: 5})
	h := wqapi.NewHandler(s, noop.Authorizer{})
	mux := http.NewServeMux()
	h.Register(mux)
	return mux
}

func benchPost(handler http.Handler, path string, body any) *httptest.ResponseRecorder {
	data, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", path, bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	return rr
}

func BenchmarkWqapiEnqueue(b *testing.B) {
	mux := setupBenchHandler(b)

	b.ResetTimer()
	for i := range b.N {
		rr := benchPost(mux, "/wq/enqueue", map[string]any{
			"queue":    "bench",
			"key":      fmt.Sprintf("enq-%08d", i),
			"priority": i % 10,
		})
		if rr.Code != http.StatusOK {
			b.Fatalf("enqueue: want 200, got %d", rr.Code)
		}
	}
}

func BenchmarkWqapiClaimComplete(b *testing.B) {
	mux := setupBenchHandler(b)

	// Pre-enqueue items.
	for i := range b.N {
		benchPost(mux, "/wq/enqueue", map[string]any{
			"queue":    "bench",
			"key":      fmt.Sprintf("cc-%08d", i),
			"priority": i % 10,
		})
	}

	b.ResetTimer()
	for range b.N {
		// Claim one item.
		rr := benchPost(mux, "/wq/claim", map[string]any{
			"queue":          "bench",
			"batch_size":     1,
			"worker_id":      "w1",
			"lease_duration": "1h",
		})
		if rr.Code != http.StatusOK {
			b.Fatalf("claim: want 200, got %d", rr.Code)
		}
		var items []store.WorkItem
		json.NewDecoder(rr.Body).Decode(&items)
		if len(items) == 0 {
			b.Fatal("claim returned 0 items")
		}

		// Complete the item.
		rr = benchPost(mux, "/wq/complete", map[string]any{
			"queue": "bench",
			"key":   items[0].Key,
		})
		if rr.Code != http.StatusOK {
			b.Fatalf("complete: want 200, got %d", rr.Code)
		}
	}
}

func BenchmarkWqapiCountByStatus(b *testing.B) {
	mux := setupBenchHandler(b)

	// Seed some items in various states.
	for i := range 100 {
		benchPost(mux, "/wq/enqueue", map[string]any{
			"queue":    "bench",
			"key":      fmt.Sprintf("cnt-%04d", i),
			"priority": i % 10,
		})
	}
	// Claim half to create mixed state.
	benchPost(mux, "/wq/claim", map[string]any{
		"queue":          "bench",
		"batch_size":     50,
		"worker_id":      "w1",
		"lease_duration": "1h",
	})

	b.ResetTimer()
	for range b.N {
		rr := benchPost(mux, "/wq/count", map[string]any{
			"queue": "bench",
		})
		if rr.Code != http.StatusOK {
			b.Fatalf("count: want 200, got %d", rr.Code)
		}
	}
}

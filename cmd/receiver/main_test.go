package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hummingbird-org/factory-workqueue/internal/store"
	"github.com/hummingbird-org/factory-workqueue/internal/store/inmem"
)

func setupStore(t *testing.T) store.Interface {
	t.Helper()
	s := inmem.New()
	if err := s.EnsureQueue(context.Background(), "test", store.QueueConfig{MaxConcurrency: 10, MaxRetry: 3}); err != nil {
		t.Fatalf("EnsureQueue: %v", err)
	}
	return s
}

// ---------- enqueueHandler tests ----------

func TestEnqueueSuccess(t *testing.T) {
	s := setupStore(t)
	h := &enqueueHandler{queue: "test", store: s}

	body := `{"key":"item-1","priority":5}`
	req := httptest.NewRequest(http.MethodPost, "/enqueue", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var resp enqueueResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Status != "enqueued" {
		t.Errorf("expected status 'enqueued', got %q", resp.Status)
	}
	if resp.Key != "item-1" {
		t.Errorf("expected key 'item-1', got %q", resp.Key)
	}

	// Verify item actually exists in the store.
	item, err := s.GetItem(context.Background(), "test", "item-1")
	if err != nil {
		t.Fatalf("GetItem: %v", err)
	}
	if item.Priority != 5 {
		t.Errorf("expected priority 5, got %d", item.Priority)
	}
}

func TestEnqueueInvalidJSON(t *testing.T) {
	s := setupStore(t)
	h := &enqueueHandler{queue: "test", store: s}

	req := httptest.NewRequest(http.MethodPost, "/enqueue", strings.NewReader(`{not json`))
	rr := httptest.NewRecorder()

	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestEnqueueEmptyKey(t *testing.T) {
	s := setupStore(t)
	h := &enqueueHandler{queue: "test", store: s}

	body := `{"key":"","priority":1}`
	req := httptest.NewRequest(http.MethodPost, "/enqueue", strings.NewReader(body))
	rr := httptest.NewRecorder()

	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "key is required") {
		t.Errorf("expected 'key is required' in body, got %q", rr.Body.String())
	}
}

func TestEnqueueDuplicate(t *testing.T) {
	s := setupStore(t)
	h := &enqueueHandler{queue: "test", store: s}

	for i := 0; i < 2; i++ {
		body := `{"key":"dup-key","priority":3}`
		req := httptest.NewRequest(http.MethodPost, "/enqueue", strings.NewReader(body))
		rr := httptest.NewRecorder()

		h.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("attempt %d: expected 200, got %d: %s", i+1, rr.Code, rr.Body.String())
		}
	}
}

func TestEnqueueContentType(t *testing.T) {
	s := setupStore(t)
	h := &enqueueHandler{queue: "test", store: s}

	body := `{"key":"ct-item","priority":1}`
	req := httptest.NewRequest(http.MethodPost, "/enqueue", strings.NewReader(body))
	rr := httptest.NewRecorder()

	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	ct := rr.Header().Get("Content-Type")
	if ct != "application/json" {
		t.Errorf("expected Content-Type 'application/json', got %q", ct)
	}
}

func TestEnqueueMaxBodySize(t *testing.T) {
	s := setupStore(t)
	h := &enqueueHandler{queue: "test", store: s}

	// Build a body that exceeds maxRequestBodySize (10 MiB).
	// We craft valid-looking JSON that is just too large.
	bigValue := strings.Repeat("x", maxRequestBodySize+1)
	body := `{"key":"` + bigValue + `"}`

	req := httptest.NewRequest(http.MethodPost, "/enqueue", strings.NewReader(body))
	rr := httptest.NewRecorder()

	h.ServeHTTP(rr, req)

	// MaxBytesReader causes the JSON decoder to fail with a 400,
	// or the handler may return 413. Either indicates rejection.
	if rr.Code != http.StatusBadRequest && rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 400 or 413, got %d: %s", rr.Code, rr.Body.String())
	}
}

// ---------- enqueueBatchHandler tests ----------

func TestEnqueueBatchSuccess(t *testing.T) {
	s := setupStore(t)
	h := &enqueueBatchHandler{queue: "test", store: s}

	payload := enqueueBatchRequest{
		Items: []store.BatchEnqueueItem{
			{Key: "b-1", Priority: 1},
			{Key: "b-2", Priority: 2},
			{Key: "b-3", Priority: 3},
		},
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/enqueue/batch", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var resp enqueueBatchResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Status != "enqueued" {
		t.Errorf("expected status 'enqueued', got %q", resp.Status)
	}
	if resp.Count != 3 {
		t.Errorf("expected count 3, got %d", resp.Count)
	}

	// Verify items exist in the store.
	for _, key := range []string{"b-1", "b-2", "b-3"} {
		if _, err := s.GetItem(context.Background(), "test", key); err != nil {
			t.Errorf("GetItem(%q): %v", key, err)
		}
	}
}

func TestEnqueueBatchEmpty(t *testing.T) {
	s := setupStore(t)
	h := &enqueueBatchHandler{queue: "test", store: s}

	body := `{"items":[]}`
	req := httptest.NewRequest(http.MethodPost, "/enqueue/batch", strings.NewReader(body))
	rr := httptest.NewRecorder()

	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "must not be empty") {
		t.Errorf("expected 'must not be empty' in body, got %q", rr.Body.String())
	}
}

func TestEnqueueBatchTooLarge(t *testing.T) {
	s := setupStore(t)
	h := &enqueueBatchHandler{queue: "test", store: s}

	items := make([]store.BatchEnqueueItem, maxBatchSize+1)
	for i := range items {
		items[i] = store.BatchEnqueueItem{Key: "k-" + strings.Repeat("0", 5)} // placeholder key
	}
	payload := enqueueBatchRequest{Items: items}
	body, _ := json.Marshal(payload)

	req := httptest.NewRequest(http.MethodPost, "/enqueue/batch", bytes.NewReader(body))
	rr := httptest.NewRecorder()

	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413, got %d: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "batch too large") {
		t.Errorf("expected 'batch too large' in body, got %q", rr.Body.String())
	}
}

func TestEnqueueBatchMissingKey(t *testing.T) {
	s := setupStore(t)
	h := &enqueueBatchHandler{queue: "test", store: s}

	payload := enqueueBatchRequest{
		Items: []store.BatchEnqueueItem{
			{Key: "valid", Priority: 1},
			{Key: "", Priority: 2}, // empty key at index 1
		},
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/enqueue/batch", bytes.NewReader(body))
	rr := httptest.NewRecorder()

	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "item[1]") {
		t.Errorf("expected error to reference 'item[1]', got %q", rr.Body.String())
	}
}

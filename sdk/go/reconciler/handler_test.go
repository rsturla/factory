package reconciler_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hummingbird-org/factory-workqueue/sdk/go/reconciler"
)

func TestReconcilerHandler_Completed(t *testing.T) {
	handler := reconciler.ReconcilerHandler(func(ctx context.Context, req reconciler.ProcessRequest) (reconciler.ProcessResponse, error) {
		if req.Key != "test-key" {
			t.Errorf("expected key test-key, got %s", req.Key)
		}
		if req.Attempt != 1 {
			t.Errorf("expected attempt 1, got %d", req.Attempt)
		}
		if req.Priority != 50 {
			t.Errorf("expected priority 50, got %d", req.Priority)
		}
		return reconciler.Completed(), nil
	})

	body := `{"key":"test-key","attempt":1,"priority":50}`
	req := httptest.NewRequest(http.MethodPost, "/process", strings.NewReader(body))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp reconciler.ProcessResponse
	json.NewDecoder(w.Body).Decode(&resp)
	if resp.Action != reconciler.ActionCompleted {
		t.Errorf("expected action completed, got %s", resp.Action)
	}
}

func TestReconcilerHandler_Converged(t *testing.T) {
	handler := reconciler.ReconcilerHandler(func(ctx context.Context, req reconciler.ProcessRequest) (reconciler.ProcessResponse, error) {
		return reconciler.Converged(), nil
	})

	req := httptest.NewRequest(http.MethodPost, "/process",
		strings.NewReader(`{"key":"k"}`))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	var resp reconciler.ProcessResponse
	json.NewDecoder(w.Body).Decode(&resp)
	if resp.Action != reconciler.ActionConverged {
		t.Errorf("expected converged, got %s", resp.Action)
	}
}

func TestReconcilerHandler_RequeueAfter(t *testing.T) {
	handler := reconciler.ReconcilerHandler(func(ctx context.Context, req reconciler.ProcessRequest) (reconciler.ProcessResponse, error) {
		return reconciler.RequeueAfter(5 * time.Minute), nil
	})

	req := httptest.NewRequest(http.MethodPost, "/process",
		strings.NewReader(`{"key":"k"}`))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	var resp reconciler.ProcessResponse
	json.NewDecoder(w.Body).Decode(&resp)
	if resp.Action != reconciler.ActionRequeue {
		t.Errorf("expected requeue, got %s", resp.Action)
	}
	if resp.RequeueAfter != "5m0s" {
		t.Errorf("expected 5m0s, got %s", resp.RequeueAfter)
	}
}

func TestReconcilerHandler_FanOut(t *testing.T) {
	handler := reconciler.ReconcilerHandler(func(ctx context.Context, req reconciler.ProcessRequest) (reconciler.ProcessResponse, error) {
		return reconciler.FanOut("child-1", "child-2", "child-3"), nil
	})

	req := httptest.NewRequest(http.MethodPost, "/process",
		strings.NewReader(`{"key":"parent"}`))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	var resp reconciler.ProcessResponse
	json.NewDecoder(w.Body).Decode(&resp)
	if resp.Action != reconciler.ActionFanOut {
		t.Errorf("expected fan_out, got %s", resp.Action)
	}
	if len(resp.FanOutKeys) != 3 {
		t.Fatalf("expected 3 fan-out keys, got %d", len(resp.FanOutKeys))
	}
	if resp.FanOutKeys[0] != "child-1" {
		t.Errorf("expected child-1, got %s", resp.FanOutKeys[0])
	}
}

func TestReconcilerHandler_Error(t *testing.T) {
	handler := reconciler.ReconcilerHandler(func(ctx context.Context, req reconciler.ProcessRequest) (reconciler.ProcessResponse, error) {
		return reconciler.ProcessResponse{}, fmt.Errorf("connection refused")
	})

	req := httptest.NewRequest(http.MethodPost, "/process",
		strings.NewReader(`{"key":"k"}`))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 (error encoded in body), got %d", w.Code)
	}

	var resp reconciler.ProcessResponse
	json.NewDecoder(w.Body).Decode(&resp)
	if resp.Error != "connection refused" {
		t.Errorf("expected error 'connection refused', got %q", resp.Error)
	}
}

func TestReconcilerHandler_BadRequest(t *testing.T) {
	handler := reconciler.ReconcilerHandler(func(ctx context.Context, req reconciler.ProcessRequest) (reconciler.ProcessResponse, error) {
		return reconciler.Completed(), nil
	})

	req := httptest.NewRequest(http.MethodPost, "/process",
		strings.NewReader(`{invalid json}`))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for bad JSON, got %d", w.Code)
	}
}

func TestReconcilerHandler_WrongMethod(t *testing.T) {
	handler := reconciler.ReconcilerHandler(func(ctx context.Context, req reconciler.ProcessRequest) (reconciler.ProcessResponse, error) {
		return reconciler.Completed(), nil
	})

	req := httptest.NewRequest(http.MethodGet, "/process", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

func TestResponseBuilders(t *testing.T) {
	t.Run("Completed", func(t *testing.T) {
		r := reconciler.Completed()
		if r.Action != "completed" {
			t.Errorf("got %s", r.Action)
		}
	})
	t.Run("Converged", func(t *testing.T) {
		r := reconciler.Converged()
		if r.Action != "converged" {
			t.Errorf("got %s", r.Action)
		}
	})
	t.Run("RequeueAfter", func(t *testing.T) {
		r := reconciler.RequeueAfter(30 * time.Second)
		if r.Action != "requeue" || r.RequeueAfter != "30s" {
			t.Errorf("got action=%s, delay=%s", r.Action, r.RequeueAfter)
		}
	})
	t.Run("FanOut", func(t *testing.T) {
		r := reconciler.FanOut("a", "b")
		if r.Action != "fan_out" || len(r.FanOutKeys) != 2 {
			t.Errorf("got action=%s, keys=%v", r.Action, r.FanOutKeys)
		}
	})
}

package authz_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hummingbird-org/factory-workqueue/internal/authz"
)

type staticAuthorizer struct {
	allowed bool
	reason  string
}

func (a staticAuthorizer) Authorize(_ context.Context, _ authz.Request) authz.Decision {
	return authz.Decision{Allowed: a.allowed, Reason: a.reason}
}

func TestMiddlewareAllowed(t *testing.T) {
	called := false
	handler := authz.Wrap(staticAuthorizer{allowed: true}, authz.ActionQueuesRead, "test-queue",
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			called = true
			w.WriteHeader(http.StatusOK)
		}))

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-Forwarded-User", "alice")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if !called {
		t.Error("handler should have been called")
	}
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
}

func TestMiddlewareDenied(t *testing.T) {
	handler := authz.Wrap(staticAuthorizer{allowed: false, reason: "not in group"}, authz.ActionItemsRetry, "q",
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			t.Fatal("handler should not be called when denied")
		}))

	req := httptest.NewRequest("POST", "/", nil)
	req.Header.Set("X-Forwarded-User", "bob")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d", rec.Code)
	}
}

func TestIdentityFromRequestForwardedHeaders(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-Forwarded-User", "alice")
	req.Header.Set("X-Forwarded-Groups", "sre-team, on-call")

	id := authz.IdentityFromRequest(req)
	if id.User != "alice" {
		t.Errorf("expected alice, got %q", id.User)
	}
	if len(id.Groups) != 2 || id.Groups[0] != "sre-team" || id.Groups[1] != "on-call" {
		t.Errorf("expected [sre-team on-call], got %v", id.Groups)
	}
}

func TestIdentityFromRequestFallbackHeaders(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-Remote-User", "bob")
	req.Header.Set("X-Remote-Groups", "dev")

	id := authz.IdentityFromRequest(req)
	if id.User != "bob" {
		t.Errorf("expected bob, got %q", id.User)
	}
	if len(id.Groups) != 1 || id.Groups[0] != "dev" {
		t.Errorf("expected [dev], got %v", id.Groups)
	}
}

func TestIdentityFromRequestPreferForwarded(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-Forwarded-User", "alice")
	req.Header.Set("X-Remote-User", "bob")

	id := authz.IdentityFromRequest(req)
	if id.User != "alice" {
		t.Errorf("expected alice (X-Forwarded-User), got %q", id.User)
	}
}

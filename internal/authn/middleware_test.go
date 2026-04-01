package authn_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hummingbird-org/factory-workqueue/internal/authn"
	"github.com/hummingbird-org/factory-workqueue/internal/authz"
)

type staticAuthenticator struct {
	identity authz.Identity
	err      error
}

func (a staticAuthenticator) Identify(_ *http.Request) (authz.Identity, error) {
	return a.identity, a.err
}

func TestMiddlewareInjectsHeaders(t *testing.T) {
	auth := staticAuthenticator{
		identity: authz.Identity{User: "alice", Groups: []string{"sre-team", "on-call"}},
	}

	var gotUser, gotGroups string
	handler := authn.Middleware(auth)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUser = r.Header.Get("X-Forwarded-User")
		gotGroups = r.Header.Get("X-Forwarded-Groups")
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/test", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if gotUser != "alice" {
		t.Errorf("expected user alice, got %q", gotUser)
	}
	if gotGroups != "sre-team,on-call" {
		t.Errorf("expected groups sre-team,on-call, got %q", gotGroups)
	}
}

func TestMiddlewareAuthError(t *testing.T) {
	auth := staticAuthenticator{err: fmt.Errorf("token expired")}

	handler := authn.Middleware(auth)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler should not be called on auth error")
	}))

	req := httptest.NewRequest("GET", "/test", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestMiddlewareEmptyIdentity(t *testing.T) {
	auth := staticAuthenticator{identity: authz.Identity{}}

	var gotUser string
	handler := authn.Middleware(auth)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUser = r.Header.Get("X-Forwarded-User")
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/test", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if gotUser != "" {
		t.Errorf("expected empty user, got %q", gotUser)
	}
}

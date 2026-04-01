package noop_test

import (
	"net/http/httptest"
	"testing"

	"github.com/hummingbird-org/factory-workqueue/internal/authn/noop"
)

func TestNoopFromHeaders(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-Forwarded-User", "alice")
	req.Header.Set("X-Forwarded-Groups", "sre-team,on-call")

	id, err := noop.Authenticator{}.Identify(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id.User != "alice" {
		t.Errorf("expected user alice, got %q", id.User)
	}
	if len(id.Groups) != 2 || id.Groups[0] != "sre-team" || id.Groups[1] != "on-call" {
		t.Errorf("expected [sre-team on-call], got %v", id.Groups)
	}
}

func TestNoopFallbackHeaders(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-Remote-User", "bob")
	req.Header.Set("X-Remote-Groups", "dev-team")

	id, err := noop.Authenticator{}.Identify(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id.User != "bob" {
		t.Errorf("expected user bob, got %q", id.User)
	}
	if len(id.Groups) != 1 || id.Groups[0] != "dev-team" {
		t.Errorf("expected [dev-team], got %v", id.Groups)
	}
}

func TestNoopNoHeaders(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)

	id, err := noop.Authenticator{}.Identify(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id.User != "" {
		t.Errorf("expected empty user, got %q", id.User)
	}
	if len(id.Groups) != 0 {
		t.Errorf("expected no groups, got %v", id.Groups)
	}
}

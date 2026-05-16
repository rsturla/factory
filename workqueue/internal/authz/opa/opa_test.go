package opa_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/hummingbird-org/factory-workqueue/internal/authz"
	"github.com/hummingbird-org/factory-workqueue/internal/authz/opa"
)

func mustNew(t *testing.T, cfg opa.Config) *opa.Authorizer {
	t.Helper()
	a, err := opa.New(cfg)
	if err != nil {
		t.Fatalf("opa.New: %v", err)
	}
	return a
}

func TestOPAAllowed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify the request format.
		var input struct {
			Input struct {
				User   string   `json:"user"`
				Groups []string `json:"groups"`
				Action string   `json:"action"`
				Queue  string   `json:"queue"`
			} `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Errorf("failed to decode OPA input: %v", err)
		}
		if input.Input.User != "alice" {
			t.Errorf("expected user alice, got %q", input.Input.User)
		}
		if input.Input.Action != "enqueue" {
			t.Errorf("expected action enqueue, got %q", input.Input.Action)
		}
		if input.Input.Queue != "rpm-update" {
			t.Errorf("expected queue rpm-update, got %q", input.Input.Queue)
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"result": map[string]any{"allow": true},
		})
	}))
	defer srv.Close()

	a := mustNew(t, opa.Config{Endpoint: srv.URL})
	decision := a.Authorize(context.Background(), authz.Request{
		User:   "alice",
		Groups: []string{"sre-team"},
		Action: authz.ActionEnqueue,
		Queue:  "rpm-update",
	})

	if !decision.Allowed {
		t.Fatalf("expected allowed, got denied: %s", decision.Reason)
	}
}

func TestOPADenied(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"result": map[string]any{"allow": false, "reason": "no matching policy"},
		})
	}))
	defer srv.Close()

	a := mustNew(t, opa.Config{Endpoint: srv.URL})
	decision := a.Authorize(context.Background(), authz.Request{
		User:   "bob",
		Groups: []string{"viewers"},
		Action: authz.ActionEnqueue,
		Queue:  "rpm-update",
	})

	if decision.Allowed {
		t.Fatal("expected denied")
	}
	if decision.Reason != "no matching policy" {
		t.Errorf("expected reason 'no matching policy', got %q", decision.Reason)
	}
}

func TestOPADeniedDefaultReason(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"result": map[string]any{"allow": false},
		})
	}))
	defer srv.Close()

	a := mustNew(t, opa.Config{Endpoint: srv.URL})
	decision := a.Authorize(context.Background(), authz.Request{
		User:   "bob",
		Groups: []string{"viewers"},
		Action: authz.ActionEnqueue,
		Queue:  "rpm-update",
	})

	if decision.Allowed {
		t.Fatal("expected denied")
	}
	if decision.Reason != "denied by policy" {
		t.Errorf("expected default reason 'denied by policy', got %q", decision.Reason)
	}
}

func TestOPAUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close() // close immediately to make it unreachable

	a := mustNew(t, opa.Config{Endpoint: srv.URL})
	decision := a.Authorize(context.Background(), authz.Request{
		User:   "alice",
		Groups: []string{"sre-team"},
		Action: authz.ActionEnqueue,
		Queue:  "rpm-update",
	})

	if decision.Allowed {
		t.Fatal("expected denied when OPA is unreachable")
	}
	if decision.Reason == "" {
		t.Error("expected a reason for denial")
	}
}

func TestOPANon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	a := mustNew(t, opa.Config{Endpoint: srv.URL})
	decision := a.Authorize(context.Background(), authz.Request{
		User:   "alice",
		Groups: []string{"sre-team"},
		Action: authz.ActionEnqueue,
		Queue:  "rpm-update",
	})

	if decision.Allowed {
		t.Fatal("expected denied on HTTP 500")
	}
	if decision.Reason != "OPA returned status 500" {
		t.Errorf("expected reason about status 500, got %q", decision.Reason)
	}
}

func TestOPAMalformedJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{not valid json`))
	}))
	defer srv.Close()

	a := mustNew(t, opa.Config{Endpoint: srv.URL})
	decision := a.Authorize(context.Background(), authz.Request{
		User:   "alice",
		Groups: []string{"sre-team"},
		Action: authz.ActionEnqueue,
		Queue:  "rpm-update",
	})

	if decision.Allowed {
		t.Fatal("expected denied on malformed JSON")
	}
	if decision.Reason != "failed to decode OPA response" {
		t.Errorf("expected decode error reason, got %q", decision.Reason)
	}
}

func TestOPAUnauthenticated(t *testing.T) {
	a := mustNew(t, opa.Config{Endpoint: "http://localhost:0"})
	decision := a.Authorize(context.Background(), authz.Request{
		User:   "",
		Groups: nil,
		Action: authz.ActionEnqueue,
		Queue:  "rpm-update",
	})

	if decision.Allowed {
		t.Fatal("expected denied for empty user")
	}
	if decision.Reason != "unauthenticated" {
		t.Errorf("expected reason 'unauthenticated', got %q", decision.Reason)
	}
}

func TestOPACustomPolicyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/data/custom/policy" {
			t.Errorf("expected path /v1/data/custom/policy, got %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"result": map[string]any{"allow": true},
		})
	}))
	defer srv.Close()

	a := mustNew(t, opa.Config{
		Endpoint:   srv.URL,
		PolicyPath: "v1/data/custom/policy",
	})
	decision := a.Authorize(context.Background(), authz.Request{
		User:   "alice",
		Action: authz.ActionEnqueue,
	})

	if !decision.Allowed {
		t.Fatalf("expected allowed, got denied: %s", decision.Reason)
	}
}

func TestOPACACertNotFound(t *testing.T) {
	_, err := opa.New(opa.Config{
		Endpoint:   "https://localhost:8181",
		CACertPath: "/nonexistent/ca.crt",
	})
	if err == nil {
		t.Fatal("expected error for nonexistent CA cert")
	}
}

func TestOPACACertInvalidPEM(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "bad-ca-*.pem")
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString("not a valid PEM")
	f.Close()

	_, err = opa.New(opa.Config{
		Endpoint:   "https://localhost:8181",
		CACertPath: f.Name(),
	})
	if err == nil {
		t.Fatal("expected error for invalid PEM")
	}
}

package openshift_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hummingbird-org/factory-workqueue/internal/authn/openshift"
)

func TestIdentifyValidToken(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/apis/user.openshift.io/v1/users/~" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Errorf("unexpected auth header: %s", r.Header.Get("Authorization"))
		}
		json.NewEncoder(w).Encode(map[string]any{
			"metadata": map[string]string{"name": "alice"},
			"groups":   []string{"sre-team", "on-call"},
		})
	}))
	defer srv.Close()

	a := openshift.NewWithClient(srv.URL, srv.Client())

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer test-token")

	id, err := a.Identify(req)
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

func TestIdentifyUnauthorized(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	a := openshift.NewWithClient(srv.URL, srv.Client())

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer bad-token")

	_, err := a.Identify(req)
	if err == nil {
		t.Fatal("expected error for unauthorized token")
	}
}

func TestIdentifyNoToken(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("should not call API without a token")
	}))
	defer srv.Close()

	a := openshift.NewWithClient(srv.URL, srv.Client())

	req := httptest.NewRequest("GET", "/", nil)

	id, err := a.Identify(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id.User != "" {
		t.Errorf("expected empty user, got %q", id.User)
	}
}

func TestIdentifyBearerCaseInsensitive(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"metadata": map[string]string{"name": "bob"},
			"groups":   []string{},
		})
	}))
	defer srv.Close()

	a := openshift.NewWithClient(srv.URL, srv.Client())

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "BEARER my-token")

	id, err := a.Identify(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id.User != "bob" {
		t.Errorf("expected user bob, got %q", id.User)
	}
}

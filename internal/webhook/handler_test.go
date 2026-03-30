package webhook_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hummingbird-org/factory/internal/store"
	"github.com/hummingbird-org/factory/internal/store/inmem"
	"github.com/hummingbird-org/factory/internal/webhook"
)

func newStore(t *testing.T) store.Interface {
	t.Helper()
	s := inmem.New()
	s.EnsureQueue(context.Background(), "test", store.QueueConfig{
		MaxConcurrency: 10, MaxRetry: 5, ComputeBackend: "kubernetes",
	})
	return s
}

func TestEnqueueHandler(t *testing.T) {
	s := newStore(t)
	handler := webhook.NewEnqueueHandler("test", s)

	req := httptest.NewRequest(http.MethodPost, "/enqueue",
		strings.NewReader(`{"key":"my-key","priority":42}`))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	counts, _ := s.CountByStatus(context.Background(), "test")
	if counts[store.StatusPending] != 1 {
		t.Fatalf("expected 1 pending, got %d", counts[store.StatusPending])
	}

	item, _ := s.GetItem(context.Background(), "test", "my-key")
	if item.Priority != 42 {
		t.Errorf("expected priority 42, got %d", item.Priority)
	}
}

func TestEnqueueHandler_MissingKey(t *testing.T) {
	s := newStore(t)
	handler := webhook.NewEnqueueHandler("test", s)

	req := httptest.NewRequest(http.MethodPost, "/enqueue",
		strings.NewReader(`{"priority":10}`))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for missing key, got %d", w.Code)
	}
}

func TestEnqueueHandler_WrongMethod(t *testing.T) {
	s := newStore(t)
	handler := webhook.NewEnqueueHandler("test", s)

	req := httptest.NewRequest(http.MethodGet, "/enqueue", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

func TestWebhookHandler_GitHub(t *testing.T) {
	s := newStore(t)
	handler := webhook.NewHandler("test", s, "", webhook.GitHubKeyExtractor)

	body := `{"action":"opened","number":123,"repository":{"full_name":"myorg/myrepo"}}`
	req := httptest.NewRequest(http.MethodPost, "/webhook",
		strings.NewReader(body))
	req.Header.Set("X-GitHub-Event", "pull_request")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	item, err := s.GetItem(context.Background(), "test", "myorg/myrepo#123")
	if err != nil {
		t.Fatalf("expected item enqueued, got err: %v", err)
	}
	if item.Key != "myorg/myrepo#123" {
		t.Errorf("unexpected key: %s", item.Key)
	}
}

func TestWebhookHandler_GitHubPush(t *testing.T) {
	s := newStore(t)
	handler := webhook.NewHandler("test", s, "", webhook.GitHubKeyExtractor)

	body := `{"ref":"refs/heads/main","repository":{"full_name":"myorg/myrepo"}}`
	req := httptest.NewRequest(http.MethodPost, "/webhook",
		strings.NewReader(body))
	req.Header.Set("X-GitHub-Event", "push")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	_, err := s.GetItem(context.Background(), "test", "myorg/myrepo@refs/heads/main")
	if err != nil {
		t.Fatalf("expected push item enqueued: %v", err)
	}
}

func TestWebhookHandler_GitLab(t *testing.T) {
	s := newStore(t)
	handler := webhook.NewHandler("test", s, "", webhook.GitLabKeyExtractor)

	body := `{"object_attributes":{"iid":42},"project":{"path_with_namespace":"mygroup/myproject"}}`
	req := httptest.NewRequest(http.MethodPost, "/webhook",
		strings.NewReader(body))
	req.Header.Set("X-Gitlab-Event", "Merge Request Hook")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	_, err := s.GetItem(context.Background(), "test", "mygroup/myproject!42")
	if err != nil {
		t.Fatalf("expected MR item enqueued: %v", err)
	}
}

func TestWebhookHandler_UnknownEvent_Skipped(t *testing.T) {
	s := newStore(t)
	handler := webhook.NewHandler("test", s, "", webhook.GitHubKeyExtractor)

	req := httptest.NewRequest(http.MethodPost, "/webhook",
		strings.NewReader(`{}`))
	req.Header.Set("X-GitHub-Event", "star")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 (skipped), got %d", w.Code)
	}

	counts, _ := s.CountByStatus(context.Background(), "test")
	if counts[store.StatusPending] != 0 {
		t.Errorf("expected 0 pending (unknown event skipped), got %d", counts[store.StatusPending])
	}
}

func TestWebhookHandler_SignatureVerification(t *testing.T) {
	secret := "test-secret-123"
	s := newStore(t)
	handler := webhook.NewHandler("test", s, secret, webhook.GitHubKeyExtractor)

	body := `{"action":"opened","number":1,"repository":{"full_name":"org/repo"}}`

	t.Run("valid signature", func(t *testing.T) {
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write([]byte(body))
		sig := "sha256=" + hex.EncodeToString(mac.Sum(nil))

		req := httptest.NewRequest(http.MethodPost, "/webhook",
			strings.NewReader(body))
		req.Header.Set("X-GitHub-Event", "pull_request")
		req.Header.Set("X-Hub-Signature-256", sig)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("expected 200 with valid sig, got %d", w.Code)
		}
	})

	t.Run("invalid signature", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/webhook",
			strings.NewReader(body))
		req.Header.Set("X-GitHub-Event", "pull_request")
		req.Header.Set("X-Hub-Signature-256", "sha256=deadbeef")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		if w.Code != http.StatusUnauthorized {
			t.Errorf("expected 401 with invalid sig, got %d", w.Code)
		}
	})

	t.Run("missing signature", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/webhook",
			strings.NewReader(body))
		req.Header.Set("X-GitHub-Event", "pull_request")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		if w.Code != http.StatusUnauthorized {
			t.Errorf("expected 401 with missing sig, got %d", w.Code)
		}
	})
}

package admin_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/hummingbird-org/factory-workqueue/internal/admin"
	"github.com/hummingbird-org/factory-workqueue/internal/authz/noop"
	"github.com/hummingbird-org/factory-workqueue/internal/store"
	"github.com/hummingbird-org/factory-workqueue/internal/store/inmem"
)

func newAdminMux(t *testing.T) *http.ServeMux {
	t.Helper()
	s := inmem.New()
	s.EnsureQueue(context.Background(), "test", store.QueueConfig{MaxConcurrency: 10, MaxRetry: 5})
	mux := http.NewServeMux()
	admin.NewHandler(s, noop.Authorizer{}).Register(mux)
	return mux
}

func FuzzAdminListItems(f *testing.F) {
	f.Add("test", "")
	f.Add("test", "pending")
	f.Add("test", "claimed")
	f.Add("test", "bogus")
	f.Add("", "")
	f.Add("../etc/passwd", "pending")
	f.Add("q%00q", "%00")
	f.Add("a%0Ab", "a%0Ab")

	f.Fuzz(func(t *testing.T, queue, status string) {
		mux := newAdminMux(t)
		path := "/admin/queues/" + url.PathEscape(queue) + "/items"
		if status != "" {
			path += "?status=" + url.QueryEscape(status)
		}
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code < 100 || rec.Code > 599 {
			t.Errorf("invalid status code: %d", rec.Code)
		}
	})
}

func FuzzAdminGetItem(f *testing.F) {
	f.Add("test", "key1")
	f.Add("", "")
	f.Add("../etc", "../../passwd")
	f.Add("q%00", "k%00")

	f.Fuzz(func(t *testing.T, queue, key string) {
		mux := newAdminMux(t)
		path := "/admin/queues/" + url.PathEscape(queue) + "/items/" + url.PathEscape(key)
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code < 100 || rec.Code > 599 {
			t.Errorf("invalid status code: %d", rec.Code)
		}
	})
}

func FuzzAdminRetryItem(f *testing.F) {
	f.Add("test", "key1")
	f.Add("", "")
	f.Add("%00", "%ff")

	f.Fuzz(func(t *testing.T, queue, key string) {
		mux := newAdminMux(t)
		path := "/admin/queues/" + url.PathEscape(queue) + "/items/" + url.PathEscape(key) + "/retry"
		req := httptest.NewRequest(http.MethodPost, path, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code < 100 || rec.Code > 599 {
			t.Errorf("invalid status code: %d", rec.Code)
		}
	})
}

func FuzzAdminPurgeDeadLetters(f *testing.F) {
	f.Add("test")
	f.Add("")
	f.Add("../etc/passwd")

	f.Fuzz(func(t *testing.T, queue string) {
		mux := newAdminMux(t)
		path := "/admin/queues/" + url.PathEscape(queue) + "/dead-letters"
		req := httptest.NewRequest(http.MethodDelete, path, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code < 100 || rec.Code > 599 {
			t.Errorf("invalid status code: %d", rec.Code)
		}
	})
}

func FuzzAdminPauseQueue(f *testing.F) {
	f.Add("test")
	f.Add("")
	f.Add("../queue")

	f.Fuzz(func(t *testing.T, queue string) {
		mux := newAdminMux(t)
		path := "/admin/queues/" + url.PathEscape(queue) + "/pause"
		req := httptest.NewRequest(http.MethodPost, path, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code < 100 || rec.Code > 599 {
			t.Errorf("invalid status code: %d", rec.Code)
		}
	})
}

func FuzzAdminResumeQueue(f *testing.F) {
	f.Add("test")
	f.Add("")
	f.Add("../queue")

	f.Fuzz(func(t *testing.T, queue string) {
		mux := newAdminMux(t)
		path := "/admin/queues/" + url.PathEscape(queue) + "/resume"
		req := httptest.NewRequest(http.MethodPost, path, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code < 100 || rec.Code > 599 {
			t.Errorf("invalid status code: %d", rec.Code)
		}
	})
}

func FuzzAdminStreamEvents(f *testing.F) {
	f.Add("test")
	f.Add("")
	f.Add("../queue")

	f.Fuzz(func(t *testing.T, queue string) {
		mux := newAdminMux(t)
		path := "/admin/queues/" + url.PathEscape(queue) + "/events"
		// SSE endpoint blocks forever reading from a channel, so use a
		// short context deadline to ensure the handler returns promptly.
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		req := httptest.NewRequest(http.MethodGet, path, nil).WithContext(ctx)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		// SSE sets 200 via headers before blocking, so any status is fine.
		if rec.Code < 100 || rec.Code > 599 {
			t.Errorf("invalid status code: %d", rec.Code)
		}
	})
}

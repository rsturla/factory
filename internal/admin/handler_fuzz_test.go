package admin_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/hummingbird-org/factory-workqueue/internal/admin"
	"github.com/hummingbird-org/factory-workqueue/internal/authz/noop"
	"github.com/hummingbird-org/factory-workqueue/internal/store"
	"github.com/hummingbird-org/factory-workqueue/internal/store/inmem"
)

func newAdminMux(t *testing.T) *http.ServeMux {
	t.Helper()
	s := inmem.New()
	s.EnsureQueue(nil, "test", store.QueueConfig{MaxConcurrency: 10, MaxRetry: 5})
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

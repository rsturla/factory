package reconciler_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hummingbird-org/factory-workqueue/sdk/go/reconciler"
)

func FuzzReconcilerHandler(f *testing.F) {
	f.Add([]byte(`{"key":"test-key","attempt":1,"priority":50}`))
	f.Add([]byte(`{"key":"k"}`))
	f.Add([]byte(`{}`))
	f.Add([]byte(`{"key":null,"attempt":-1,"priority":-999}`))
	f.Add([]byte(`{"key":"k","attempt":999999999,"trace_id":"00-abc-def-01"}`))
	f.Add([]byte(`null`))
	f.Add([]byte(``))
	f.Add([]byte(`{invalid`))
	f.Add([]byte(`{"key":123}`))

	handler := reconciler.ReconcilerHandler(func(ctx context.Context, req reconciler.ProcessRequest) (reconciler.ProcessResponse, error) {
		return reconciler.Completed(), nil
	})

	f.Fuzz(func(t *testing.T, data []byte) {
		req := httptest.NewRequest(http.MethodPost, "/process", bytes.NewReader(data))
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code < 100 || rec.Code > 599 {
			t.Errorf("invalid status code: %d", rec.Code)
		}
	})
}

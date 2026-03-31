package client_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hummingbird-org/factory-workqueue/pkg/client"
	"github.com/hummingbird-org/factory-workqueue/pkg/sdk"
)

func FuzzReconcilerClientProcess(f *testing.F) {
	// Fuzz the JSON response body from a mock reconciler server.
	f.Add([]byte(`{"action":"completed"}`))
	f.Add([]byte(`{"action":"requeue","requeue_after":"5m"}`))
	f.Add([]byte(`{"action":"fan_out","fan_out_keys":["a","b"]}`))
	f.Add([]byte(`{"action":"completed","error":"oops"}`))
	f.Add([]byte(`{"action":"unknown_action"}`))
	f.Add([]byte(`{"action":""}`))
	f.Add([]byte(`{"action":"requeue","requeue_after":"invalid"}`))
	f.Add([]byte(`{"action":"requeue","requeue_after":"-5m"}`))
	f.Add([]byte(`{"action":"fan_out","fan_out_keys":null}`))
	f.Add([]byte(`{}`))
	f.Add([]byte(`null`))
	f.Add([]byte(``))
	f.Add([]byte(`{invalid`))
	f.Add([]byte(`{"action":"requeue","requeue_after":"999999999999h"}`))

	f.Fuzz(func(t *testing.T, data []byte) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write(data)
		}))
		defer srv.Close()

		c := client.NewReconcilerClient(srv.URL)
		// Must not panic. Errors are acceptable.
		resp, err := c.Process(context.Background(), sdk.ProcessRequest{Key: "k"})
		if err != nil {
			return
		}
		// If parsing succeeded, action must be a string (possibly empty).
		_ = resp.Action
	})
}

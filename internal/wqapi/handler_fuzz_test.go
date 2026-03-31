package wqapi_test

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hummingbird-org/factory-workqueue/internal/authz/noop"
	"github.com/hummingbird-org/factory-workqueue/internal/store"
	"github.com/hummingbird-org/factory-workqueue/internal/store/inmem"
	"github.com/hummingbird-org/factory-workqueue/internal/wqapi"
)

func newFuzzMux(t *testing.T) (*http.ServeMux, store.Interface) {
	t.Helper()
	s := inmem.New()
	s.EnsureQueue(nil, "q", store.QueueConfig{MaxConcurrency: 10, MaxRetry: 5})
	mux := http.NewServeMux()
	wqapi.NewHandler(s, noop.Authorizer{}).Register(mux)
	return mux, s
}

func fuzzEndpoint(f *testing.F, path string, seeds ...string) {
	f.Helper()
	for _, s := range seeds {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		mux, _ := newFuzzMux(t)
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(data))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code < 100 || rec.Code > 599 {
			t.Errorf("invalid status code: %d", rec.Code)
		}
	})
}

func FuzzWqapiEnqueue(f *testing.F) {
	fuzzEndpoint(f, "/wq/enqueue",
		`{"queue":"q","key":"k","priority":1}`,
		`{"queue":"q","key":"k","priority":-1}`,
		`{"queue":"","key":"","priority":0}`,
		`{}`,
		`{"queue":"q","key":"k","priority":99999999999}`,
		`null`,
		``,
		`{invalid`,
	)
}

func FuzzWqapiClaim(f *testing.F) {
	fuzzEndpoint(f, "/wq/claim",
		`{"queue":"q","batch_size":1,"worker_id":"w","lease_duration":"1m"}`,
		`{"queue":"q","batch_size":0,"worker_id":"","lease_duration":""}`,
		`{"queue":"q","batch_size":-1,"worker_id":"w","lease_duration":"invalid"}`,
		`{"queue":"q","batch_size":1000000,"worker_id":"w","lease_duration":"999999h"}`,
		`{}`,
		``,
	)
}

func FuzzWqapiComplete(f *testing.F) {
	fuzzEndpoint(f, "/wq/complete",
		`{"queue":"q","key":"k"}`,
		`{"queue":"","key":""}`,
		`{}`,
		``,
	)
}

func FuzzWqapiFail(f *testing.F) {
	fuzzEndpoint(f, "/wq/fail",
		`{"queue":"q","key":"k","error":"boom"}`,
		`{"queue":"q","key":"k","error":""}`,
		`{}`,
		``,
	)
}

func FuzzWqapiHeartbeat(f *testing.F) {
	fuzzEndpoint(f, "/wq/heartbeat",
		`{"queue":"q","key":"k","duration":"5m"}`,
		`{"queue":"q","key":"k","duration":"invalid"}`,
		`{"queue":"q","key":"k","duration":""}`,
		`{"queue":"q","key":"k","duration":"-1s"}`,
		`{}`,
		``,
	)
}

func FuzzWqapiTransition(f *testing.F) {
	fuzzEndpoint(f, "/wq/transition",
		`{"queue":"q","key":"k","from":"pending","to":"active"}`,
		`{"queue":"q","key":"k","from":"","to":""}`,
		`{"queue":"q","key":"k","from":"bogus","to":"bogus"}`,
		`{}`,
		``,
	)
}

func FuzzWqapiRequeueUndo(f *testing.F) {
	fuzzEndpoint(f, "/wq/requeue-undo",
		`{"queue":"q","key":"k","not_before":"2025-01-01T00:00:00Z"}`,
		`{"queue":"q","key":"k","not_before":"invalid"}`,
		`{"queue":"q","key":"k","not_before":""}`,
		`{}`,
		``,
	)
}

func FuzzWqapiEnsureQueue(f *testing.F) {
	fuzzEndpoint(f, "/wq/ensure-queue",
		`{"queue":"q","config":{"max_concurrency":10,"max_retry":5,"compute_backend":"kubernetes"}}`,
		`{"queue":"q","config":{"max_concurrency":-1,"max_retry":-1}}`,
		`{"queue":"","config":{}}`,
		`{}`,
		``,
	)
}

func FuzzWqapiList(f *testing.F) {
	fuzzEndpoint(f, "/wq/list",
		`{"queue":"q","limit":10,"offset":0}`,
		`{"queue":"q","status":"pending","limit":100}`,
		`{"queue":"q","status":"bogus","limit":-1,"offset":-1}`,
		`{}`,
		``,
	)
}

func FuzzWqapiRecordHistory(f *testing.F) {
	fuzzEndpoint(f, "/wq/record-history",
		`{"queue":"q","key":"k","from_status":"pending","to_status":"active"}`,
		`{"queue":"","key":"","from_status":"","to_status":""}`,
		`{}`,
		``,
	)
}

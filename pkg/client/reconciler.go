// Package client provides HTTP clients for communication between factory services.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/hummingbird-org/factory-workqueue/pkg/sdk"
)

// ReconcilerClient calls a reconciler service's /process endpoint.
// Used by the dispatcher to invoke reconcilers over HTTP.
type ReconcilerClient struct {
	endpoint   string
	httpClient *http.Client
}

// NewReconcilerClient creates a client targeting the given reconciler endpoint.
func NewReconcilerClient(endpoint string) *ReconcilerClient {
	return &ReconcilerClient{
		endpoint: endpoint,
		httpClient: &http.Client{
			Timeout: 0, // no timeout — reconcilers can be long-running; use context cancellation
		},
	}
}

// Process sends a work item key to the reconciler and returns its response.
func (c *ReconcilerClient) Process(ctx context.Context, req sdk.ProcessRequest) (sdk.ProcessResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return sdk.ProcessResponse{}, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint+"/process", bytes.NewReader(body))
	if err != nil {
		return sdk.ProcessResponse{}, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return sdk.ProcessResponse{}, fmt.Errorf("call reconciler: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return sdk.ProcessResponse{}, fmt.Errorf("reconciler returned status %d", resp.StatusCode)
	}

	var result sdk.ProcessResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return sdk.ProcessResponse{}, fmt.Errorf("decode response: %w", err)
	}

	return result, nil
}

// WithTimeout returns a copy of the client with a per-request timeout.
func (c *ReconcilerClient) WithTimeout(d time.Duration) *ReconcilerClient {
	return &ReconcilerClient{
		endpoint: c.endpoint,
		httpClient: &http.Client{
			Timeout:   d,
			Transport: c.httpClient.Transport,
		},
	}
}

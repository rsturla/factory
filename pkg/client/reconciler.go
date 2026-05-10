// Package client provides HTTP clients for communication between factory services.
package client

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/hummingbird-org/factory-workqueue/pkg/sdk"
)

// Option configures a ReconcilerClient.
type Option func(*ReconcilerClient) error

// ReconcilerClient calls a reconciler service's /process endpoint.
// Used by the dispatcher to invoke reconcilers over HTTP.
type ReconcilerClient struct {
	endpoint   string
	httpClient *http.Client
}

// DefaultReconcilerTimeout is the maximum time a single reconciler call may
// take before the HTTP client cancels the request.  This is a safety net for
// hung reconcilers — callers should also use context deadlines for tighter
// control.  Use WithTimeout to override.
const DefaultReconcilerTimeout = 30 * time.Minute

// NewReconcilerClient creates a client targeting the given reconciler endpoint.
// Options may be passed to configure TLS or other settings. If any option
// returns an error, NewReconcilerClient panics. Use NewReconcilerClientE for
// an error-returning variant.
func NewReconcilerClient(endpoint string, opts ...Option) *ReconcilerClient {
	c, err := NewReconcilerClientE(endpoint, opts...)
	if err != nil {
		panic("client: " + err.Error())
	}
	return c
}

// NewReconcilerClientE is like NewReconcilerClient but returns an error instead
// of panicking.
func NewReconcilerClientE(endpoint string, opts ...Option) (*ReconcilerClient, error) {
	c := &ReconcilerClient{
		endpoint: endpoint,
		httpClient: &http.Client{
			Timeout: DefaultReconcilerTimeout,
			Transport: &http.Transport{
				MaxIdleConnsPerHost: 16,
			},
		},
	}
	for _, opt := range opts {
		if err := opt(c); err != nil {
			return nil, err
		}
	}
	return c, nil
}

// WithCACert configures a custom CA certificate for verifying the reconciler's
// TLS certificate. If path is empty, the option is a no-op.
func WithCACert(path string) Option {
	return func(c *ReconcilerClient) error {
		if path == "" {
			return nil
		}
		caCert, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read reconciler CA cert: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(caCert) {
			return fmt.Errorf("reconciler CA cert contains no valid PEM certificates: %s", path)
		}
		transport := c.httpClient.Transport.(*http.Transport).Clone()
		transport.TLSClientConfig = &tls.Config{RootCAs: pool}
		c.httpClient.Transport = transport
		return nil
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

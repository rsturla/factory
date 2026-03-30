package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/hummingbird-org/factory/internal/workqueue"
)

// WorkqueueClient implements workqueue.Interface over HTTP.
// Used by external workers (EC2) that cannot connect directly to PostgreSQL.
type WorkqueueClient struct {
	endpoint   string
	httpClient *http.Client
}

// NewWorkqueueClient creates a client targeting the factory API endpoint.
func NewWorkqueueClient(endpoint string) *WorkqueueClient {
	return &WorkqueueClient{
		endpoint: endpoint,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

func (c *WorkqueueClient) Enqueue(ctx context.Context, queue, key string, priority int, opts ...workqueue.EnqueueOption) error {
	return c.post(ctx, "/wq/enqueue", map[string]any{
		"queue":    queue,
		"key":      key,
		"priority": priority,
	})
}

func (c *WorkqueueClient) ClaimBatch(ctx context.Context, queue string, batchSize int, workerID string, leaseDuration time.Duration) ([]workqueue.WorkItem, error) {
	body, err := c.postJSON(ctx, "/wq/claim", map[string]any{
		"queue":          queue,
		"batch_size":     batchSize,
		"worker_id":      workerID,
		"lease_duration": leaseDuration.String(),
	})
	if err != nil {
		return nil, err
	}
	var items []workqueue.WorkItem
	if err := json.Unmarshal(body, &items); err != nil {
		return nil, fmt.Errorf("decode claim response: %w", err)
	}
	return items, nil
}

func (c *WorkqueueClient) Complete(ctx context.Context, queue, key string) error {
	return c.post(ctx, "/wq/complete", map[string]any{"queue": queue, "key": key})
}

func (c *WorkqueueClient) Fail(ctx context.Context, queue, key string, errMsg string) error {
	return c.post(ctx, "/wq/fail", map[string]any{"queue": queue, "key": key, "error": errMsg})
}

func (c *WorkqueueClient) Requeue(ctx context.Context, queue, key string, opts ...workqueue.RequeueOption) error {
	return c.post(ctx, "/wq/requeue", map[string]any{"queue": queue, "key": key})
}

func (c *WorkqueueClient) RequeueUndoAttempt(ctx context.Context, queue, key string, notBefore time.Time) error {
	return c.post(ctx, "/wq/requeue-undo", map[string]any{
		"queue":      queue,
		"key":        key,
		"not_before": notBefore.Format(time.RFC3339),
	})
}

func (c *WorkqueueClient) Deadletter(ctx context.Context, queue, key string) error {
	return c.post(ctx, "/wq/deadletter", map[string]any{"queue": queue, "key": key})
}

func (c *WorkqueueClient) ExtendLease(ctx context.Context, queue, key string, duration time.Duration) error {
	return c.post(ctx, "/wq/heartbeat", map[string]any{
		"queue":    queue,
		"key":      key,
		"duration": duration.String(),
	})
}

func (c *WorkqueueClient) Transition(ctx context.Context, queue, key string, from, to workqueue.Status, opts ...workqueue.TransitionOption) error {
	return c.post(ctx, "/wq/transition", map[string]any{
		"queue": queue,
		"key":   key,
		"from":  from,
		"to":    to,
	})
}

func (c *WorkqueueClient) CountByStatus(ctx context.Context, queue string) (map[workqueue.Status]int64, error) {
	body, err := c.postJSON(ctx, "/wq/count", map[string]any{"queue": queue})
	if err != nil {
		return nil, err
	}
	var counts map[workqueue.Status]int64
	if err := json.Unmarshal(body, &counts); err != nil {
		return nil, fmt.Errorf("decode count response: %w", err)
	}
	return counts, nil
}

func (c *WorkqueueClient) List(ctx context.Context, filter workqueue.ListFilter) ([]workqueue.WorkItem, error) {
	body, err := c.postJSON(ctx, "/wq/list", filter)
	if err != nil {
		return nil, err
	}
	var items []workqueue.WorkItem
	if err := json.Unmarshal(body, &items); err != nil {
		return nil, fmt.Errorf("decode list response: %w", err)
	}
	return items, nil
}

func (c *WorkqueueClient) RepairCounter(ctx context.Context, queue string) error {
	return c.post(ctx, "/wq/repair", map[string]any{"queue": queue})
}

func (c *WorkqueueClient) EnsureQueue(ctx context.Context, queue string, cfg workqueue.QueueConfig) error {
	return c.post(ctx, "/wq/ensure-queue", map[string]any{
		"queue":  queue,
		"config": cfg,
	})
}

// post sends a JSON POST and checks for a 2xx response.
func (c *WorkqueueClient) post(ctx context.Context, path string, payload any) error {
	_, err := c.postJSON(ctx, path, payload)
	return err
}

// postJSON sends a JSON POST and returns the response body.
func (c *WorkqueueClient) postJSON(ctx context.Context, path string, payload any) ([]byte, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint+path, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	respBody := make([]byte, 0, 4096)
	buf := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(buf)
		respBody = append(respBody, buf[:n]...)
		if err != nil {
			break
		}
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("http %d: %s", resp.StatusCode, string(respBody))
	}

	return respBody, nil
}

// Verify interface compliance.
var _ workqueue.Interface = (*WorkqueueClient)(nil)

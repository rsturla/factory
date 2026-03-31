package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/hummingbird-org/factory-workqueue/internal/store"
)

// WorkqueueClient implements store.Interface over HTTP.
// Used by external workers (EC2) that cannot connect directly to the database.
type WorkqueueClient struct {
	endpoint   string
	httpClient *http.Client
}

// NewWorkqueueClient creates a client targeting the factory API endpoint.
func NewWorkqueueClient(endpoint string) *WorkqueueClient {
	return &WorkqueueClient{
		endpoint:   endpoint,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

func (c *WorkqueueClient) Enqueue(ctx context.Context, queue, key string, priority int, opts ...store.EnqueueOption) error {
	return c.post(ctx, "/wq/enqueue", map[string]any{"queue": queue, "key": key, "priority": priority})
}

func (c *WorkqueueClient) ClaimBatch(ctx context.Context, queue string, batchSize int, workerID string, leaseDuration time.Duration) ([]store.WorkItem, error) {
	body, err := c.postJSON(ctx, "/wq/claim", map[string]any{
		"queue": queue, "batch_size": batchSize, "worker_id": workerID, "lease_duration": leaseDuration.String(),
	})
	if err != nil {
		return nil, err
	}
	var items []store.WorkItem
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

func (c *WorkqueueClient) Requeue(ctx context.Context, queue, key string, opts ...store.RequeueOption) error {
	return c.post(ctx, "/wq/requeue", map[string]any{"queue": queue, "key": key})
}

func (c *WorkqueueClient) RequeueUndoAttempt(ctx context.Context, queue, key string, notBefore time.Time) error {
	return c.post(ctx, "/wq/requeue-undo", map[string]any{
		"queue": queue, "key": key, "not_before": notBefore.Format(time.RFC3339),
	})
}

func (c *WorkqueueClient) Deadletter(ctx context.Context, queue, key string) error {
	return c.post(ctx, "/wq/deadletter", map[string]any{"queue": queue, "key": key})
}

func (c *WorkqueueClient) ExtendLease(ctx context.Context, queue, key string, duration time.Duration) error {
	return c.post(ctx, "/wq/heartbeat", map[string]any{
		"queue": queue, "key": key, "duration": duration.String(),
	})
}

func (c *WorkqueueClient) Transition(ctx context.Context, queue, key string, from, to store.Status, opts ...store.TransitionOption) error {
	return c.post(ctx, "/wq/transition", map[string]any{
		"queue": queue, "key": key, "from": from, "to": to,
	})
}

func (c *WorkqueueClient) EnsureQueue(ctx context.Context, queue string, cfg store.QueueConfig) error {
	return c.post(ctx, "/wq/ensure-queue", map[string]any{"queue": queue, "config": cfg})
}

func (c *WorkqueueClient) RepairCounter(ctx context.Context, queue string) error {
	return c.post(ctx, "/wq/repair", map[string]any{"queue": queue})
}

func (c *WorkqueueClient) CountByStatus(ctx context.Context, queue string) (map[store.Status]int64, error) {
	body, err := c.postJSON(ctx, "/wq/count", map[string]any{"queue": queue})
	if err != nil {
		return nil, err
	}
	var counts map[store.Status]int64
	if err := json.Unmarshal(body, &counts); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	return counts, nil
}

func (c *WorkqueueClient) List(ctx context.Context, filter store.ListFilter) ([]store.WorkItem, error) {
	body, err := c.postJSON(ctx, "/wq/list", filter)
	if err != nil {
		return nil, err
	}
	var items []store.WorkItem
	if err := json.Unmarshal(body, &items); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	return items, nil
}

func (c *WorkqueueClient) GetItem(ctx context.Context, queue, key string) (*store.WorkItem, error) {
	body, err := c.postJSON(ctx, "/wq/get-item", map[string]any{"queue": queue, "key": key})
	if err != nil {
		return nil, err
	}
	var item store.WorkItem
	if err := json.Unmarshal(body, &item); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	return &item, nil
}

func (c *WorkqueueClient) ListQueues(ctx context.Context) ([]store.QueueInfo, error) {
	body, err := c.postJSON(ctx, "/wq/list-queues", nil)
	if err != nil {
		return nil, err
	}
	var queues []store.QueueInfo
	if err := json.Unmarshal(body, &queues); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	return queues, nil
}

func (c *WorkqueueClient) SetQueuePaused(ctx context.Context, queue string, paused bool) error {
	return c.post(ctx, "/wq/set-paused", map[string]any{"queue": queue, "paused": paused})
}

func (c *WorkqueueClient) IsQueuePaused(ctx context.Context, queue string) (bool, error) {
	body, err := c.postJSON(ctx, "/wq/is-paused", map[string]any{"queue": queue})
	if err != nil {
		return false, err
	}
	var result struct {
		Paused bool `json:"paused"`
	}
	json.Unmarshal(body, &result)
	return result.Paused, nil
}

func (c *WorkqueueClient) ListWorkers(ctx context.Context, queue string) ([]store.WorkerLease, error) {
	body, err := c.postJSON(ctx, "/wq/list-workers", map[string]any{"queue": queue})
	if err != nil {
		return nil, err
	}
	var workers []store.WorkerLease
	if err := json.Unmarshal(body, &workers); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	return workers, nil
}

func (c *WorkqueueClient) PurgeDeadLetters(ctx context.Context, queue string) (int64, error) {
	body, err := c.postJSON(ctx, "/wq/purge-dead-letters", map[string]any{"queue": queue})
	if err != nil {
		return 0, err
	}
	var result struct {
		Count int64 `json:"count"`
	}
	json.Unmarshal(body, &result)
	return result.Count, nil
}

func (c *WorkqueueClient) RecordHistory(ctx context.Context, entry store.HistoryEntry) error {
	return c.post(ctx, "/wq/record-history", entry)
}

func (c *WorkqueueClient) GetItemHistory(ctx context.Context, queue, key string) ([]store.HistoryEntry, error) {
	body, err := c.postJSON(ctx, "/wq/get-history", map[string]any{"queue": queue, "key": key})
	if err != nil {
		return nil, err
	}
	var entries []store.HistoryEntry
	if err := json.Unmarshal(body, &entries); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	return entries, nil
}

func (c *WorkqueueClient) Subscribe(ctx context.Context, queue string) (<-chan store.Event, error) {
	return nil, fmt.Errorf("subscribe not supported over HTTP client")
}

func (c *WorkqueueClient) post(ctx context.Context, path string, payload any) error {
	_, err := c.postJSON(ctx, path, payload)
	return err
}

func (c *WorkqueueClient) postJSON(ctx context.Context, path string, payload any) ([]byte, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal: %w", err)
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
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("http %d: %s", resp.StatusCode, respBody)
	}
	return respBody, nil
}

var _ store.Interface = (*WorkqueueClient)(nil)

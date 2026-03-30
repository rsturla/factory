package sdk

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// EnqueueClient is an HTTP client for enqueuing work into factory queues.
// Reconcilers use this for cross-queue triggers and fan-out.
type EnqueueClient struct {
	endpoint   string
	httpClient *http.Client
}

// EnqueueRequest is the payload sent to the receiver's /enqueue endpoint.
type EnqueueRequest struct {
	Queue    string `json:"queue"`
	Key      string `json:"key"`
	Priority int    `json:"priority"`
}

// NewEnqueueClient creates a client that connects to a factory receiver endpoint.
func NewEnqueueClient(endpoint string) *EnqueueClient {
	return &EnqueueClient{
		endpoint: endpoint,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// Enqueue submits a key to the given queue with the specified priority.
func (c *EnqueueClient) Enqueue(ctx context.Context, queue, key string, priority int) error {
	body, err := json.Marshal(EnqueueRequest{
		Queue:    queue,
		Key:      key,
		Priority: priority,
	})
	if err != nil {
		return fmt.Errorf("marshal enqueue request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint+"/enqueue", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("enqueue request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("enqueue failed: status %d", resp.StatusCode)
	}

	return nil
}

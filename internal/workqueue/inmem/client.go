// Package inmem implements workqueue.Interface with an in-memory store for testing.
package inmem

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/hummingbird-org/factory/internal/workqueue"
)

// Client implements workqueue.Interface using in-memory data structures.
// It is safe for concurrent use. Not suitable for production — use postgres.
type Client struct {
	mu     sync.Mutex
	items  map[itemKey]*workqueue.WorkItem
	queues map[string]*queueMeta
}

type itemKey struct {
	queue, key string
}

type queueMeta struct {
	config      workqueue.QueueConfig
	inProgress  int
}

// New creates a new in-memory workqueue client.
func New() *Client {
	return &Client{
		items:  make(map[itemKey]*workqueue.WorkItem),
		queues: make(map[string]*queueMeta),
	}
}

func (c *Client) EnsureQueue(_ context.Context, queue string, cfg workqueue.QueueConfig) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.queues[queue]; !ok {
		c.queues[queue] = &queueMeta{config: cfg}
	}
	return nil
}

func (c *Client) Enqueue(_ context.Context, queue, key string, priority int, opts ...workqueue.EnqueueOption) error {
	o := workqueue.ApplyEnqueueOptions(opts)
	c.mu.Lock()
	defer c.mu.Unlock()

	ik := itemKey{queue, key}
	if existing, ok := c.items[ik]; ok {
		if existing.Status == workqueue.StatusPending {
			if priority > existing.Priority {
				existing.Priority = priority
			}
			existing.UpdatedAt = time.Now()
		}
		return nil
	}

	now := time.Now()
	item := &workqueue.WorkItem{
		Queue:       queue,
		Key:         key,
		Status:      workqueue.StatusPending,
		Priority:    priority,
		MaxAttempts: 5,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if o.NotBefore != nil {
		item.NotBefore = o.NotBefore
	}

	// Default max_attempts from queue config if available.
	if q, ok := c.queues[queue]; ok {
		if q.config.MaxRetry > 0 {
			item.MaxAttempts = q.config.MaxRetry
		}
	}

	c.items[ik] = item
	return nil
}

func (c *Client) ClaimBatch(_ context.Context, queue string, batchSize int, workerID string, leaseDuration time.Duration) ([]workqueue.WorkItem, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	q, ok := c.queues[queue]
	if !ok {
		return nil, fmt.Errorf("queue %q not found", queue)
	}

	remaining := q.config.MaxConcurrency - q.inProgress
	if remaining <= 0 {
		return nil, nil
	}
	limit := min(batchSize, remaining)

	// Collect eligible items.
	now := time.Now()
	var eligible []*workqueue.WorkItem
	for ik, item := range c.items {
		if ik.queue != queue || item.Status != workqueue.StatusPending {
			continue
		}
		if item.NotBefore != nil && item.NotBefore.After(now) {
			continue
		}
		eligible = append(eligible, item)
	}

	// Sort by priority DESC, then created_at ASC.
	sort.Slice(eligible, func(i, j int) bool {
		if eligible[i].Priority != eligible[j].Priority {
			return eligible[i].Priority > eligible[j].Priority
		}
		return eligible[i].CreatedAt.Before(eligible[j].CreatedAt)
	})

	if limit > len(eligible) {
		limit = len(eligible)
	}

	var claimed []workqueue.WorkItem
	for _, item := range eligible[:limit] {
		item.Status = workqueue.StatusClaimed
		item.WorkerID = workerID
		item.Attempts++
		leaseExp := now.Add(leaseDuration)
		item.LeaseExpires = &leaseExp
		item.ClaimedAt = &now
		item.UpdatedAt = now
		claimed = append(claimed, *item)
	}

	q.inProgress += len(claimed)
	return claimed, nil
}

func (c *Client) Complete(_ context.Context, queue, key string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.completeItem(queue, key, workqueue.StatusSucceeded, "")
}

func (c *Client) Fail(_ context.Context, queue, key string, errMsg string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.completeItem(queue, key, workqueue.StatusFailed, errMsg)
}

func (c *Client) completeItem(queue, key string, status workqueue.Status, errMsg string) error {
	ik := itemKey{queue, key}
	item, ok := c.items[ik]
	if !ok {
		return workqueue.ErrNotFound
	}
	if item.Status != workqueue.StatusClaimed && item.Status != workqueue.StatusRunning {
		return workqueue.ErrNotFound
	}

	item.Status = status
	item.ErrorMessage = errMsg
	now := time.Now()
	item.CompletedAt = &now
	item.UpdatedAt = now
	item.LeaseExpires = nil

	if q, ok := c.queues[queue]; ok {
		q.inProgress = max(q.inProgress-1, 0)
	}
	return nil
}

func (c *Client) Requeue(_ context.Context, queue, key string, opts ...workqueue.RequeueOption) error {
	o := workqueue.ApplyRequeueOptions(opts)
	c.mu.Lock()
	defer c.mu.Unlock()

	ik := itemKey{queue, key}
	item, ok := c.items[ik]
	if !ok {
		return workqueue.ErrNotFound
	}
	if item.Status != workqueue.StatusClaimed && item.Status != workqueue.StatusRunning && item.Status != workqueue.StatusFailed {
		return workqueue.ErrNotFound
	}

	item.Status = workqueue.StatusPending
	item.NotBefore = o.NotBefore
	item.WorkerID = ""
	item.LeaseExpires = nil
	item.ErrorMessage = ""
	item.ClaimedAt = nil
	item.CompletedAt = nil
	item.UpdatedAt = time.Now()

	if q, ok := c.queues[queue]; ok {
		q.inProgress = max(q.inProgress-1, 0)
	}
	return nil
}

func (c *Client) RequeueUndoAttempt(_ context.Context, queue, key string, notBefore time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	ik := itemKey{queue, key}
	item, ok := c.items[ik]
	if !ok {
		return workqueue.ErrNotFound
	}
	if item.Status != workqueue.StatusClaimed && item.Status != workqueue.StatusRunning {
		return workqueue.ErrNotFound
	}

	item.Status = workqueue.StatusPending
	item.Attempts = max(item.Attempts-1, 0)
	item.NotBefore = &notBefore
	item.WorkerID = ""
	item.LeaseExpires = nil
	item.ErrorMessage = ""
	item.ClaimedAt = nil
	item.CompletedAt = nil
	item.UpdatedAt = time.Now()

	if q, ok := c.queues[queue]; ok {
		q.inProgress = max(q.inProgress-1, 0)
	}
	return nil
}

func (c *Client) Deadletter(_ context.Context, queue, key string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	ik := itemKey{queue, key}
	item, ok := c.items[ik]
	if !ok {
		return workqueue.ErrNotFound
	}
	if item.Status != workqueue.StatusClaimed && item.Status != workqueue.StatusRunning && item.Status != workqueue.StatusFailed {
		return workqueue.ErrNotFound
	}

	item.Status = workqueue.StatusDeadLetter
	now := time.Now()
	item.CompletedAt = &now
	item.UpdatedAt = now
	item.LeaseExpires = nil

	if q, ok := c.queues[queue]; ok {
		q.inProgress = max(q.inProgress-1, 0)
	}
	return nil
}

func (c *Client) ExtendLease(_ context.Context, queue, key string, duration time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	ik := itemKey{queue, key}
	item, ok := c.items[ik]
	if !ok {
		return workqueue.ErrNotFound
	}
	if item.Status != workqueue.StatusClaimed && item.Status != workqueue.StatusRunning {
		return workqueue.ErrNotFound
	}

	now := time.Now()
	exp := now.Add(duration)
	item.LeaseExpires = &exp
	item.UpdatedAt = now
	return nil
}

func (c *Client) Transition(_ context.Context, queue, key string, from, to workqueue.Status, opts ...workqueue.TransitionOption) error {
	o := workqueue.ApplyTransitionOptions(opts)
	c.mu.Lock()
	defer c.mu.Unlock()

	ik := itemKey{queue, key}
	item, ok := c.items[ik]
	if !ok {
		return workqueue.ErrNotFound
	}
	if item.Status != from {
		return workqueue.ErrConflict
	}

	item.Status = to
	if o.WorkerID != "" {
		item.WorkerID = o.WorkerID
	}
	if o.ErrorMessage != "" {
		item.ErrorMessage = o.ErrorMessage
	}
	item.UpdatedAt = time.Now()
	return nil
}

func (c *Client) CountByStatus(_ context.Context, queue string) (map[workqueue.Status]int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	counts := make(map[workqueue.Status]int64)
	for ik, item := range c.items {
		if ik.queue == queue {
			counts[item.Status]++
		}
	}
	return counts, nil
}

func (c *Client) List(_ context.Context, filter workqueue.ListFilter) ([]workqueue.WorkItem, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	var items []workqueue.WorkItem
	for ik, item := range c.items {
		if ik.queue != filter.Queue {
			continue
		}
		if filter.Status != nil && item.Status != *filter.Status {
			continue
		}
		items = append(items, *item)
	}

	sort.Slice(items, func(i, j int) bool {
		if items[i].Priority != items[j].Priority {
			return items[i].Priority > items[j].Priority
		}
		return items[i].CreatedAt.Before(items[j].CreatedAt)
	})

	limit := filter.Limit
	if limit <= 0 {
		limit = 100
	}
	start := filter.Offset
	if start > len(items) {
		return nil, nil
	}
	end := start + limit
	if end > len(items) {
		end = len(items)
	}
	return items[start:end], nil
}

func (c *Client) RepairCounter(_ context.Context, queue string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	q, ok := c.queues[queue]
	if !ok {
		return nil
	}

	count := 0
	for ik, item := range c.items {
		if ik.queue == queue && (item.Status == workqueue.StatusClaimed || item.Status == workqueue.StatusRunning) {
			count++
		}
	}
	q.inProgress = count
	return nil
}

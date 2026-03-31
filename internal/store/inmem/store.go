// Package inmem implements store.Interface with in-memory data structures for testing.
package inmem

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/hummingbird-org/factory-workqueue/internal/store"
)

// Store implements store.Interface using in-memory data structures.
type Store struct {
	mu       sync.Mutex
	items    map[itemKey]*store.WorkItem
	queues   map[string]*queueMeta
	history  []store.HistoryEntry
	historyID int64
	subs     map[string][]chan store.Event
}

type itemKey struct {
	queue, key string
}

type queueMeta struct {
	config     store.QueueConfig
	inProgress int
}

// New creates a new in-memory store.
func New() *Store {
	return &Store{
		items:  make(map[itemKey]*store.WorkItem),
		queues: make(map[string]*queueMeta),
		subs:   make(map[string][]chan store.Event),
	}
}

func (s *Store) EnsureQueue(_ context.Context, queue string, cfg store.QueueConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.queues[queue]; !ok {
		s.queues[queue] = &queueMeta{config: cfg}
	}
	return nil
}

func (s *Store) Enqueue(_ context.Context, queue, key string, priority int, opts ...store.EnqueueOption) error {
	o := store.ApplyEnqueueOptions(opts)
	s.mu.Lock()
	defer s.mu.Unlock()

	ik := itemKey{queue, key}
	if existing, ok := s.items[ik]; ok {
		switch existing.Status {
		case store.StatusPending:
			// Merge priority upward.
			if priority > existing.Priority {
				existing.Priority = priority
			}
			existing.UpdatedAt = time.Now()
		case store.StatusClaimed, store.StatusRunning:
			// In-flight — don't touch.
		default:
			// Succeeded, failed, dead_letter — reset to pending.
			existing.Status = store.StatusPending
			existing.Priority = priority
			existing.Attempts = 0
			existing.NotBefore = o.NotBefore
			existing.WorkerID = ""
			existing.LeaseExpires = nil
			existing.ErrorMessage = ""
			existing.ClaimedAt = nil
			existing.CompletedAt = nil
			existing.UpdatedAt = time.Now()
		}
		return nil
	}

	now := time.Now()
	item := &store.WorkItem{
		Queue:       queue,
		Key:         key,
		Status:      store.StatusPending,
		Priority:    priority,
		MaxAttempts: 5,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if o.NotBefore != nil {
		item.NotBefore = o.NotBefore
	}
	if q, ok := s.queues[queue]; ok && q.config.MaxRetry > 0 {
		item.MaxAttempts = q.config.MaxRetry
	}

	s.items[ik] = item
	s.emit(store.Event{Queue: queue, Key: key, Status: "pending", Priority: priority})
	return nil
}

func (s *Store) ClaimBatch(_ context.Context, queue string, batchSize int, workerID string, leaseDuration time.Duration) ([]store.WorkItem, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	q, ok := s.queues[queue]
	if !ok {
		return nil, fmt.Errorf("queue %q not found", queue)
	}

	remaining := q.config.MaxConcurrency - q.inProgress
	if remaining <= 0 {
		return nil, nil
	}
	limit := min(batchSize, remaining)

	now := time.Now()
	var eligible []*store.WorkItem
	for ik, item := range s.items {
		if ik.queue != queue || item.Status != store.StatusPending {
			continue
		}
		if item.NotBefore != nil && item.NotBefore.After(now) {
			continue
		}
		eligible = append(eligible, item)
	}

	sort.Slice(eligible, func(i, j int) bool {
		if eligible[i].Priority != eligible[j].Priority {
			return eligible[i].Priority > eligible[j].Priority
		}
		if !eligible[i].CreatedAt.Equal(eligible[j].CreatedAt) {
			return eligible[i].CreatedAt.Before(eligible[j].CreatedAt)
		}
		return eligible[i].Key < eligible[j].Key
	})

	if limit > len(eligible) {
		limit = len(eligible)
	}

	var claimed []store.WorkItem
	for _, item := range eligible[:limit] {
		item.Status = store.StatusClaimed
		item.WorkerID = workerID
		item.Attempts++
		leaseExp := now.Add(leaseDuration)
		item.LeaseExpires = &leaseExp
		item.ClaimedAt = &now
		item.UpdatedAt = now
		claimed = append(claimed, *item)

		s.addHistory(store.HistoryEntry{
			Queue: queue, Key: item.Key, FromStatus: "pending", ToStatus: "claimed",
			WorkerID: workerID, Attempt: item.Attempts, CreatedAt: now,
		})
	}

	q.inProgress += len(claimed)
	return claimed, nil
}

func (s *Store) Complete(_ context.Context, queue, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.completeItem(queue, key, store.StatusSucceeded, "")
}

func (s *Store) Fail(_ context.Context, queue, key string, errMsg string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.completeItem(queue, key, store.StatusFailed, errMsg)
}

func (s *Store) completeItem(queue, key string, status store.Status, errMsg string) error {
	ik := itemKey{queue, key}
	item, ok := s.items[ik]
	if !ok || (item.Status != store.StatusClaimed && item.Status != store.StatusRunning) {
		return store.ErrNotFound
	}

	item.Status = status
	item.ErrorMessage = errMsg
	now := time.Now()
	item.CompletedAt = &now
	item.UpdatedAt = now
	item.LeaseExpires = nil

	if q, ok := s.queues[queue]; ok {
		q.inProgress = max(q.inProgress-1, 0)
	}

	s.addHistory(store.HistoryEntry{
		Queue: queue, Key: key, FromStatus: "running", ToStatus: string(status), CreatedAt: now,
	})
	s.emit(store.Event{Queue: queue, Key: key, Status: string(status), Priority: item.Priority})
	return nil
}

func (s *Store) Requeue(_ context.Context, queue, key string, opts ...store.RequeueOption) error {
	o := store.ApplyRequeueOptions(opts)
	s.mu.Lock()
	defer s.mu.Unlock()

	ik := itemKey{queue, key}
	item, ok := s.items[ik]
	if !ok || (item.Status != store.StatusClaimed && item.Status != store.StatusRunning && item.Status != store.StatusFailed) {
		return store.ErrNotFound
	}

	item.Status = store.StatusPending
	item.NotBefore = o.NotBefore
	item.WorkerID = ""
	item.LeaseExpires = nil
	item.ErrorMessage = ""
	item.ClaimedAt = nil
	item.CompletedAt = nil
	item.UpdatedAt = time.Now()

	if q, ok := s.queues[queue]; ok {
		q.inProgress = max(q.inProgress-1, 0)
	}

	s.addHistory(store.HistoryEntry{
		Queue: queue, Key: key, FromStatus: "running", ToStatus: "pending", CreatedAt: time.Now(),
	})
	return nil
}

func (s *Store) RequeueUndoAttempt(_ context.Context, queue, key string, notBefore time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	ik := itemKey{queue, key}
	item, ok := s.items[ik]
	if !ok || (item.Status != store.StatusClaimed && item.Status != store.StatusRunning) {
		return store.ErrNotFound
	}

	item.Status = store.StatusPending
	item.Attempts = max(item.Attempts-1, 0)
	item.NotBefore = &notBefore
	item.WorkerID = ""
	item.LeaseExpires = nil
	item.ErrorMessage = ""
	item.ClaimedAt = nil
	item.CompletedAt = nil
	item.UpdatedAt = time.Now()

	if q, ok := s.queues[queue]; ok {
		q.inProgress = max(q.inProgress-1, 0)
	}

	s.addHistory(store.HistoryEntry{
		Queue: queue, Key: key, FromStatus: "claimed", ToStatus: "pending", CreatedAt: time.Now(),
	})
	return nil
}

func (s *Store) Deadletter(_ context.Context, queue, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	ik := itemKey{queue, key}
	item, ok := s.items[ik]
	if !ok || (item.Status != store.StatusClaimed && item.Status != store.StatusRunning && item.Status != store.StatusFailed) {
		return store.ErrNotFound
	}

	item.Status = store.StatusDeadLetter
	now := time.Now()
	item.CompletedAt = &now
	item.UpdatedAt = now
	item.LeaseExpires = nil

	if q, ok := s.queues[queue]; ok {
		q.inProgress = max(q.inProgress-1, 0)
	}

	s.addHistory(store.HistoryEntry{
		Queue: queue, Key: key, FromStatus: "failed", ToStatus: "dead_letter", CreatedAt: now,
	})
	s.emit(store.Event{Queue: queue, Key: key, Status: "dead_letter", Priority: item.Priority})
	return nil
}

func (s *Store) ExtendLease(_ context.Context, queue, key string, duration time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	ik := itemKey{queue, key}
	item, ok := s.items[ik]
	if !ok || (item.Status != store.StatusClaimed && item.Status != store.StatusRunning) {
		return store.ErrNotFound
	}

	now := time.Now()
	exp := now.Add(duration)
	item.LeaseExpires = &exp
	item.UpdatedAt = now
	return nil
}

func (s *Store) Transition(_ context.Context, queue, key string, from, to store.Status, opts ...store.TransitionOption) error {
	o := store.ApplyTransitionOptions(opts)
	s.mu.Lock()
	defer s.mu.Unlock()

	ik := itemKey{queue, key}
	item, ok := s.items[ik]
	if !ok {
		return store.ErrNotFound
	}
	if item.Status != from {
		return store.ErrConflict
	}

	item.Status = to
	if o.WorkerID != "" {
		item.WorkerID = o.WorkerID
	}
	if o.ErrorMessage != "" {
		item.ErrorMessage = o.ErrorMessage
	}
	item.UpdatedAt = time.Now()

	s.addHistory(store.HistoryEntry{
		Queue: queue, Key: key, FromStatus: string(from), ToStatus: string(to),
		WorkerID: o.WorkerID, CreatedAt: time.Now(),
	})
	s.emit(store.Event{Queue: queue, Key: key, Status: string(to), Priority: item.Priority})
	return nil
}

// --- Queue Management ---

func (s *Store) RepairCounter(_ context.Context, queue string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	q, ok := s.queues[queue]
	if !ok {
		return nil
	}
	count := 0
	for ik, item := range s.items {
		if ik.queue == queue && (item.Status == store.StatusClaimed || item.Status == store.StatusRunning) {
			count++
		}
	}
	q.inProgress = count
	return nil
}

// --- Query Operations ---

func (s *Store) CountByStatus(_ context.Context, queue string) (map[store.Status]int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	counts := make(map[store.Status]int64)
	for ik, item := range s.items {
		if ik.queue == queue {
			counts[item.Status]++
		}
	}
	return counts, nil
}

func (s *Store) List(_ context.Context, filter store.ListFilter) ([]store.WorkItem, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var items []store.WorkItem
	for ik, item := range s.items {
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
		if !items[i].CreatedAt.Equal(items[j].CreatedAt) {
			return items[i].CreatedAt.Before(items[j].CreatedAt)
		}
		return items[i].Key < items[j].Key
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

func (s *Store) GetItem(_ context.Context, queue, key string) (*store.WorkItem, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	ik := itemKey{queue, key}
	item, ok := s.items[ik]
	if !ok {
		return nil, store.ErrNotFound
	}
	cp := *item
	return &cp, nil
}

// --- Admin Queries ---

func (s *Store) ListQueues(_ context.Context) ([]store.QueueInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var queues []store.QueueInfo
	for name, q := range s.queues {
		qi := store.QueueInfo{
			Name:           name,
			MaxConcurrency: q.config.MaxConcurrency,
			MaxRetry:       q.config.MaxRetry,
			ComputeBackend: q.config.ComputeBackend,
			InProgress:     q.inProgress,
			Counts:         make(map[string]int),
		}
		for ik, item := range s.items {
			if ik.queue == name {
				qi.Counts[string(item.Status)]++
			}
		}
		queues = append(queues, qi)
	}

	sort.Slice(queues, func(i, j int) bool {
		return queues[i].Name < queues[j].Name
	})
	return queues, nil
}

func (s *Store) ListWorkers(_ context.Context, queue string) ([]store.WorkerLease, error) {
	// In-memory store doesn't track workers — return empty.
	return nil, nil
}

func (s *Store) PurgeDeadLetters(_ context.Context, queue string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var count int64
	for ik, item := range s.items {
		if ik.queue == queue && item.Status == store.StatusDeadLetter {
			delete(s.items, ik)
			count++
		}
	}
	return count, nil
}

// --- History ---

func (s *Store) RecordHistory(_ context.Context, entry store.HistoryEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.addHistory(entry)
	return nil
}

func (s *Store) GetItemHistory(_ context.Context, queue, key string) ([]store.HistoryEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var entries []store.HistoryEntry
	for _, e := range s.history {
		if e.Queue == queue && e.Key == key {
			entries = append(entries, e)
		}
	}
	// Reverse for descending order.
	for i, j := 0, len(entries)-1; i < j; i, j = i+1, j-1 {
		entries[i], entries[j] = entries[j], entries[i]
	}
	return entries, nil
}

func (s *Store) addHistory(entry store.HistoryEntry) {
	s.historyID++
	entry.ID = s.historyID
	if entry.CreatedAt.IsZero() {
		entry.CreatedAt = time.Now()
	}
	s.history = append(s.history, entry)
}

// --- Events ---

func (s *Store) Subscribe(ctx context.Context, queue string) (<-chan store.Event, error) {
	s.mu.Lock()
	ch := make(chan store.Event, 64)
	s.subs[queue] = append(s.subs[queue], ch)
	s.mu.Unlock()

	go func() {
		<-ctx.Done()
		s.mu.Lock()
		defer s.mu.Unlock()
		subs := s.subs[queue]
		for i, sub := range subs {
			if sub == ch {
				s.subs[queue] = append(subs[:i], subs[i+1:]...)
				break
			}
		}
		close(ch)
	}()

	return ch, nil
}

// emit sends an event to all subscribers for a queue. Must be called with mu held.
func (s *Store) emit(event store.Event) {
	for _, ch := range s.subs[event.Queue] {
		select {
		case ch <- event:
		default:
		}
	}
}

// Verify interface compliance.
var _ store.Interface = (*Store)(nil)

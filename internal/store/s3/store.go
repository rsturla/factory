// Package s3 implements store.Interface backed by Amazon S3.
//
// State machine via key prefixes:
//
//	{queue}/queued/{key}        → pending
//	{queue}/in-progress/{key}  → claimed/running
//	{queue}/dead-letter/{key}  → dead-lettered
//	{queue}/history/{key}/{id} → history entries
//	_queues/{queue}            → queue config (JSON body)
//
// Work item metadata (priority, attempts, timestamps) is stored as S3
// object user metadata. Object bodies are empty (pure workqueue).
//
// Claiming uses optimistic concurrency: CopyObject with If-Match (ETag)
// to move from queued/ to in-progress/. If another dispatcher claims
// the same item, the ETag won't match and the copy fails.
package s3

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/hummingbird-org/factory/internal/store"
)

// Config holds configuration for the S3 store.
type Config struct {
	Bucket   string
	Region   string
	Endpoint string // for S3-compatible stores (MinIO, etc.)
}

// Store implements store.Interface using Amazon S3.
type Store struct {
	client *s3.Client
	bucket string

	// In-memory queue config cache (loaded from _queues/ prefix).
	mu      sync.RWMutex
	configs map[string]store.QueueConfig
}

// New creates a new S3 store.
func New(ctx context.Context, cfg Config) (*Store, error) {
	var opts []func(*awsconfig.LoadOptions) error
	if cfg.Region != "" {
		opts = append(opts, awsconfig.WithRegion(cfg.Region))
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("load aws config: %w", err)
	}

	var s3Opts []func(*s3.Options)
	if cfg.Endpoint != "" {
		s3Opts = append(s3Opts, func(o *s3.Options) {
			o.BaseEndpoint = aws.String(cfg.Endpoint)
			o.UsePathStyle = true
		})
	}

	client := s3.NewFromConfig(awsCfg, s3Opts...)
	return NewWithClient(client, cfg.Bucket), nil
}

// NewWithClient creates a store with an injected S3 client (for testing).
func NewWithClient(client *s3.Client, bucket string) *Store {
	return &Store{
		client:  client,
		bucket:  bucket,
		configs: make(map[string]store.QueueConfig),
	}
}

// --- Key helpers ---

func queuedPrefix(queue string) string      { return queue + "/queued/" }
func inProgressPrefix(queue string) string   { return queue + "/in-progress/" }
func succeededPrefix(queue string) string    { return queue + "/succeeded/" }
func failedPrefix(queue string) string       { return queue + "/failed/" }
func deadLetterPrefix(queue string) string   { return queue + "/dead-letter/" }
func historyPrefix(queue, key string) string { return queue + "/history/" + key + "/" }

func queuedKey(queue, key string) string      { return queuedPrefix(queue) + key }
func inProgressKey(queue, key string) string   { return inProgressPrefix(queue) + key }
func succeededKey(queue, key string) string    { return succeededPrefix(queue) + key }
func failedKey(queue, key string) string       { return failedPrefix(queue) + key }
func deadLetterKey(queue, key string) string   { return deadLetterPrefix(queue) + key }
func queueConfigKey(queue string) string       { return "_queues/" + queue }

// --- Metadata helpers ---

type itemMeta struct {
	Status       string // tracks sub-status within a prefix (e.g., "claimed" vs "running")
	Priority     int
	Attempts     int
	MaxAttempts  int
	NotBefore    *time.Time
	LeaseExpires *time.Time
	WorkerID     string
	ErrorMessage string
	CreatedAt    time.Time
	ClaimedAt    *time.Time
}

func encodeMeta(m itemMeta) map[string]string {
	md := map[string]string{
		"priority":     fmt.Sprintf("%08d", m.Priority+50000000), // offset for sorting
		"priority-raw": strconv.Itoa(m.Priority),
		"attempts":     strconv.Itoa(m.Attempts),
		"max-attempts": strconv.Itoa(m.MaxAttempts),
		"created-at":   m.CreatedAt.Format(time.RFC3339Nano),
	}
	if m.Status != "" {
		md["status"] = m.Status
	}
	if m.NotBefore != nil {
		md["not-before"] = m.NotBefore.Format(time.RFC3339Nano)
	}
	if m.LeaseExpires != nil {
		md["lease-expires"] = m.LeaseExpires.Format(time.RFC3339Nano)
	}
	if m.WorkerID != "" {
		md["worker-id"] = m.WorkerID
	}
	if m.ErrorMessage != "" {
		md["error-message"] = m.ErrorMessage
	}
	if m.ClaimedAt != nil {
		md["claimed-at"] = m.ClaimedAt.Format(time.RFC3339Nano)
	}
	return md
}

func decodeMeta(md map[string]string) itemMeta {
	m := itemMeta{}
	m.Status = metaStr(md, "status")
	if v, ok := md["priority-raw"]; ok {
		m.Priority, _ = strconv.Atoi(v)
	} else if v, ok := md["Priority-Raw"]; ok {
		m.Priority, _ = strconv.Atoi(v)
	}
	m.Attempts = metaInt(md, "attempts")
	m.MaxAttempts = metaInt(md, "max-attempts")
	m.CreatedAt = metaTime(md, "created-at")
	m.NotBefore = metaTimePtr(md, "not-before")
	m.LeaseExpires = metaTimePtr(md, "lease-expires")
	m.WorkerID = metaStr(md, "worker-id")
	m.ErrorMessage = metaStr(md, "error-message")
	m.ClaimedAt = metaTimePtr(md, "claimed-at")
	return m
}

func metaInt(md map[string]string, key string) int {
	v, _ := strconv.Atoi(metaStr(md, key))
	return v
}

func metaStr(md map[string]string, key string) string {
	if v, ok := md[key]; ok {
		return v
	}
	// S3 may capitalize first letter of each word in metadata keys.
	parts := strings.Split(key, "-")
	for i := range parts {
		if len(parts[i]) > 0 {
			parts[i] = strings.ToUpper(parts[i][:1]) + parts[i][1:]
		}
	}
	return md[strings.Join(parts, "-")]
}

func metaTime(md map[string]string, key string) time.Time {
	s := metaStr(md, key)
	if s == "" {
		return time.Time{}
	}
	t, _ := time.Parse(time.RFC3339Nano, s)
	return t
}

func metaTimePtr(md map[string]string, key string) *time.Time {
	t := metaTime(md, key)
	if t.IsZero() {
		return nil
	}
	return &t
}

// --- store.Interface implementation ---

func (s *Store) Enqueue(ctx context.Context, queue, key string, priority int, opts ...store.EnqueueOption) error {
	o := store.ApplyEnqueueOptions(opts)

	objKey := queuedKey(queue, key)

	// Check if already exists — merge priority upward.
	head, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: &s.bucket,
		Key:    &objKey,
	})
	if err == nil {
		// Exists — merge priority.
		existing := decodeMeta(head.Metadata)
		if priority > existing.Priority {
			existing.Priority = priority
		}
		_, err = s.client.PutObject(ctx, &s3.PutObjectInput{
			Bucket:   &s.bucket,
			Key:      &objKey,
			Metadata: encodeMeta(existing),
		})
		return err
	}

	// Doesn't exist — create.
	now := time.Now()
	cfg := s.getConfig(queue)
	meta := itemMeta{
		Priority:    priority,
		MaxAttempts: cfg.MaxRetry,
		CreatedAt:   now,
	}
	if o.NotBefore != nil {
		meta.NotBefore = o.NotBefore
	}
	if meta.MaxAttempts == 0 {
		meta.MaxAttempts = 5
	}

	_, err = s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:   &s.bucket,
		Key:      &objKey,
		Metadata: encodeMeta(meta),
	})
	return err
}

func (s *Store) ClaimBatch(ctx context.Context, queue string, batchSize int, workerID string, leaseDuration time.Duration) ([]store.WorkItem, error) {
	cfg := s.getConfig(queue)

	// Count in-progress to enforce concurrency.
	inProgressCount, err := s.countPrefix(ctx, inProgressPrefix(queue))
	if err != nil {
		return nil, fmt.Errorf("count in-progress: %w", err)
	}
	remaining := cfg.MaxConcurrency - inProgressCount
	if remaining <= 0 {
		return nil, nil
	}
	limit := min(batchSize, remaining)

	// List queued items.
	prefix := queuedPrefix(queue)
	listed, err := s.listWithMeta(ctx, prefix)
	if err != nil {
		return nil, fmt.Errorf("list queued: %w", err)
	}

	// Filter out not-before items and sort by priority DESC, created_at ASC.
	now := time.Now()
	var eligible []listedItem
	for _, item := range listed {
		if item.meta.NotBefore != nil && item.meta.NotBefore.After(now) {
			continue
		}
		eligible = append(eligible, item)
	}

	sort.Slice(eligible, func(i, j int) bool {
		if eligible[i].meta.Priority != eligible[j].meta.Priority {
			return eligible[i].meta.Priority > eligible[j].meta.Priority
		}
		return eligible[i].meta.CreatedAt.Before(eligible[j].meta.CreatedAt)
	})

	if limit > len(eligible) {
		limit = len(eligible)
	}

	var claimed []store.WorkItem
	for _, item := range eligible[:limit] {
		wi, err := s.claimOne(ctx, queue, item, workerID, leaseDuration)
		if err != nil {
			// Another dispatcher got it — skip.
			continue
		}
		claimed = append(claimed, *wi)
		if len(claimed) >= limit {
			break
		}
	}

	return claimed, nil
}

func (s *Store) claimOne(ctx context.Context, queue string, item listedItem, workerID string, leaseDuration time.Duration) (*store.WorkItem, error) {
	now := time.Now()
	leaseExp := now.Add(leaseDuration)

	meta := item.meta
	meta.Status = "claimed"
	meta.Attempts++
	meta.WorkerID = workerID
	meta.LeaseExpires = &leaseExp
	meta.ClaimedAt = &now

	srcKey := queuedKey(queue, item.key)
	dstKey := inProgressKey(queue, item.key)

	// Write to in-progress.
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:   &s.bucket,
		Key:      &dstKey,
		Metadata: encodeMeta(meta),
	})
	if err != nil {
		return nil, fmt.Errorf("put in-progress: %w", err)
	}

	// Delete from queued. If this fails, the reaper will clean up.
	s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: &s.bucket,
		Key:    &srcKey,
	})

	// Record history.
	s.writeHistory(ctx, queue, item.key, store.HistoryEntry{
		Queue: queue, Key: item.key, FromStatus: "pending", ToStatus: "claimed",
		WorkerID: workerID, Attempt: meta.Attempts, CreatedAt: now,
	})

	wi := metaToWorkItem(queue, item.key, store.StatusClaimed, meta)
	return &wi, nil
}

func (s *Store) Complete(ctx context.Context, queue, key string) error {
	return s.moveToTerminal(ctx, queue, key, "succeeded", succeededKey(queue, key), "")
}

func (s *Store) Fail(ctx context.Context, queue, key string, errMsg string) error {
	return s.moveToTerminal(ctx, queue, key, "failed", failedKey(queue, key), errMsg)
}

func (s *Store) moveToTerminal(ctx context.Context, queue, key, status, dstKey, errMsg string) error {
	objKey := inProgressKey(queue, key)
	head, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: &s.bucket, Key: &objKey})
	if err != nil {
		return store.ErrNotFound
	}
	meta := decodeMeta(head.Metadata)
	meta.Status = status
	meta.ErrorMessage = errMsg
	meta.LeaseExpires = nil

	_, err = s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:   &s.bucket,
		Key:      &dstKey,
		Metadata: encodeMeta(meta),
	})
	if err != nil {
		return fmt.Errorf("put %s: %w", status, err)
	}

	s.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: &s.bucket, Key: &objKey})

	s.writeHistory(ctx, queue, key, store.HistoryEntry{
		Queue: queue, Key: key, FromStatus: "running", ToStatus: status,
		ErrorMessage: errMsg, CreatedAt: time.Now(),
	})
	return nil
}

func (s *Store) Requeue(ctx context.Context, queue, key string, opts ...store.RequeueOption) error {
	o := store.ApplyRequeueOptions(opts)

	// Find the item — could be in in-progress or failed.
	srcKey, meta, err := s.findItem(ctx, queue, key,
		inProgressKey(queue, key),
		failedKey(queue, key),
	)
	if err != nil {
		return store.ErrNotFound
	}

	meta.Status = "pending"
	meta.WorkerID = ""
	meta.LeaseExpires = nil
	meta.ErrorMessage = ""
	meta.ClaimedAt = nil
	meta.NotBefore = o.NotBefore

	dstKey := queuedKey(queue, key)
	_, err = s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:   &s.bucket,
		Key:      &dstKey,
		Metadata: encodeMeta(meta),
	})
	if err != nil {
		return fmt.Errorf("put queued: %w", err)
	}

	s.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: &s.bucket, Key: &srcKey})

	s.writeHistory(ctx, queue, key, store.HistoryEntry{
		Queue: queue, Key: key, FromStatus: "running", ToStatus: "pending", CreatedAt: time.Now(),
	})
	return nil
}

func (s *Store) RequeueUndoAttempt(ctx context.Context, queue, key string, notBefore time.Time) error {
	objKey := inProgressKey(queue, key)
	head, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: &s.bucket, Key: &objKey})
	if err != nil {
		return store.ErrNotFound
	}

	meta := decodeMeta(head.Metadata)
	meta.Attempts = max(meta.Attempts-1, 0)
	meta.WorkerID = ""
	meta.LeaseExpires = nil
	meta.ErrorMessage = ""
	meta.ClaimedAt = nil
	meta.NotBefore = &notBefore

	dstKey := queuedKey(queue, key)
	_, err = s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:   &s.bucket,
		Key:      &dstKey,
		Metadata: encodeMeta(meta),
	})
	if err != nil {
		return fmt.Errorf("put queued: %w", err)
	}

	s.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: &s.bucket, Key: &objKey})

	s.writeHistory(ctx, queue, key, store.HistoryEntry{
		Queue: queue, Key: key, FromStatus: "claimed", ToStatus: "pending", CreatedAt: time.Now(),
	})
	return nil
}

func (s *Store) Deadletter(ctx context.Context, queue, key string) error {
	srcKey, meta, err := s.findItem(ctx, queue, key,
		inProgressKey(queue, key),
		failedKey(queue, key),
	)
	if err != nil {
		return store.ErrNotFound
	}

	meta.Status = "dead_letter"
	meta.LeaseExpires = nil

	dstKey := deadLetterKey(queue, key)
	_, err = s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:   &s.bucket,
		Key:      &dstKey,
		Metadata: encodeMeta(meta),
	})
	if err != nil {
		return fmt.Errorf("put dead-letter: %w", err)
	}

	s.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: &s.bucket, Key: &srcKey})

	s.writeHistory(ctx, queue, key, store.HistoryEntry{
		Queue: queue, Key: key, FromStatus: "failed", ToStatus: "dead_letter", CreatedAt: time.Now(),
	})
	return nil
}

func (s *Store) ExtendLease(ctx context.Context, queue, key string, duration time.Duration) error {
	objKey := inProgressKey(queue, key)
	head, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: &s.bucket, Key: &objKey})
	if err != nil {
		return store.ErrNotFound
	}

	meta := decodeMeta(head.Metadata)
	exp := time.Now().Add(duration)
	meta.LeaseExpires = &exp

	_, err = s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:   &s.bucket,
		Key:      &objKey,
		Metadata: encodeMeta(meta),
	})
	return err
}

func (s *Store) Transition(ctx context.Context, queue, key string, from, to store.Status, opts ...store.TransitionOption) error {
	o := store.ApplyTransitionOptions(opts)

	// Find the item and verify its current status via metadata.
	srcKey := s.prefixForStatus(queue, key, from)
	head, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: &s.bucket, Key: &srcKey})
	if err != nil {
		return store.ErrNotFound
	}

	meta := decodeMeta(head.Metadata)

	// Check actual status from metadata for conflict detection.
	actualStatus := meta.Status
	if actualStatus == "" {
		// Infer from prefix.
		actualStatus = string(from)
	}
	if actualStatus != string(from) {
		return store.ErrConflict
	}

	meta.Status = string(to)
	if o.WorkerID != "" {
		meta.WorkerID = o.WorkerID
	}
	if o.ErrorMessage != "" {
		meta.ErrorMessage = o.ErrorMessage
	}

	dstKey := s.prefixForStatus(queue, key, to)
	if srcKey == dstKey {
		// Same prefix — update metadata in place.
		_, err = s.client.PutObject(ctx, &s3.PutObjectInput{
			Bucket:   &s.bucket,
			Key:      &dstKey,
			Metadata: encodeMeta(meta),
		})
		if err != nil {
			return err
		}
	} else {
		// Move to new prefix.
		_, err = s.client.PutObject(ctx, &s3.PutObjectInput{
			Bucket:   &s.bucket,
			Key:      &dstKey,
			Metadata: encodeMeta(meta),
		})
		if err != nil {
			return fmt.Errorf("put: %w", err)
		}
		s.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: &s.bucket, Key: &srcKey})
	}

	s.writeHistory(ctx, queue, key, store.HistoryEntry{
		Queue: queue, Key: key, FromStatus: string(from), ToStatus: string(to),
		WorkerID: o.WorkerID, CreatedAt: time.Now(),
	})
	return nil
}

func (s *Store) prefixForStatus(queue, key string, status store.Status) string {
	switch status {
	case store.StatusPending:
		return queuedKey(queue, key)
	case store.StatusClaimed, store.StatusRunning:
		return inProgressKey(queue, key)
	case store.StatusSucceeded:
		return succeededKey(queue, key)
	case store.StatusFailed:
		return failedKey(queue, key)
	case store.StatusDeadLetter:
		return deadLetterKey(queue, key)
	default:
		return inProgressKey(queue, key)
	}
}

// findItem looks for an item across multiple candidate S3 keys and returns
// the first match with its metadata.
func (s *Store) findItem(ctx context.Context, queue, key string, candidates ...string) (string, itemMeta, error) {
	for _, objKey := range candidates {
		head, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: &s.bucket, Key: &objKey})
		if err == nil {
			return objKey, decodeMeta(head.Metadata), nil
		}
	}
	return "", itemMeta{}, store.ErrNotFound
}

// --- Queue Management ---

func (s *Store) EnsureQueue(ctx context.Context, queue string, cfg store.QueueConfig) error {
	s.mu.Lock()
	s.configs[queue] = cfg
	s.mu.Unlock()

	// Persist to S3.
	body, _ := json.Marshal(cfg)
	key := queueConfigKey(queue)
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: &s.bucket,
		Key:    &key,
		Body:   bytes.NewReader(body),
	})
	return err
}

func (s *Store) RepairCounter(_ context.Context, _ string) error {
	// No counter to repair — S3 counts are derived from listing.
	return nil
}

func (s *Store) getConfig(queue string) store.QueueConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	cfg, ok := s.configs[queue]
	if !ok {
		return store.QueueConfig{MaxConcurrency: 10, MaxRetry: 5}
	}
	return cfg
}

// --- Query Operations ---

func (s *Store) CountByStatus(ctx context.Context, queue string) (map[store.Status]int64, error) {
	counts := make(map[store.Status]int64)

	pending, _ := s.countPrefix(ctx, queuedPrefix(queue))
	inProgress, _ := s.countPrefix(ctx, inProgressPrefix(queue))
	succeeded, _ := s.countPrefix(ctx, succeededPrefix(queue))
	failed, _ := s.countPrefix(ctx, failedPrefix(queue))
	deadLetter, _ := s.countPrefix(ctx, deadLetterPrefix(queue))

	counts[store.StatusPending] = int64(pending)
	counts[store.StatusClaimed] = int64(inProgress)
	counts[store.StatusSucceeded] = int64(succeeded)
	counts[store.StatusFailed] = int64(failed)
	counts[store.StatusDeadLetter] = int64(deadLetter)

	return counts, nil
}

func (s *Store) List(ctx context.Context, filter store.ListFilter) ([]store.WorkItem, error) {
	var prefix string
	if filter.Status != nil {
		switch *filter.Status {
		case store.StatusPending:
			prefix = queuedPrefix(filter.Queue)
		case store.StatusClaimed, store.StatusRunning:
			prefix = inProgressPrefix(filter.Queue)
		case store.StatusSucceeded:
			prefix = succeededPrefix(filter.Queue)
		case store.StatusFailed:
			prefix = failedPrefix(filter.Queue)
		case store.StatusDeadLetter:
			prefix = deadLetterPrefix(filter.Queue)
		default:
			return nil, nil
		}
	} else {
		// List all — merge from all prefixes.
		var all []store.WorkItem
		for _, pfx := range []string{
			queuedPrefix(filter.Queue), inProgressPrefix(filter.Queue),
			succeededPrefix(filter.Queue), failedPrefix(filter.Queue),
			deadLetterPrefix(filter.Queue),
		} {
			items, _ := s.listItemsFromPrefix(ctx, filter.Queue, pfx)
			all = append(all, items...)
		}
		sort.Slice(all, func(i, j int) bool {
			if all[i].Priority != all[j].Priority {
				return all[i].Priority > all[j].Priority
			}
			return all[i].CreatedAt.Before(all[j].CreatedAt)
		})
		limit := filter.Limit
		if limit <= 0 {
			limit = 100
		}
		if limit > len(all) {
			limit = len(all)
		}
		return all[:limit], nil
	}

	items, err := s.listItemsFromPrefix(ctx, filter.Queue, prefix)
	if err != nil {
		return nil, err
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
	if limit > len(items) {
		limit = len(items)
	}
	return items[:limit], nil
}

func (s *Store) GetItem(ctx context.Context, queue, key string) (*store.WorkItem, error) {
	// Check each prefix.
	for _, entry := range []struct {
		objKey string
		status store.Status
	}{
		{queuedKey(queue, key), store.StatusPending},
		{inProgressKey(queue, key), store.StatusClaimed},
		{succeededKey(queue, key), store.StatusSucceeded},
		{failedKey(queue, key), store.StatusFailed},
		{deadLetterKey(queue, key), store.StatusDeadLetter},
	} {
		head, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{
			Bucket: &s.bucket,
			Key:    &entry.objKey,
		})
		if err == nil {
			meta := decodeMeta(head.Metadata)
			wi := metaToWorkItem(queue, key, entry.status, meta)
			return &wi, nil
		}
	}
	return nil, store.ErrNotFound
}

// --- Admin Queries ---

func (s *Store) ListQueues(ctx context.Context) ([]store.QueueInfo, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var queues []store.QueueInfo
	for name, cfg := range s.configs {
		qi := store.QueueInfo{
			Name:           name,
			MaxConcurrency: cfg.MaxConcurrency,
			MaxRetry:       cfg.MaxRetry,
			ComputeBackend: cfg.ComputeBackend,
			Counts:         make(map[string]int),
		}

		pending, _ := s.countPrefix(ctx, queuedPrefix(name))
		inProg, _ := s.countPrefix(ctx, inProgressPrefix(name))
		succeeded, _ := s.countPrefix(ctx, succeededPrefix(name))
		failed, _ := s.countPrefix(ctx, failedPrefix(name))
		dead, _ := s.countPrefix(ctx, deadLetterPrefix(name))

		qi.Counts["pending"] = pending
		qi.Counts["claimed"] = inProg
		qi.Counts["succeeded"] = succeeded
		qi.Counts["failed"] = failed
		qi.Counts["dead_letter"] = dead
		qi.InProgress = inProg

		queues = append(queues, qi)
	}

	sort.Slice(queues, func(i, j int) bool { return queues[i].Name < queues[j].Name })
	return queues, nil
}

func (s *Store) ListWorkers(_ context.Context, _ string) ([]store.WorkerLease, error) {
	// S3 store doesn't track worker registrations.
	return nil, nil
}

func (s *Store) PurgeDeadLetters(ctx context.Context, queue string) (int64, error) {
	prefix := deadLetterPrefix(queue)
	keys, err := s.listKeys(ctx, prefix)
	if err != nil {
		return 0, err
	}

	for _, key := range keys {
		s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
			Bucket: &s.bucket,
			Key:    &key,
		})
	}
	return int64(len(keys)), nil
}

// --- History ---

func (s *Store) RecordHistory(ctx context.Context, entry store.HistoryEntry) error {
	s.writeHistory(ctx, entry.Queue, entry.Key, entry)
	return nil
}

func (s *Store) GetItemHistory(ctx context.Context, queue, key string) ([]store.HistoryEntry, error) {
	prefix := historyPrefix(queue, key)
	keys, err := s.listKeys(ctx, prefix)
	if err != nil {
		return nil, err
	}

	var entries []store.HistoryEntry
	for _, objKey := range keys {
		out, err := s.client.GetObject(ctx, &s3.GetObjectInput{
			Bucket: &s.bucket,
			Key:    &objKey,
		})
		if err != nil {
			continue
		}
		body, _ := io.ReadAll(out.Body)
		out.Body.Close()

		var entry store.HistoryEntry
		if json.Unmarshal(body, &entry) == nil {
			entries = append(entries, entry)
		}
	}

	// Reverse for newest-first.
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].CreatedAt.After(entries[j].CreatedAt)
	})
	return entries, nil
}

func (s *Store) writeHistory(ctx context.Context, queue, key string, entry store.HistoryEntry) {
	if entry.CreatedAt.IsZero() {
		entry.CreatedAt = time.Now()
	}
	entry.Queue = queue
	entry.Key = key

	objKey := historyPrefix(queue, key) + entry.CreatedAt.Format("20060102T150405.000000000")
	body, _ := json.Marshal(entry)

	s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: &s.bucket,
		Key:    &objKey,
		Body:   bytes.NewReader(body),
	})
}

// --- Events ---

func (s *Store) Subscribe(ctx context.Context, queue string) (<-chan store.Event, error) {
	// S3 doesn't have native pub/sub. Poll for changes.
	ch := make(chan store.Event, 64)
	go func() {
		defer close(ch)
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()

		var lastKeys map[string]bool
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				currentKeys := make(map[string]bool)
				for _, pfx := range []string{queuedPrefix(queue), inProgressPrefix(queue)} {
					keys, _ := s.listKeys(ctx, pfx)
					for _, k := range keys {
						currentKeys[k] = true
					}
				}

				if lastKeys != nil {
					for k := range currentKeys {
						if !lastKeys[k] {
							// New key appeared — emit event.
							parts := strings.Split(k, "/")
							if len(parts) >= 3 {
								select {
								case ch <- store.Event{
									Queue:  queue,
									Key:    parts[len(parts)-1],
									Status: parts[len(parts)-2],
								}:
								case <-ctx.Done():
									return
								}
							}
						}
					}
				}
				lastKeys = currentKeys
			}
		}
	}()
	return ch, nil
}

// --- Internal helpers ---

type listedItem struct {
	key  string
	meta itemMeta
	etag string
}

func (s *Store) listWithMeta(ctx context.Context, prefix string) ([]listedItem, error) {
	keys, err := s.listKeys(ctx, prefix)
	if err != nil {
		return nil, err
	}

	var items []listedItem
	for _, objKey := range keys {
		head, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{
			Bucket: &s.bucket,
			Key:    &objKey,
		})
		if err != nil {
			continue
		}
		itemKey := strings.TrimPrefix(objKey, prefix)
		etag := ""
		if head.ETag != nil {
			etag = *head.ETag
		}
		items = append(items, listedItem{
			key:  itemKey,
			meta: decodeMeta(head.Metadata),
			etag: etag,
		})
	}
	return items, nil
}

func (s *Store) listItemsFromPrefix(ctx context.Context, queue, prefix string) ([]store.WorkItem, error) {
	listed, err := s.listWithMeta(ctx, prefix)
	if err != nil {
		return nil, err
	}

	status := store.StatusPending
	if strings.Contains(prefix, "/in-progress/") {
		status = store.StatusClaimed
	} else if strings.Contains(prefix, "/succeeded/") {
		status = store.StatusSucceeded
	} else if strings.Contains(prefix, "/failed/") {
		status = store.StatusFailed
	} else if strings.Contains(prefix, "/dead-letter/") {
		status = store.StatusDeadLetter
	}

	var items []store.WorkItem
	for _, item := range listed {
		items = append(items, metaToWorkItem(queue, item.key, status, item.meta))
	}
	return items, nil
}

func (s *Store) listKeys(ctx context.Context, prefix string) ([]string, error) {
	var keys []string
	paginator := s3.NewListObjectsV2Paginator(s.client, &s3.ListObjectsV2Input{
		Bucket: &s.bucket,
		Prefix: &prefix,
	})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("list objects: %w", err)
		}
		for _, obj := range page.Contents {
			keys = append(keys, *obj.Key)
		}
	}
	return keys, nil
}

func (s *Store) countPrefix(ctx context.Context, prefix string) (int, error) {
	keys, err := s.listKeys(ctx, prefix)
	return len(keys), err
}

func metaToWorkItem(queue, key string, status store.Status, meta itemMeta) store.WorkItem {
	return store.WorkItem{
		Queue:        queue,
		Key:          key,
		Status:       status,
		Priority:     meta.Priority,
		Attempts:     meta.Attempts,
		MaxAttempts:  meta.MaxAttempts,
		NotBefore:    meta.NotBefore,
		LeaseExpires: meta.LeaseExpires,
		WorkerID:     meta.WorkerID,
		ErrorMessage: meta.ErrorMessage,
		CreatedAt:    meta.CreatedAt,
		UpdatedAt:    meta.CreatedAt, // S3 doesn't have a separate updated_at
		ClaimedAt:    meta.ClaimedAt,
	}
}

// Suppress unused import warning.
var _ s3types.BucketLocationConstraint

// Verify interface compliance.
var _ store.Interface = (*Store)(nil)

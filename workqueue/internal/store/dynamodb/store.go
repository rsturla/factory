// Package dynamodb implements store.Interface using DynamoDB for the hot path
// (queue mechanics) and S3 for the cold path (history, archival).
//
// # Table schema
//
//	PK: "{queue}#{key}"   SK: "ITEM"     ← work items
//	PK: "_queue#{queue}"  SK: "CONFIG"   ← queue config (in_progress counter, leader, pause)
//	PK: "_schema"         SK: "VERSION"  ← schema version marker
//
// # GSI "ClaimIndex" — claim-time priority ordering
//
// Used by ClaimBatch to find the highest-priority pending items in a queue,
// and by CountByStatus for per-status item counts.
//
//	GSI1PK: "{queue}#{status}"                         (partition by queue + status)
//	GSI1SK: "{inverted_priority}#{created_at}"         (sort: highest priority first, FIFO within)
//
// Priority is inverted (1B - priority) so that higher priority → lower sort key → returned first.
//
// # GSI "LeaseIndex" — expired lease detection
//
// Sparse index: only items with active leases (claimed/running) are projected.
// Used by ListExpiredLeases to find items whose leases have expired without
// scanning all in-flight work.
//
//	GSI2PK: "{queue}#active"                           (partition by queue, active leases only)
//	GSI2SK: "{lease_expires_rfc3339}#{key}"            (sort by expiry time, key suffix for uniqueness)
//
// Items enter the LeaseIndex on claim, update on ExtendLease, and are removed
// on Complete/Fail/Requeue/Deadletter.
//
// # History
//
// History entries are stored in S3 at {queue}/history/{key}/{timestamp}.
package dynamodb

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	dyntypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/hummingbird-org/factory-workqueue/internal/store"
)

const (
	gsiName      = "ClaimIndex"
	leaseGSIName = "LeaseIndex"
	itemSK       = "ITEM"
	cfgSK        = "CONFIG"
)

// Config holds configuration for the DynamoDB+S3 store.
type Config struct {
	TableName     string // DynamoDB table name
	HistoryBucket string // S3 bucket for history entries
	Region        string
	DDBEndpoint   string // optional, for local DynamoDB
	S3Endpoint    string // optional, for MinIO/rustfs
}

// countCacheEntry holds cached CountByStatus results with an expiry time.
type countCacheEntry struct {
	counts map[store.Status]int64
	expiry time.Time
}

// Store implements store.Interface using DynamoDB + S3.
type Store struct {
	ddb        *dynamodb.Client
	s3client   *s3.Client
	table      string
	histBucket string

	countCache   map[string]countCacheEntry
	countCacheMu sync.Mutex
}

// New creates a new DynamoDB+S3 store.
func New(ctx context.Context, cfg Config) (*Store, error) {
	var opts []func(*awsconfig.LoadOptions) error
	if cfg.Region != "" {
		opts = append(opts, awsconfig.WithRegion(cfg.Region))
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("load aws config: %w", err)
	}

	var ddbOpts []func(*dynamodb.Options)
	if cfg.DDBEndpoint != "" {
		ddbOpts = append(ddbOpts, func(o *dynamodb.Options) {
			o.BaseEndpoint = aws.String(cfg.DDBEndpoint)
		})
	}

	var s3Opts []func(*s3.Options)
	if cfg.S3Endpoint != "" {
		s3Opts = append(s3Opts, func(o *s3.Options) {
			o.BaseEndpoint = aws.String(cfg.S3Endpoint)
			o.UsePathStyle = true
		})
	}

	return &Store{
		ddb:        dynamodb.NewFromConfig(awsCfg, ddbOpts...),
		s3client:   s3.NewFromConfig(awsCfg, s3Opts...),
		table:      cfg.TableName,
		histBucket: cfg.HistoryBucket,
		countCache: make(map[string]countCacheEntry),
	}, nil
}

// NewWithClients creates a store with injected clients (for testing).
func NewWithClients(ddb *dynamodb.Client, s3client *s3.Client, tableName, historyBucket string) *Store {
	return &Store{
		ddb:        ddb,
		s3client:   s3client,
		table:      tableName,
		histBucket: historyBucket,
		countCache: make(map[string]countCacheEntry),
	}
}

// CreateTable creates the DynamoDB table and GSI. Idempotent.
func (s *Store) CreateTable(ctx context.Context) error {
	_, err := s.ddb.CreateTable(ctx, &dynamodb.CreateTableInput{
		TableName: &s.table,
		KeySchema: []dyntypes.KeySchemaElement{
			{AttributeName: aws.String("PK"), KeyType: dyntypes.KeyTypeHash},
			{AttributeName: aws.String("SK"), KeyType: dyntypes.KeyTypeRange},
		},
		AttributeDefinitions: []dyntypes.AttributeDefinition{
			{AttributeName: aws.String("PK"), AttributeType: dyntypes.ScalarAttributeTypeS},
			{AttributeName: aws.String("SK"), AttributeType: dyntypes.ScalarAttributeTypeS},
			{AttributeName: aws.String("GSI1PK"), AttributeType: dyntypes.ScalarAttributeTypeS},
			{AttributeName: aws.String("GSI1SK"), AttributeType: dyntypes.ScalarAttributeTypeS},
			{AttributeName: aws.String("GSI2PK"), AttributeType: dyntypes.ScalarAttributeTypeS},
			{AttributeName: aws.String("GSI2SK"), AttributeType: dyntypes.ScalarAttributeTypeS},
		},
		GlobalSecondaryIndexes: []dyntypes.GlobalSecondaryIndex{
			{
				IndexName: aws.String(gsiName),
				KeySchema: []dyntypes.KeySchemaElement{
					{AttributeName: aws.String("GSI1PK"), KeyType: dyntypes.KeyTypeHash},
					{AttributeName: aws.String("GSI1SK"), KeyType: dyntypes.KeyTypeRange},
				},
				Projection: &dyntypes.Projection{
					ProjectionType: dyntypes.ProjectionTypeAll,
				},
			},
			{
				IndexName: aws.String(leaseGSIName),
				KeySchema: []dyntypes.KeySchemaElement{
					{AttributeName: aws.String("GSI2PK"), KeyType: dyntypes.KeyTypeHash},
					{AttributeName: aws.String("GSI2SK"), KeyType: dyntypes.KeyTypeRange},
				},
				Projection: &dyntypes.Projection{
					ProjectionType: dyntypes.ProjectionTypeAll,
				},
			},
		},
		BillingMode: dyntypes.BillingModePayPerRequest,
	})
	if err != nil {
		// Table already exists — check schema version.
		var resourceInUse *dyntypes.ResourceInUseException
		if ok := errors.As(err, &resourceInUse); ok {
			return s.checkSchemaVersion(ctx)
		}
		return fmt.Errorf("create table: %w", err)
	}

	// New table — write schema version marker.
	s.ddb.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: &s.table,
		Item: map[string]dyntypes.AttributeValue{
			"PK":             &dyntypes.AttributeValueMemberS{Value: "_schema"},
			"SK":             &dyntypes.AttributeValueMemberS{Value: "VERSION"},
			"schema_version": &dyntypes.AttributeValueMemberN{Value: strconv.Itoa(SchemaVersion)},
			"created_at":     &dyntypes.AttributeValueMemberS{Value: time.Now().Format(time.RFC3339Nano)},
		},
	})

	return nil
}

// SchemaVersion is the current DynamoDB table schema version.
// Increment this when the table structure changes (new GSI, TTL config, etc.).
const SchemaVersion = 2

func (s *Store) checkSchemaVersion(ctx context.Context) error {
	result, err := s.ddb.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: &s.table,
		Key: map[string]dyntypes.AttributeValue{
			"PK": &dyntypes.AttributeValueMemberS{Value: "_schema"},
			"SK": &dyntypes.AttributeValueMemberS{Value: "VERSION"},
		},
	})
	if err != nil {
		return nil // can't check — table might be pre-versioning, allow startup
	}

	if result.Item == nil {
		return nil // no version marker — table predates versioning, allow startup
	}

	vAttr, ok := result.Item["schema_version"].(*dyntypes.AttributeValueMemberN)
	if !ok {
		return nil
	}

	tableVersion, err := strconv.Atoi(vAttr.Value)
	if err != nil {
		return nil
	}

	if tableVersion != SchemaVersion {
		return fmt.Errorf(
			"DynamoDB table schema version mismatch: table has version %d, code expects version %d. "+
				"Manual table migration required", tableVersion, SchemaVersion)
	}

	return nil
}

// --- Key helpers ---
// See package doc for full GSI schema descriptions.

func itemPK(queue, key string) string { return queue + "#" + key }

// ClaimIndex keys — partition by queue+status, sort by priority (desc) + time.
// When ClaimShards > 1, pending items are distributed across N sub-partitions
// (e.g., "myqueue#pending#3") for write throughput. Non-pending statuses stay unsharded.
func claimIndexPK(queue string, status store.Status) string { return queue + "#" + string(status) }
func claimIndexPKSharded(queue string, status store.Status, shard int) string {
	return fmt.Sprintf("%s#%s#%d", queue, string(status), shard)
}
func pendingPK(queue string, shards int) string {
	if shards <= 1 {
		return claimIndexPK(queue, store.StatusPending)
	}
	return claimIndexPKSharded(queue, store.StatusPending, rand.IntN(shards))
}

// LeaseIndex keys — partition by queue (active leases only), sort by expiry.
func leaseIndexPK(queue string) string { return queue + "#active" }
func leaseIndexSK(leaseExpires time.Time, key string) string {
	return leaseExpires.Format(time.RFC3339Nano) + "#" + key
}

// claimIndexSK encodes priority (descending) and created_at (ascending) for server-side ordering.
// Priority is inverted: higher priority → lower sort key → returned first.
// Offset by 1 billion to handle negative priorities without sign issues.
func claimIndexSK(priority int, createdAt time.Time) string {
	invertedPriority := 1000000000 - priority
	return fmt.Sprintf("%010d#%s", invertedPriority, createdAt.Format(time.RFC3339Nano))
}

// --- DynamoDB item type ---

type ddbItem struct {
	PK            string `dynamodbav:"PK"`
	SK            string `dynamodbav:"SK"`
	GSI1PK        string `dynamodbav:"GSI1PK"`
	GSI1SK        string `dynamodbav:"GSI1SK"`
	GSI2PK        string `dynamodbav:"GSI2PK,omitempty"`
	GSI2SK        string `dynamodbav:"GSI2SK,omitempty"`
	PendingGSI1PK string `dynamodbav:"pending_gsi1pk,omitempty"`
	PendingGSI1SK string `dynamodbav:"pending_gsi1sk,omitempty"`
	Queue         string `dynamodbav:"queue"`
	Key           string `dynamodbav:"key"`
	Status        string `dynamodbav:"status"`
	Priority      int    `dynamodbav:"priority"`
	Attempts      int    `dynamodbav:"attempts"`
	MaxAttempts   int    `dynamodbav:"max_attempts"`
	NotBefore     string `dynamodbav:"not_before,omitempty"`
	LeaseExpires  string `dynamodbav:"lease_expires,omitempty"`
	WorkerID      string `dynamodbav:"worker_id,omitempty"`
	ErrorMessage  string `dynamodbav:"error_message,omitempty"`
	CreatedAt     string `dynamodbav:"created_at"`
	UpdatedAt     string `dynamodbav:"updated_at"`
	ClaimedAt     string `dynamodbav:"claimed_at,omitempty"`
	CompletedAt   string `dynamodbav:"completed_at,omitempty"`
}

func (d ddbItem) toWorkItem() store.WorkItem {
	wi := store.WorkItem{
		Queue:        d.Queue,
		Key:          d.Key,
		Status:       store.Status(d.Status),
		Priority:     d.Priority,
		Attempts:     d.Attempts,
		MaxAttempts:  d.MaxAttempts,
		WorkerID:     d.WorkerID,
		ErrorMessage: d.ErrorMessage,
	}
	wi.CreatedAt, _ = time.Parse(time.RFC3339Nano, d.CreatedAt)
	wi.UpdatedAt, _ = time.Parse(time.RFC3339Nano, d.UpdatedAt)
	wi.NotBefore = parseTimePtr(d.NotBefore)
	wi.LeaseExpires = parseTimePtr(d.LeaseExpires)
	wi.ClaimedAt = parseTimePtr(d.ClaimedAt)
	wi.CompletedAt = parseTimePtr(d.CompletedAt)
	return wi
}

func parseTimePtr(s string) *time.Time {
	if s == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return nil
	}
	return &t
}

func timeStr(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339Nano)
}

func timePtrStr(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.Format(time.RFC3339Nano)
}

// --- store.Interface implementation ---

func (s *Store) Enqueue(ctx context.Context, queue, key string, priority int, opts ...store.EnqueueOption) error {
	o := store.ApplyEnqueueOptions(opts)
	now := time.Now()
	cfg := s.getQueueConfig(ctx, queue)
	pendPartition := pendingPK(queue, cfg.ClaimShards)

	// Try to update existing pending item (merge priority upward).
	pk := itemPK(queue, key)
	_, err := s.ddb.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: &s.table,
		Key: map[string]dyntypes.AttributeValue{
			"PK": &dyntypes.AttributeValueMemberS{Value: pk},
			"SK": &dyntypes.AttributeValueMemberS{Value: itemSK},
		},
		ConditionExpression: aws.String("#status = :pending AND #priority < :newpriority"),
		UpdateExpression:    aws.String("SET #priority = :newpriority, #updated = :now, #gsi1sk = :gsi1sk, #pendgsi = :gsi1sk"),
		ExpressionAttributeNames: map[string]string{
			"#status":   "status",
			"#priority": "priority",
			"#updated":  "updated_at",
			"#gsi1sk":   "GSI1SK",
			"#pendgsi":  "pending_gsi1sk",
		},
		ExpressionAttributeValues: map[string]dyntypes.AttributeValue{
			":pending":     &dyntypes.AttributeValueMemberS{Value: "pending"},
			":newpriority": &dyntypes.AttributeValueMemberN{Value: strconv.Itoa(priority)},
			":now":         &dyntypes.AttributeValueMemberS{Value: timeStr(now)},
			":gsi1sk":      &dyntypes.AttributeValueMemberS{Value: claimIndexSK(priority, now)},
		},
	})
	if err == nil {
		return nil // priority merged upward
	}

	// Either doesn't exist or priority is already >= new. Put if not exists.
	pendingSK := claimIndexSK(priority, now)
	item := ddbItem{
		PK:            pk,
		SK:            itemSK,
		GSI1PK:        pendPartition,
		GSI1SK:        pendingSK,
		PendingGSI1PK: pendPartition,
		PendingGSI1SK: pendingSK,
		Queue:         queue,
		Key:           key,
		Status:        "pending",
		Priority:      priority,
		MaxAttempts:   5,
		CreatedAt:     timeStr(now),
		UpdatedAt:     timeStr(now),
	}
	if o.NotBefore != nil {
		item.NotBefore = timePtrStr(o.NotBefore)
	}

	av, err := attributevalue.MarshalMap(item)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}

	_, err = s.ddb.PutItem(ctx, &dynamodb.PutItemInput{
		TableName:           &s.table,
		Item:                av,
		ConditionExpression: aws.String("attribute_not_exists(PK)"),
	})
	if err != nil {
		var condFail *dyntypes.ConditionalCheckFailedException
		if !errors.As(err, &condFail) {
			return fmt.Errorf("put item: %w", err)
		}

		// Item exists. If it's in a terminal state, reset to pending.
		// If it's pending (priority already >=) or in-flight, no-op.
		_, resetErr := s.ddb.UpdateItem(ctx, &dynamodb.UpdateItemInput{
			TableName: &s.table,
			Key: map[string]dyntypes.AttributeValue{
				"PK": &dyntypes.AttributeValueMemberS{Value: pk},
				"SK": &dyntypes.AttributeValueMemberS{Value: itemSK},
			},
			ConditionExpression: aws.String("#status IN (:succeeded, :failed, :dead_letter)"),
			UpdateExpression: aws.String(
				"SET #status = :pending, #priority = :priority, #attempts = :zero, " +
					"#gsi1pk = :gsi1pk, #gsi1sk = :gsi1sk, #pendgpk = :gsi1pk, #pendgsi = :gsi1sk, #updated = :now, " +
					"#worker = :empty, #lease = :empty, #errmsg = :empty, " +
					"#claimed_at = :empty, #completed_at = :empty, #not_before = :notbefore"),
			ExpressionAttributeNames: map[string]string{
				"#status": "status", "#priority": "priority", "#attempts": "attempts",
				"#gsi1pk": "GSI1PK", "#gsi1sk": "GSI1SK",
				"#pendgpk": "pending_gsi1pk", "#pendgsi": "pending_gsi1sk", "#updated": "updated_at",
				"#worker": "worker_id", "#lease": "lease_expires", "#errmsg": "error_message",
				"#claimed_at": "claimed_at", "#completed_at": "completed_at", "#not_before": "not_before",
			},
			ExpressionAttributeValues: map[string]dyntypes.AttributeValue{
				":succeeded":   &dyntypes.AttributeValueMemberS{Value: "succeeded"},
				":failed":      &dyntypes.AttributeValueMemberS{Value: "failed"},
				":dead_letter": &dyntypes.AttributeValueMemberS{Value: "dead_letter"},
				":pending":     &dyntypes.AttributeValueMemberS{Value: "pending"},
				":priority":    &dyntypes.AttributeValueMemberN{Value: strconv.Itoa(priority)},
				":zero":        &dyntypes.AttributeValueMemberN{Value: "0"},
				":gsi1pk":      &dyntypes.AttributeValueMemberS{Value: pendPartition},
				":gsi1sk":      &dyntypes.AttributeValueMemberS{Value: claimIndexSK(priority, now)},
				":now":         &dyntypes.AttributeValueMemberS{Value: timeStr(now)},
				":empty":       &dyntypes.AttributeValueMemberS{Value: ""},
				":notbefore":   &dyntypes.AttributeValueMemberS{Value: timePtrStr(o.NotBefore)},
			},
		})
		// Ignore condition failure — means it's pending or in-flight, which is fine.
		_ = resetErr
		return nil
	}
	return nil
}

func (s *Store) EnqueueBatch(ctx context.Context, queue string, items []store.BatchEnqueueItem) (int, error) {
	if len(items) == 0 {
		return 0, nil
	}

	// DynamoDB doesn't support conditional batch upserts, so we fan out
	// individual enqueues concurrently. Chunk to limit goroutine count.
	const concurrency = 25
	type result struct {
		err error
	}

	count := 0
	for start := 0; start < len(items); start += concurrency {
		end := start + concurrency
		if end > len(items) {
			end = len(items)
		}
		chunk := items[start:end]

		results := make([]result, len(chunk))
		var wg sync.WaitGroup
		for i, bi := range chunk {
			wg.Add(1)
			go func(idx int, item store.BatchEnqueueItem) {
				defer wg.Done()
				var opts []store.EnqueueOption
				if item.NotBefore != nil {
					opts = append(opts, store.WithNotBefore(*item.NotBefore))
				}
				results[idx].err = s.Enqueue(ctx, queue, item.Key, item.Priority, opts...)
			}(i, bi)
		}
		wg.Wait()

		for _, r := range results {
			if r.err == nil {
				count++
			}
		}
	}

	return count, nil
}

func (s *Store) ClaimBatch(ctx context.Context, queue string, batchSize int, workerID string, leaseDuration time.Duration) ([]store.WorkItem, error) {
	cfg := s.getQueueConfig(ctx, queue)

	candidates, err := s.queryPendingItems(ctx, queue, cfg.ClaimShards, min(batchSize*2, 1000))
	if err != nil {
		return nil, err
	}
	limit := min(batchSize, cfg.MaxConcurrency)

	now := time.Now()
	leaseExp := now.Add(leaseDuration)

	var claimed []store.WorkItem
	for _, item := range candidates {
		if len(claimed) >= limit {
			break
		}

		// Filter out not-before items.
		if item.NotBefore != "" {
			nb, _ := time.Parse(time.RFC3339Nano, item.NotBefore)
			if now.Before(nb) {
				continue
			}
		}

		// Atomically increment in_progress counter. If at capacity, stop.
		if !s.tryIncrementInProgress(ctx, queue, cfg.MaxConcurrency) {
			break
		}

		// Atomic claim via conditional update.
		pk := itemPK(item.Queue, item.Key)
		_, err := s.ddb.UpdateItem(ctx, &dynamodb.UpdateItemInput{
			TableName: &s.table,
			Key: map[string]dyntypes.AttributeValue{
				"PK": &dyntypes.AttributeValueMemberS{Value: pk},
				"SK": &dyntypes.AttributeValueMemberS{Value: itemSK},
			},
			ConditionExpression: aws.String("#status = :pending"),
			UpdateExpression: aws.String(
				"SET #status = :claimed, #gsi1pk = :gsi1pk, #gsi1sk = :gsi1sk, " +
					"#worker = :worker, #lease = :lease, #attempts = #attempts + :one, " +
					"#claimed_at = :now, #updated = :now, " +
					"#gsi2pk = :gsi2pk, #gsi2sk = :gsi2sk"),
			ExpressionAttributeNames: map[string]string{
				"#status":     "status",
				"#gsi1pk":     "GSI1PK",
				"#gsi1sk":     "GSI1SK",
				"#worker":     "worker_id",
				"#lease":      "lease_expires",
				"#attempts":   "attempts",
				"#claimed_at": "claimed_at",
				"#updated":    "updated_at",
				"#gsi2pk":     "GSI2PK",
				"#gsi2sk":     "GSI2SK",
			},
			ExpressionAttributeValues: map[string]dyntypes.AttributeValue{
				":pending": &dyntypes.AttributeValueMemberS{Value: "pending"},
				":claimed": &dyntypes.AttributeValueMemberS{Value: "claimed"},
				":gsi1pk":  &dyntypes.AttributeValueMemberS{Value: claimIndexPK(queue, store.StatusClaimed)},
				":gsi1sk":  &dyntypes.AttributeValueMemberS{Value: claimIndexSK(item.Priority, item.toWorkItem().CreatedAt)},
				":worker":  &dyntypes.AttributeValueMemberS{Value: workerID},
				":lease":   &dyntypes.AttributeValueMemberS{Value: timeStr(leaseExp)},
				":one":     &dyntypes.AttributeValueMemberN{Value: "1"},
				":now":     &dyntypes.AttributeValueMemberS{Value: timeStr(now)},
				":gsi2pk":  &dyntypes.AttributeValueMemberS{Value: leaseIndexPK(queue)},
				":gsi2sk":  &dyntypes.AttributeValueMemberS{Value: leaseIndexSK(leaseExp, item.Key)},
			},
			ReturnValues: dyntypes.ReturnValueAllNew,
		})
		if err != nil {
			// Condition failed — another dispatcher claimed it. Release the slot.
			s.decrementInProgress(ctx, queue)
			continue
		}

		// Re-read to get the updated item.
		wi := item.toWorkItem()
		wi.Status = store.StatusClaimed
		wi.WorkerID = workerID
		wi.LeaseExpires = &leaseExp
		wi.Attempts++
		wi.ClaimedAt = &now
		wi.UpdatedAt = now
		claimed = append(claimed, wi)

		s.writeHistory(ctx, queue, item.Key, store.HistoryEntry{
			Queue: queue, Key: item.Key, FromStatus: "pending", ToStatus: "claimed",
			WorkerID: workerID, Attempt: wi.Attempts, CreatedAt: now,
		})
	}

	return claimed, nil
}

func (s *Store) Complete(ctx context.Context, queue, key string) error {
	now := time.Now()
	pk := itemPK(queue, key)

	_, err := s.ddb.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{
		TransactItems: []dyntypes.TransactWriteItem{
			{
				Update: &dyntypes.Update{
					TableName: &s.table,
					Key: map[string]dyntypes.AttributeValue{
						"PK": &dyntypes.AttributeValueMemberS{Value: pk},
						"SK": &dyntypes.AttributeValueMemberS{Value: itemSK},
					},
					ConditionExpression: aws.String("#status = :claimed OR #status = :running"),
					UpdateExpression: aws.String(
						"SET #status = :status, #gsi1pk = :gsi1pk, #completed = :now, #updated = :now, #lease = :empty " +
							"REMOVE #gsi2pk, #gsi2sk"),
					ExpressionAttributeNames: map[string]string{
						"#status":    "status",
						"#gsi1pk":    "GSI1PK",
						"#completed": "completed_at",
						"#updated":   "updated_at",
						"#lease":     "lease_expires",
						"#gsi2pk":    "GSI2PK",
						"#gsi2sk":    "GSI2SK",
					},
					ExpressionAttributeValues: map[string]dyntypes.AttributeValue{
						":claimed": &dyntypes.AttributeValueMemberS{Value: "claimed"},
						":running": &dyntypes.AttributeValueMemberS{Value: "running"},
						":status":  &dyntypes.AttributeValueMemberS{Value: string(store.StatusSucceeded)},
						":gsi1pk":  &dyntypes.AttributeValueMemberS{Value: claimIndexPK(queue, store.StatusSucceeded)},
						":now":     &dyntypes.AttributeValueMemberS{Value: timeStr(now)},
						":empty":   &dyntypes.AttributeValueMemberS{Value: ""},
					},
				},
			},
			s.decrementCounterItem(queue),
		},
	})
	if err != nil {
		if isTransactionConflict(err) {
			return store.ErrNotFound
		}
		return fmt.Errorf("complete: %w", err)
	}

	s.writeHistory(ctx, queue, key, store.HistoryEntry{
		Queue: queue, Key: key, FromStatus: "running", ToStatus: "succeeded",
		CreatedAt: now,
	})
	return nil
}

func (s *Store) Fail(ctx context.Context, queue, key string, errMsg string) error {
	now := time.Now()
	pk := itemPK(queue, key)

	updateExpr := "SET #status = :status, #gsi1pk = :gsi1pk, #completed = :now, #updated = :now, #lease = :empty"
	exprNames := map[string]string{
		"#status":    "status",
		"#gsi1pk":    "GSI1PK",
		"#completed": "completed_at",
		"#updated":   "updated_at",
		"#lease":     "lease_expires",
		"#gsi2pk":    "GSI2PK",
		"#gsi2sk":    "GSI2SK",
	}
	exprValues := map[string]dyntypes.AttributeValue{
		":claimed": &dyntypes.AttributeValueMemberS{Value: "claimed"},
		":running": &dyntypes.AttributeValueMemberS{Value: "running"},
		":status":  &dyntypes.AttributeValueMemberS{Value: string(store.StatusFailed)},
		":gsi1pk":  &dyntypes.AttributeValueMemberS{Value: claimIndexPK(queue, store.StatusFailed)},
		":now":     &dyntypes.AttributeValueMemberS{Value: timeStr(now)},
		":empty":   &dyntypes.AttributeValueMemberS{Value: ""},
	}

	if errMsg != "" {
		updateExpr += ", #errmsg = :errmsg"
		exprNames["#errmsg"] = "error_message"
		exprValues[":errmsg"] = &dyntypes.AttributeValueMemberS{Value: errMsg}
	}

	updateExpr += " REMOVE #gsi2pk, #gsi2sk"

	_, err := s.ddb.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: &s.table,
		Key: map[string]dyntypes.AttributeValue{
			"PK": &dyntypes.AttributeValueMemberS{Value: pk},
			"SK": &dyntypes.AttributeValueMemberS{Value: itemSK},
		},
		ConditionExpression:       aws.String("#status = :claimed OR #status = :running"),
		UpdateExpression:          aws.String(updateExpr),
		ExpressionAttributeNames:  exprNames,
		ExpressionAttributeValues: exprValues,
	})
	if err != nil {
		var condFail *dyntypes.ConditionalCheckFailedException
		if ok := errors.As(err, &condFail); ok {
			return store.ErrNotFound
		}
		return fmt.Errorf("fail: %w", err)
	}

	// Do NOT decrement in_progress here. Fail() is always followed by
	// Requeue() or Deadletter(), which handle the decrement. Decrementing
	// in both places would double-decrement the counter.

	s.writeHistory(ctx, queue, key, store.HistoryEntry{
		Queue: queue, Key: key, FromStatus: "running", ToStatus: "failed",
		ErrorMessage: errMsg, CreatedAt: now,
	})
	return nil
}

func (s *Store) Requeue(ctx context.Context, queue, key string, opts ...store.RequeueOption) error {
	o := store.ApplyRequeueOptions(opts)
	now := time.Now()
	pk := itemPK(queue, key)

	notBefore := ""
	if o.NotBefore != nil {
		notBefore = timePtrStr(o.NotBefore)
	}

	_, err := s.ddb.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{
		TransactItems: []dyntypes.TransactWriteItem{
			{
				Update: &dyntypes.Update{
					TableName: &s.table,
					Key: map[string]dyntypes.AttributeValue{
						"PK": &dyntypes.AttributeValueMemberS{Value: pk},
						"SK": &dyntypes.AttributeValueMemberS{Value: itemSK},
					},
					ConditionExpression: aws.String("#status IN (:claimed, :running, :failed)"),
					UpdateExpression: aws.String(
						"SET #status = :pending, #gsi1pk = #pendgpk, #gsi1sk = #pendgsi, " +
							"#worker = :empty, #lease = :empty, #errmsg = :empty, " +
							"#claimed_at = :empty, #completed_at = :empty, " +
							"#not_before = :notbefore, #updated = :now " +
							"REMOVE #gsi2pk, #gsi2sk"),
					ExpressionAttributeNames: map[string]string{
						"#status":       "status",
						"#gsi1pk":       "GSI1PK",
						"#gsi1sk":       "GSI1SK",
						"#pendgpk":      "pending_gsi1pk",
						"#pendgsi":      "pending_gsi1sk",
						"#worker":       "worker_id",
						"#lease":        "lease_expires",
						"#errmsg":       "error_message",
						"#claimed_at":   "claimed_at",
						"#completed_at": "completed_at",
						"#not_before":   "not_before",
						"#updated":      "updated_at",
						"#gsi2pk":       "GSI2PK",
						"#gsi2sk":       "GSI2SK",
					},
					ExpressionAttributeValues: map[string]dyntypes.AttributeValue{
						":pending":   &dyntypes.AttributeValueMemberS{Value: "pending"},
						":claimed":   &dyntypes.AttributeValueMemberS{Value: "claimed"},
						":running":   &dyntypes.AttributeValueMemberS{Value: "running"},
						":failed":    &dyntypes.AttributeValueMemberS{Value: "failed"},
						":empty":     &dyntypes.AttributeValueMemberS{Value: ""},
						":notbefore": &dyntypes.AttributeValueMemberS{Value: notBefore},
						":now":       &dyntypes.AttributeValueMemberS{Value: timeStr(now)},
					},
				},
			},
			s.decrementCounterItem(queue),
		},
	})
	if err != nil {
		if isTransactionConflict(err) {
			return store.ErrNotFound
		}
		return fmt.Errorf("requeue: %w", err)
	}

	s.writeHistory(ctx, queue, key, store.HistoryEntry{
		Queue: queue, Key: key, FromStatus: "running", ToStatus: "pending", CreatedAt: now,
	})
	return nil
}

func (s *Store) RequeueUndoAttempt(ctx context.Context, queue, key string, notBefore time.Time) error {
	now := time.Now()
	pk := itemPK(queue, key)

	_, err := s.ddb.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{
		TransactItems: []dyntypes.TransactWriteItem{
			{
				Update: &dyntypes.Update{
					TableName: &s.table,
					Key: map[string]dyntypes.AttributeValue{
						"PK": &dyntypes.AttributeValueMemberS{Value: pk},
						"SK": &dyntypes.AttributeValueMemberS{Value: itemSK},
					},
					ConditionExpression: aws.String("#status IN (:claimed, :running)"),
					UpdateExpression: aws.String(
						"SET #status = :pending, #gsi1pk = #pendgpk, #gsi1sk = #pendgsi, " +
							"#worker = :empty, #lease = :empty, " +
							"#errmsg = :empty, #claimed_at = :empty, #completed_at = :empty, " +
							"#not_before = :notbefore, #updated = :now " +
							"ADD #attempts :negone " +
							"REMOVE #gsi2pk, #gsi2sk"),
					ExpressionAttributeNames: map[string]string{
						"#status": "status", "#gsi1pk": "GSI1PK", "#gsi1sk": "GSI1SK",
						"#pendgpk": "pending_gsi1pk", "#pendgsi": "pending_gsi1sk",
						"#attempts": "attempts", "#worker": "worker_id", "#lease": "lease_expires",
						"#errmsg": "error_message", "#claimed_at": "claimed_at",
						"#completed_at": "completed_at", "#not_before": "not_before", "#updated": "updated_at",
						"#gsi2pk": "GSI2PK", "#gsi2sk": "GSI2SK",
					},
					ExpressionAttributeValues: map[string]dyntypes.AttributeValue{
						":pending":   &dyntypes.AttributeValueMemberS{Value: "pending"},
						":claimed":   &dyntypes.AttributeValueMemberS{Value: "claimed"},
						":running":   &dyntypes.AttributeValueMemberS{Value: "running"},
						":negone":    &dyntypes.AttributeValueMemberN{Value: "-1"},
						":empty":     &dyntypes.AttributeValueMemberS{Value: ""},
						":notbefore": &dyntypes.AttributeValueMemberS{Value: timeStr(notBefore)},
						":now":       &dyntypes.AttributeValueMemberS{Value: timeStr(now)},
					},
				},
			},
			s.decrementCounterItem(queue),
		},
	})
	if err != nil {
		if isTransactionConflict(err) {
			return store.ErrNotFound
		}
		return fmt.Errorf("requeue undo: %w", err)
	}

	s.writeHistory(ctx, queue, key, store.HistoryEntry{
		Queue: queue, Key: key, FromStatus: "claimed", ToStatus: "pending", CreatedAt: now,
	})
	return nil
}

func (s *Store) Deadletter(ctx context.Context, queue, key string) error {
	now := time.Now()
	pk := itemPK(queue, key)

	item, err := s.getItem(ctx, pk)
	if err != nil {
		return store.ErrNotFound
	}

	_, err = s.ddb.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{
		TransactItems: []dyntypes.TransactWriteItem{
			{
				Update: &dyntypes.Update{
					TableName: &s.table,
					Key: map[string]dyntypes.AttributeValue{
						"PK": &dyntypes.AttributeValueMemberS{Value: pk},
						"SK": &dyntypes.AttributeValueMemberS{Value: itemSK},
					},
					ConditionExpression: aws.String("#status IN (:claimed, :running, :failed)"),
					UpdateExpression: aws.String(
						"SET #status = :dl, #gsi1pk = :gsi1pk, #gsi1sk = :gsi1sk, " +
							"#completed = :now, #updated = :now, #lease = :empty " +
							"REMOVE #gsi2pk, #gsi2sk"),
					ExpressionAttributeNames: map[string]string{
						"#status": "status", "#gsi1pk": "GSI1PK", "#gsi1sk": "GSI1SK",
						"#completed": "completed_at", "#updated": "updated_at", "#lease": "lease_expires",
						"#gsi2pk": "GSI2PK", "#gsi2sk": "GSI2SK",
					},
					ExpressionAttributeValues: map[string]dyntypes.AttributeValue{
						":claimed": &dyntypes.AttributeValueMemberS{Value: "claimed"},
						":running": &dyntypes.AttributeValueMemberS{Value: "running"},
						":failed":  &dyntypes.AttributeValueMemberS{Value: "failed"},
						":dl":      &dyntypes.AttributeValueMemberS{Value: "dead_letter"},
						":gsi1pk":  &dyntypes.AttributeValueMemberS{Value: claimIndexPK(queue, store.StatusDeadLetter)},
						":gsi1sk":  &dyntypes.AttributeValueMemberS{Value: claimIndexSK(item.Priority, item.toWorkItem().CreatedAt)},
						":now":     &dyntypes.AttributeValueMemberS{Value: timeStr(now)},
						":empty":   &dyntypes.AttributeValueMemberS{Value: ""},
					},
				},
			},
			s.decrementCounterItem(queue),
		},
	})
	if err != nil {
		if isTransactionConflict(err) {
			return store.ErrNotFound
		}
		return fmt.Errorf("deadletter: %w", err)
	}

	s.writeHistory(ctx, queue, key, store.HistoryEntry{
		Queue: queue, Key: key, FromStatus: "failed", ToStatus: "dead_letter", CreatedAt: now,
	})
	return nil
}

func (s *Store) ExtendLease(ctx context.Context, queue, key string, duration time.Duration) error {
	pk := itemPK(queue, key)
	exp := time.Now().Add(duration)

	_, err := s.ddb.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: &s.table,
		Key: map[string]dyntypes.AttributeValue{
			"PK": &dyntypes.AttributeValueMemberS{Value: pk},
			"SK": &dyntypes.AttributeValueMemberS{Value: itemSK},
		},
		ConditionExpression: aws.String("#status IN (:claimed, :running)"),
		UpdateExpression:    aws.String("SET #lease = :lease, #updated = :now, #gsi2sk = :gsi2sk"),
		ExpressionAttributeNames: map[string]string{
			"#status": "status", "#lease": "lease_expires", "#updated": "updated_at",
			"#gsi2sk": "GSI2SK",
		},
		ExpressionAttributeValues: map[string]dyntypes.AttributeValue{
			":claimed": &dyntypes.AttributeValueMemberS{Value: "claimed"},
			":running": &dyntypes.AttributeValueMemberS{Value: "running"},
			":lease":   &dyntypes.AttributeValueMemberS{Value: timeStr(exp)},
			":now":     &dyntypes.AttributeValueMemberS{Value: timeStr(time.Now())},
			":gsi2sk":  &dyntypes.AttributeValueMemberS{Value: leaseIndexSK(exp, key)},
		},
	})
	if err != nil {
		var condFail *dyntypes.ConditionalCheckFailedException
		if ok := errors.As(err, &condFail); ok {
			return store.ErrNotFound
		}
		return fmt.Errorf("extend lease: %w", err)
	}
	return nil
}

func (s *Store) Transition(ctx context.Context, queue, key string, from, to store.Status, opts ...store.TransitionOption) error {
	o := store.ApplyTransitionOptions(opts)
	if !store.ValidTransition(from, to) {
		return store.ErrInvalidTransition
	}
	now := time.Now()
	pk := itemPK(queue, key)

	item, err := s.getItem(ctx, pk)
	if err != nil {
		return store.ErrNotFound
	}

	var updateExpr string
	exprNames := map[string]string{
		"#status": "status", "#gsi1pk": "GSI1PK", "#gsi1sk": "GSI1SK", "#updated": "updated_at",
	}
	exprValues := map[string]dyntypes.AttributeValue{
		":from": &dyntypes.AttributeValueMemberS{Value: string(from)},
		":to":   &dyntypes.AttributeValueMemberS{Value: string(to)},
		":now":  &dyntypes.AttributeValueMemberS{Value: timeStr(now)},
	}

	if to == store.StatusPending {
		// Restore sharded pending GSI1PK from stored attribute.
		updateExpr = "SET #status = :to, #gsi1pk = #pendgpk, #gsi1sk = :gsi1sk, #updated = :now"
		exprNames["#pendgpk"] = "pending_gsi1pk"
		exprValues[":gsi1sk"] = &dyntypes.AttributeValueMemberS{Value: claimIndexSK(item.Priority, item.toWorkItem().CreatedAt)}
	} else {
		updateExpr = "SET #status = :to, #gsi1pk = :gsi1pk, #gsi1sk = :gsi1sk, #updated = :now"
		exprValues[":gsi1pk"] = &dyntypes.AttributeValueMemberS{Value: claimIndexPK(queue, to)}
		exprValues[":gsi1sk"] = &dyntypes.AttributeValueMemberS{Value: claimIndexSK(item.Priority, item.toWorkItem().CreatedAt)}
	}

	if o.WorkerID != "" {
		updateExpr += ", #worker = :worker"
		exprNames["#worker"] = "worker_id"
		exprValues[":worker"] = &dyntypes.AttributeValueMemberS{Value: o.WorkerID}
	}
	if o.ErrorMessage != "" {
		updateExpr += ", #errmsg = :errmsg"
		exprNames["#errmsg"] = "error_message"
		exprValues[":errmsg"] = &dyntypes.AttributeValueMemberS{Value: o.ErrorMessage}
	}

	if to != store.StatusClaimed && to != store.StatusRunning {
		updateExpr += " REMOVE #gsi2pk, #gsi2sk"
		exprNames["#gsi2pk"] = "GSI2PK"
		exprNames["#gsi2sk"] = "GSI2SK"
	}

	_, err = s.ddb.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: &s.table,
		Key: map[string]dyntypes.AttributeValue{
			"PK": &dyntypes.AttributeValueMemberS{Value: pk},
			"SK": &dyntypes.AttributeValueMemberS{Value: itemSK},
		},
		ConditionExpression:       aws.String("#status = :from"),
		UpdateExpression:          aws.String(updateExpr),
		ExpressionAttributeNames:  exprNames,
		ExpressionAttributeValues: exprValues,
	})
	if err != nil {
		var condFail *dyntypes.ConditionalCheckFailedException
		if ok := errors.As(err, &condFail); ok {
			return store.ErrConflict
		}
		return fmt.Errorf("transition: %w", err)
	}

	s.writeHistory(ctx, queue, key, store.HistoryEntry{
		Queue: queue, Key: key, FromStatus: from, ToStatus: to,
		WorkerID: o.WorkerID, CreatedAt: now,
	})
	return nil
}

// --- Queue Management ---

func (s *Store) EnsureQueue(ctx context.Context, queue string, cfg store.QueueConfig) error {
	cfgJSON, _ := json.Marshal(cfg)
	pk := "_queue#" + queue

	// Upsert — always update config, preserve in_progress counter.
	_, err := s.ddb.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: &s.table,
		Key: map[string]dyntypes.AttributeValue{
			"PK": &dyntypes.AttributeValueMemberS{Value: pk},
			"SK": &dyntypes.AttributeValueMemberS{Value: cfgSK},
		},
		UpdateExpression: aws.String("SET config = :cfg"),
		ExpressionAttributeValues: map[string]dyntypes.AttributeValue{
			":cfg": &dyntypes.AttributeValueMemberS{Value: string(cfgJSON)},
		},
	})
	if err != nil {
		return fmt.Errorf("ensure queue: %w", err)
	}
	return nil
}

func (s *Store) RepairCounter(ctx context.Context, queue string) error {
	claimed, err := s.countByStatusSingle(ctx, queue, store.StatusClaimed)
	if err != nil {
		return fmt.Errorf("count claimed: %w", err)
	}
	running, err := s.countByStatusSingle(ctx, queue, store.StatusRunning)
	if err != nil {
		return fmt.Errorf("count running: %w", err)
	}

	pk := "_queue#" + queue
	_, err = s.ddb.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: &s.table,
		Key: map[string]dyntypes.AttributeValue{
			"PK": &dyntypes.AttributeValueMemberS{Value: pk},
			"SK": &dyntypes.AttributeValueMemberS{Value: cfgSK},
		},
		UpdateExpression: aws.String("SET in_progress = :val"),
		ExpressionAttributeValues: map[string]dyntypes.AttributeValue{
			":val": &dyntypes.AttributeValueMemberN{Value: strconv.FormatInt(claimed+running, 10)},
		},
	})
	if err != nil {
		return fmt.Errorf("repair counter: %w", err)
	}
	return nil
}

func (s *Store) SetQueuePaused(ctx context.Context, queue string, paused bool) error {
	pk := "_queue#" + queue
	_, err := s.ddb.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: &s.table,
		Key: map[string]dyntypes.AttributeValue{
			"PK": &dyntypes.AttributeValueMemberS{Value: pk},
			"SK": &dyntypes.AttributeValueMemberS{Value: cfgSK},
		},
		UpdateExpression: aws.String("SET paused = :p"),
		ExpressionAttributeValues: map[string]dyntypes.AttributeValue{
			":p": &dyntypes.AttributeValueMemberBOOL{Value: paused},
		},
	})
	return err
}

func (s *Store) IsQueuePaused(ctx context.Context, queue string) (bool, error) {
	pk := "_queue#" + queue
	result, err := s.ddb.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: &s.table,
		Key: map[string]dyntypes.AttributeValue{
			"PK": &dyntypes.AttributeValueMemberS{Value: pk},
			"SK": &dyntypes.AttributeValueMemberS{Value: cfgSK},
		},
	})
	if err != nil || result.Item == nil {
		return false, nil
	}
	if v, ok := result.Item["paused"].(*dyntypes.AttributeValueMemberBOOL); ok {
		return v.Value, nil
	}
	return false, nil
}

func (s *Store) getQueueConfig(ctx context.Context, queue string) store.QueueConfig {
	pk := "_queue#" + queue
	result, err := s.ddb.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: &s.table,
		Key: map[string]dyntypes.AttributeValue{
			"PK": &dyntypes.AttributeValueMemberS{Value: pk},
			"SK": &dyntypes.AttributeValueMemberS{Value: cfgSK},
		},
	})
	if err != nil || result.Item == nil {
		return store.QueueConfig{MaxConcurrency: 10, MaxRetry: 5}
	}
	cfgAttr, ok := result.Item["config"].(*dyntypes.AttributeValueMemberS)
	if !ok {
		return store.QueueConfig{MaxConcurrency: 10, MaxRetry: 5}
	}
	var cfg store.QueueConfig
	json.Unmarshal([]byte(cfgAttr.Value), &cfg)
	return cfg
}

// --- Query Operations ---

func (s *Store) CountByStatus(ctx context.Context, queue string, statuses ...store.Status) (map[store.Status]int64, error) {
	fullQuery := len(statuses) == 0

	// Check cache for full (unfiltered) counts.
	if fullQuery {
		s.countCacheMu.Lock()
		if entry, ok := s.countCache[queue]; ok && time.Now().Before(entry.expiry) {
			s.countCacheMu.Unlock()
			return entry.counts, nil
		}
		s.countCacheMu.Unlock()

		statuses = []store.Status{
			store.StatusPending, store.StatusClaimed, store.StatusRunning,
			store.StatusSucceeded, store.StatusFailed, store.StatusDeadLetter,
		}
	}

	counts := make(map[store.Status]int64)
	for _, status := range statuses {
		n, err := s.countByStatusSingle(ctx, queue, status)
		if err != nil {
			return nil, err
		}
		if n > 0 {
			counts[status] = n
		}
	}

	// Cache full results.
	if fullQuery {
		s.countCacheMu.Lock()
		s.countCache[queue] = countCacheEntry{counts: counts, expiry: time.Now().Add(5 * time.Second)}
		s.countCacheMu.Unlock()
	}

	return counts, nil
}

// tryIncrementInProgress atomically increments the in_progress counter on the
// queue config item. Returns false if the counter is already at maxConcurrency.
func (s *Store) tryIncrementInProgress(ctx context.Context, queue string, maxConcurrency int) bool {
	pk := "_queue#" + queue
	_, err := s.ddb.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: &s.table,
		Key: map[string]dyntypes.AttributeValue{
			"PK": &dyntypes.AttributeValueMemberS{Value: pk},
			"SK": &dyntypes.AttributeValueMemberS{Value: cfgSK},
		},
		ConditionExpression: aws.String("attribute_not_exists(in_progress) OR in_progress < :max"),
		UpdateExpression:    aws.String("ADD in_progress :one"),
		ExpressionAttributeValues: map[string]dyntypes.AttributeValue{
			":max": &dyntypes.AttributeValueMemberN{Value: strconv.Itoa(maxConcurrency)},
			":one": &dyntypes.AttributeValueMemberN{Value: "1"},
		},
	})
	return err == nil
}

// decrementInProgress atomically decrements the in_progress counter.
// Used only in ClaimBatch to release a slot when a conditional claim fails.
func (s *Store) decrementInProgress(ctx context.Context, queue string) {
	pk := "_queue#" + queue
	s.ddb.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: &s.table,
		Key: map[string]dyntypes.AttributeValue{
			"PK": &dyntypes.AttributeValueMemberS{Value: pk},
			"SK": &dyntypes.AttributeValueMemberS{Value: cfgSK},
		},
		ConditionExpression: aws.String("attribute_exists(in_progress) AND in_progress > :zero"),
		UpdateExpression:    aws.String("ADD in_progress :neg"),
		ExpressionAttributeValues: map[string]dyntypes.AttributeValue{
			":neg":  &dyntypes.AttributeValueMemberN{Value: "-1"},
			":zero": &dyntypes.AttributeValueMemberN{Value: "0"},
		},
	})
}

// decrementCounterItem returns a TransactWriteItem that atomically decrements
// the in_progress counter. Used by Complete/Requeue/RequeueUndoAttempt/Deadletter
// to bundle the counter update with the work item update in a single transaction.
func (s *Store) decrementCounterItem(queue string) dyntypes.TransactWriteItem {
	pk := "_queue#" + queue
	return dyntypes.TransactWriteItem{
		Update: &dyntypes.Update{
			TableName: &s.table,
			Key: map[string]dyntypes.AttributeValue{
				"PK": &dyntypes.AttributeValueMemberS{Value: pk},
				"SK": &dyntypes.AttributeValueMemberS{Value: cfgSK},
			},
			ConditionExpression: aws.String("attribute_exists(in_progress) AND in_progress > :zero"),
			UpdateExpression:    aws.String("ADD in_progress :neg"),
			ExpressionAttributeValues: map[string]dyntypes.AttributeValue{
				":neg":  &dyntypes.AttributeValueMemberN{Value: "-1"},
				":zero": &dyntypes.AttributeValueMemberN{Value: "0"},
			},
		},
	}
}

// isTransactionConflict checks if a TransactionCanceledException was caused
// by a conditional check failure on the first (work item) operation.
func isTransactionConflict(err error) bool {
	var txErr *dyntypes.TransactionCanceledException
	if !errors.As(err, &txErr) {
		return false
	}
	if len(txErr.CancellationReasons) > 0 {
		r := txErr.CancellationReasons[0]
		return r.Code != nil && *r.Code == "ConditionalCheckFailed"
	}
	return false
}

// countInProgressConsistent uses a strongly consistent Scan to count claimed+running items.
func (s *Store) countInProgressConsistent(ctx context.Context, queue string) (int64, error) {
	result, err := s.ddb.Scan(ctx, &dynamodb.ScanInput{
		TableName:        &s.table,
		FilterExpression: aws.String("#q = :queue AND (#status = :claimed OR #status = :running) AND SK = :sk"),
		ExpressionAttributeNames: map[string]string{
			"#q":      "queue",
			"#status": "status",
		},
		ExpressionAttributeValues: map[string]dyntypes.AttributeValue{
			":queue":   &dyntypes.AttributeValueMemberS{Value: queue},
			":claimed": &dyntypes.AttributeValueMemberS{Value: "claimed"},
			":running": &dyntypes.AttributeValueMemberS{Value: "running"},
			":sk":      &dyntypes.AttributeValueMemberS{Value: itemSK},
		},
		Select:         dyntypes.SelectCount,
		ConsistentRead: aws.Bool(true),
	})
	if err != nil {
		return 0, err
	}
	return int64(result.Count), nil
}

func (s *Store) countByStatusSingle(ctx context.Context, queue string, status store.Status) (int64, error) {
	if status == store.StatusPending {
		cfg := s.getQueueConfig(ctx, queue)
		if cfg.ClaimShards > 1 {
			return s.countByStatusSharded(ctx, queue, status, cfg.ClaimShards)
		}
	}
	return s.countGSIPartition(ctx, claimIndexPK(queue, status))
}

func (s *Store) countGSIPartition(ctx context.Context, gsiPK string) (int64, error) {
	result, err := s.ddb.Query(ctx, &dynamodb.QueryInput{
		TableName:              &s.table,
		IndexName:              aws.String(gsiName),
		KeyConditionExpression: aws.String("GSI1PK = :pk"),
		ExpressionAttributeValues: map[string]dyntypes.AttributeValue{
			":pk": &dyntypes.AttributeValueMemberS{Value: gsiPK},
		},
		Select: dyntypes.SelectCount,
	})
	if err != nil {
		return 0, err
	}
	return int64(result.Count), nil
}

func (s *Store) countByStatusSharded(ctx context.Context, queue string, status store.Status, shards int) (int64, error) {
	type result struct {
		count int64
		err   error
	}
	results := make([]result, shards)
	var wg sync.WaitGroup
	for i := range shards {
		wg.Add(1)
		go func(shard int) {
			defer wg.Done()
			c, err := s.countGSIPartition(ctx, claimIndexPKSharded(queue, status, shard))
			results[shard] = result{count: c, err: err}
		}(i)
	}
	wg.Wait()

	// Also count the unsharded partition for items enqueued before sharding was enabled.
	unsharded, _ := s.countGSIPartition(ctx, claimIndexPK(queue, status))

	var total int64 = unsharded
	for _, r := range results {
		if r.err != nil {
			return 0, r.err
		}
		total += r.count
	}
	return total, nil
}

func (s *Store) List(ctx context.Context, filter store.ListFilter) ([]store.WorkItem, error) {
	limit := filter.Limit
	if limit <= 0 {
		limit = 100
	}

	if filter.Status != nil {
		fetchLimit := limit
		if filter.Offset > 0 {
			fetchLimit += filter.Offset
		}
		items, err := s.listByStatus(ctx, filter.Queue, *filter.Status, fetchLimit)
		if err != nil {
			return nil, err
		}
		if filter.Offset > 0 {
			if filter.Offset >= len(items) {
				return nil, nil
			}
			items = items[filter.Offset:]
		}
		if limit > len(items) {
			limit = len(items)
		}
		return items[:limit], nil
	}

	// List all statuses and merge.
	fetchLimit := limit
	if filter.Offset > 0 {
		fetchLimit += filter.Offset
	}
	var all []store.WorkItem
	for _, status := range []store.Status{
		store.StatusPending, store.StatusClaimed, store.StatusRunning,
		store.StatusSucceeded, store.StatusFailed, store.StatusDeadLetter,
	} {
		items, _ := s.listByStatus(ctx, filter.Queue, status, fetchLimit)
		all = append(all, items...)
	}

	sort.Slice(all, func(i, j int) bool {
		if all[i].Priority != all[j].Priority {
			return all[i].Priority > all[j].Priority
		}
		return all[i].CreatedAt.Before(all[j].CreatedAt)
	})

	// Apply offset.
	if filter.Offset > 0 {
		if filter.Offset >= len(all) {
			return nil, nil
		}
		all = all[filter.Offset:]
	}

	if limit > len(all) {
		limit = len(all)
	}
	return all[:limit], nil
}

// queryPendingItems queries pending items across shards, merges by priority order.
// With shards <= 1, this is a single GSI query (zero overhead).
func (s *Store) queryPendingItems(ctx context.Context, queue string, shards, limit int) ([]ddbItem, error) {
	if shards <= 1 {
		return s.queryGSIPartition(ctx, claimIndexPK(queue, store.StatusPending), limit)
	}

	// Query all shards + the unsharded partition (for items predating shard config) in parallel.
	type shardResult struct {
		items []ddbItem
		err   error
	}
	results := make([]shardResult, shards+1)
	var wg sync.WaitGroup
	for i := range shards {
		wg.Add(1)
		go func(shard int) {
			defer wg.Done()
			pk := claimIndexPKSharded(queue, store.StatusPending, shard)
			items, err := s.queryGSIPartition(ctx, pk, limit)
			results[shard] = shardResult{items: items, err: err}
		}(i)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		items, err := s.queryGSIPartition(ctx, claimIndexPK(queue, store.StatusPending), limit)
		results[shards] = shardResult{items: items, err: err}
	}()
	wg.Wait()

	var all []ddbItem
	for _, r := range results {
		if r.err != nil {
			return nil, r.err
		}
		all = append(all, r.items...)
	}

	sort.Slice(all, func(i, j int) bool { return all[i].GSI1SK < all[j].GSI1SK })

	if len(all) > limit {
		all = all[:limit]
	}
	return all, nil
}

func (s *Store) queryGSIPartition(ctx context.Context, gsiPK string, limit int) ([]ddbItem, error) {
	result, err := s.ddb.Query(ctx, &dynamodb.QueryInput{
		TableName:              &s.table,
		IndexName:              aws.String(gsiName),
		KeyConditionExpression: aws.String("GSI1PK = :pk"),
		ExpressionAttributeValues: map[string]dyntypes.AttributeValue{
			":pk": &dyntypes.AttributeValueMemberS{Value: gsiPK},
		},
		Limit: aws.Int32(int32(limit)),
	})
	if err != nil {
		return nil, fmt.Errorf("query claim index: %w", err)
	}

	var items []ddbItem
	for _, rawItem := range result.Items {
		var item ddbItem
		if err := attributevalue.UnmarshalMap(rawItem, &item); err != nil {
			continue
		}
		items = append(items, item)
	}
	return items, nil
}

func (s *Store) listByStatus(ctx context.Context, queue string, status store.Status, limit int) ([]store.WorkItem, error) {
	if status == store.StatusPending {
		cfg := s.getQueueConfig(ctx, queue)
		if cfg.ClaimShards > 1 {
			return s.listByStatusSharded(ctx, queue, status, cfg.ClaimShards, limit)
		}
	}
	return s.listGSIPartition(ctx, claimIndexPK(queue, status), limit)
}

func (s *Store) listGSIPartition(ctx context.Context, gsiPK string, limit int) ([]store.WorkItem, error) {
	result, err := s.ddb.Query(ctx, &dynamodb.QueryInput{
		TableName:              &s.table,
		IndexName:              aws.String(gsiName),
		KeyConditionExpression: aws.String("GSI1PK = :pk"),
		ExpressionAttributeValues: map[string]dyntypes.AttributeValue{
			":pk": &dyntypes.AttributeValueMemberS{Value: gsiPK},
		},
		Limit: aws.Int32(int32(limit)),
	})
	if err != nil {
		return nil, err
	}

	var items []store.WorkItem
	for _, rawItem := range result.Items {
		var item ddbItem
		if err := attributevalue.UnmarshalMap(rawItem, &item); err != nil {
			continue
		}
		items = append(items, item.toWorkItem())
	}
	return items, nil
}

func (s *Store) listByStatusSharded(ctx context.Context, queue string, status store.Status, shards, limit int) ([]store.WorkItem, error) {
	type shardResult struct {
		items []store.WorkItem
		err   error
	}
	results := make([]shardResult, shards)
	var wg sync.WaitGroup
	for i := range shards {
		wg.Add(1)
		go func(shard int) {
			defer wg.Done()
			pk := claimIndexPKSharded(queue, status, shard)
			items, err := s.listGSIPartition(ctx, pk, limit)
			results[shard] = shardResult{items: items, err: err}
		}(i)
	}
	wg.Wait()

	// Also query the unsharded partition for items predating shard config.
	unsharded, _ := s.listGSIPartition(ctx, claimIndexPK(queue, status), limit)

	all := unsharded
	for _, r := range results {
		if r.err != nil {
			return nil, r.err
		}
		all = append(all, r.items...)
	}

	sort.Slice(all, func(i, j int) bool {
		if all[i].Priority != all[j].Priority {
			return all[i].Priority > all[j].Priority
		}
		return all[i].CreatedAt.Before(all[j].CreatedAt)
	})

	if len(all) > limit {
		all = all[:limit]
	}
	return all, nil
}

func (s *Store) ListExpiredLeases(ctx context.Context, queue string, limit int) ([]store.WorkItem, error) {
	if limit <= 0 {
		limit = 100
	}
	now := time.Now()

	result, err := s.ddb.Query(ctx, &dynamodb.QueryInput{
		TableName:              &s.table,
		IndexName:              aws.String(leaseGSIName),
		KeyConditionExpression: aws.String("GSI2PK = :pk AND GSI2SK < :now"),
		ExpressionAttributeValues: map[string]dyntypes.AttributeValue{
			":pk":  &dyntypes.AttributeValueMemberS{Value: leaseIndexPK(queue)},
			":now": &dyntypes.AttributeValueMemberS{Value: now.Format(time.RFC3339Nano)},
		},
		Limit: aws.Int32(int32(limit)),
	})
	if err != nil {
		return nil, fmt.Errorf("query lease index: %w", err)
	}

	var items []store.WorkItem
	for _, rawItem := range result.Items {
		var item ddbItem
		if err := attributevalue.UnmarshalMap(rawItem, &item); err != nil {
			continue
		}
		items = append(items, item.toWorkItem())
	}
	return items, nil
}

func (s *Store) GetItem(ctx context.Context, queue, key string) (*store.WorkItem, error) {
	item, err := s.getItem(ctx, itemPK(queue, key))
	if err != nil {
		return nil, store.ErrNotFound
	}
	wi := item.toWorkItem()
	return &wi, nil
}

func (s *Store) getItem(ctx context.Context, pk string) (*ddbItem, error) {
	result, err := s.ddb.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: &s.table,
		Key: map[string]dyntypes.AttributeValue{
			"PK": &dyntypes.AttributeValueMemberS{Value: pk},
			"SK": &dyntypes.AttributeValueMemberS{Value: itemSK},
		},
	})
	if err != nil {
		return nil, err
	}
	if result.Item == nil {
		return nil, store.ErrNotFound
	}
	var item ddbItem
	if err := attributevalue.UnmarshalMap(result.Item, &item); err != nil {
		return nil, err
	}
	return &item, nil
}

// --- Admin Queries ---

func (s *Store) ListQueues(ctx context.Context) ([]store.QueueInfo, error) {
	// Scan for queue config items.
	result, err := s.ddb.Scan(ctx, &dynamodb.ScanInput{
		TableName:        &s.table,
		FilterExpression: aws.String("SK = :cfg AND begins_with(PK, :prefix)"),
		ExpressionAttributeValues: map[string]dyntypes.AttributeValue{
			":cfg":    &dyntypes.AttributeValueMemberS{Value: cfgSK},
			":prefix": &dyntypes.AttributeValueMemberS{Value: "_queue#"},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("scan queues: %w", err)
	}

	var queues []store.QueueInfo
	for _, item := range result.Items {
		pkAttr, ok := item["PK"].(*dyntypes.AttributeValueMemberS)
		if !ok {
			continue
		}
		queueName := strings.TrimPrefix(pkAttr.Value, "_queue#")

		// Parse config directly from scan result instead of re-fetching.
		cfg := store.QueueConfig{MaxConcurrency: 10, MaxRetry: 5}
		if v, ok := item["config"].(*dyntypes.AttributeValueMemberS); ok {
			json.Unmarshal([]byte(v.Value), &cfg)
		}

		// Check paused state from the same scan item.
		paused := false
		if v, ok := item["paused"].(*dyntypes.AttributeValueMemberBOOL); ok {
			paused = v.Value
		}

		counts, _ := s.CountByStatus(ctx, queueName)
		qi := store.QueueInfo{
			Name:           queueName,
			MaxConcurrency: cfg.MaxConcurrency,
			MaxRetry:       cfg.MaxRetry,
			Paused:         paused,
			InProgress:     int(counts[store.StatusClaimed] + counts[store.StatusRunning]),
			Counts:         make(map[string]int),
		}
		for status, count := range counts {
			qi.Counts[string(status)] = int(count)
		}
		queues = append(queues, qi)
	}

	sort.Slice(queues, func(i, j int) bool { return queues[i].Name < queues[j].Name })
	return queues, nil
}

func (s *Store) ListWorkers(_ context.Context, _ string) ([]store.WorkerLease, error) {
	return nil, nil
}

func (s *Store) PurgeDeadLetters(ctx context.Context, queue string) (int64, error) {
	items, err := s.listByStatus(ctx, queue, store.StatusDeadLetter, 1000)
	if err != nil {
		return 0, err
	}
	if len(items) == 0 {
		return 0, nil
	}

	var deleted int64
	for start := 0; start < len(items); start += 25 {
		end := start + 25
		if end > len(items) {
			end = len(items)
		}
		chunk := items[start:end]

		requests := make([]dyntypes.WriteRequest, len(chunk))
		for i, item := range chunk {
			requests[i] = dyntypes.WriteRequest{
				DeleteRequest: &dyntypes.DeleteRequest{
					Key: map[string]dyntypes.AttributeValue{
						"PK": &dyntypes.AttributeValueMemberS{Value: itemPK(queue, item.Key)},
						"SK": &dyntypes.AttributeValueMemberS{Value: itemSK},
					},
				},
			}
		}

		out, err := s.ddb.BatchWriteItem(ctx, &dynamodb.BatchWriteItemInput{
			RequestItems: map[string][]dyntypes.WriteRequest{s.table: requests},
		})
		if err != nil {
			return deleted, fmt.Errorf("batch delete: %w", err)
		}

		unprocessed := out.UnprocessedItems[s.table]
		for retries := 0; len(unprocessed) > 0 && retries < 3; retries++ {
			time.Sleep(time.Duration(100*(1<<retries)) * time.Millisecond)
			out, err = s.ddb.BatchWriteItem(ctx, &dynamodb.BatchWriteItemInput{
				RequestItems: map[string][]dyntypes.WriteRequest{s.table: unprocessed},
			})
			if err != nil {
				return deleted, fmt.Errorf("batch delete retry: %w", err)
			}
			unprocessed = out.UnprocessedItems[s.table]
		}

		deleted += int64(len(chunk) - len(unprocessed))
	}

	return deleted, nil
}

// --- History (S3-backed) ---

func (s *Store) RecordHistory(ctx context.Context, entry store.HistoryEntry) error {
	s.writeHistory(ctx, entry.Queue, entry.Key, entry)
	return nil
}

func (s *Store) GetItemHistory(ctx context.Context, queue, key string) ([]store.HistoryEntry, error) {
	prefix := queue + "/history/" + key + "/"
	keys, err := s.listS3Keys(ctx, prefix)
	if err != nil {
		return nil, err
	}

	var entries []store.HistoryEntry
	for _, objKey := range keys {
		out, err := s.s3client.GetObject(ctx, &s3.GetObjectInput{
			Bucket: &s.histBucket,
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

	objKey := fmt.Sprintf("%s/history/%s/%s_%08x",
		queue, key, entry.CreatedAt.Format("20060102T150405.000000000"), rand.Uint32())
	body, _ := json.Marshal(entry)

	s.s3client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: &s.histBucket,
		Key:    &objKey,
		Body:   bytes.NewReader(body),
	})
}

func (s *Store) listS3Keys(ctx context.Context, prefix string) ([]string, error) {
	var keys []string
	paginator := s3.NewListObjectsV2Paginator(s.s3client, &s3.ListObjectsV2Input{
		Bucket: &s.histBucket,
		Prefix: &prefix,
	})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, obj := range page.Contents {
			keys = append(keys, *obj.Key)
		}
	}
	return keys, nil
}

// --- Leader Election ---

func (s *Store) TryLeader(ctx context.Context, queue, workerID string, ttl time.Duration) (bool, error) {
	pk := "_queue#" + queue
	expires := timeStr(time.Now().Add(ttl))
	now := timeStr(time.Now())

	_, err := s.ddb.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: &s.table,
		Key: map[string]dyntypes.AttributeValue{
			"PK": &dyntypes.AttributeValueMemberS{Value: pk},
			"SK": &dyntypes.AttributeValueMemberS{Value: cfgSK},
		},
		ConditionExpression: aws.String(
			"attribute_exists(config) AND (attribute_not_exists(leader_id) OR leader_id = :empty OR leader_id = :wid OR leader_expires < :now)"),
		UpdateExpression: aws.String("SET leader_id = :wid, leader_expires = :exp"),
		ExpressionAttributeValues: map[string]dyntypes.AttributeValue{
			":wid":   &dyntypes.AttributeValueMemberS{Value: workerID},
			":exp":   &dyntypes.AttributeValueMemberS{Value: expires},
			":now":   &dyntypes.AttributeValueMemberS{Value: now},
			":empty": &dyntypes.AttributeValueMemberS{Value: ""},
		},
	})
	if err != nil {
		var condFail *dyntypes.ConditionalCheckFailedException
		if errors.As(err, &condFail) {
			return false, nil
		}
		return false, fmt.Errorf("try leader: %w", err)
	}
	return true, nil
}

// --- Events ---

func (s *Store) Subscribe(ctx context.Context, queue string) (<-chan store.Event, error) {
	// DynamoDB Streams could drive this, but for simplicity we poll the GSI.
	ch := make(chan store.Event, 64)
	go func() {
		defer close(ch)
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()

		var lastPending, lastClaimed int64
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				counts, _ := s.CountByStatus(ctx, queue, store.StatusPending, store.StatusClaimed)
				pending := counts[store.StatusPending]
				claimed := counts[store.StatusClaimed]
				if pending != lastPending || claimed != lastClaimed {
					select {
					case ch <- store.Event{Queue: queue, Status: "changed"}:
					case <-ctx.Done():
						return
					}
				}
				lastPending = pending
				lastClaimed = claimed
			}
		}
	}()
	return ch, nil
}

// --- Health ---

func (s *Store) Ping(ctx context.Context) error {
	_, err := s.ddb.DescribeTable(ctx, &dynamodb.DescribeTableInput{
		TableName: &s.table,
	})
	return err
}

// Verify interface compliance.
var _ store.Interface = (*Store)(nil)

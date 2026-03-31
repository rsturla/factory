// Package dynamodb implements store.Interface using DynamoDB for the hot path
// (queue mechanics) and S3 for the cold path (history, archival).
//
// Table schema:
//
//	PK: "{queue}#{key}"   SK: "ITEM"     ← work items
//	PK: "_queue#{queue}"  SK: "CONFIG"   ← queue config
//
// GSI "ClaimIndex":
//
//	PK: "{queue}#{status}"  SK: "{priority_desc}#{created_at}"
//
// Claiming uses Query on ClaimIndex + conditional UpdateItem, giving
// single-digit ms latency and atomic conflict detection — no scanning,
// no sharding needed.
//
// History entries are stored in S3 at {queue}/history/{key}/{timestamp}.
package dynamodb

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	dyntypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/hummingbird-org/factory/internal/store"
)

const (
	gsiName = "ClaimIndex"
	itemSK  = "ITEM"
	cfgSK   = "CONFIG"
)

// Config holds configuration for the DynamoDB+S3 store.
type Config struct {
	TableName     string // DynamoDB table name
	HistoryBucket string // S3 bucket for history entries
	Region        string
	DDBEndpoint   string // optional, for local DynamoDB
	S3Endpoint    string // optional, for MinIO/rustfs
}

// Store implements store.Interface using DynamoDB + S3.
type Store struct {
	ddb       *dynamodb.Client
	s3client  *s3.Client
	table     string
	histBucket string
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
	}, nil
}

// NewWithClients creates a store with injected clients (for testing).
func NewWithClients(ddb *dynamodb.Client, s3client *s3.Client, tableName, historyBucket string) *Store {
	return &Store{
		ddb:        ddb,
		s3client:   s3client,
		table:      tableName,
		histBucket: historyBucket,
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
				ProvisionedThroughput: &dyntypes.ProvisionedThroughput{
					ReadCapacityUnits:  aws.Int64(10),
					WriteCapacityUnits: aws.Int64(10),
				},
			},
		},
		BillingMode: dyntypes.BillingModePayPerRequest,
	})
	if err != nil {
		// Ignore if table already exists.
		var resourceInUse *dyntypes.ResourceInUseException
		if ok := errors.As(err, &resourceInUse); ok {
			return nil
		}
		return fmt.Errorf("create table: %w", err)
	}

	// Write a schema version marker so operators can tell which version
	// of the table schema is deployed.
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
const SchemaVersion = 1

// --- Key helpers ---

func itemPK(queue, key string) string     { return queue + "#" + key }
func gsi1PK(queue string, status store.Status) string { return queue + "#" + string(status) }

// gsi1SK encodes priority (descending) and created_at (ascending) for server-side ordering.
// Priority is inverted: higher priority → lower sort key → returned first.
// Offset by 1 billion to handle negative priorities without sign issues.
func gsi1SK(priority int, createdAt time.Time) string {
	invertedPriority := 1000000000 - priority
	return fmt.Sprintf("%010d#%s", invertedPriority, createdAt.Format(time.RFC3339Nano))
}

// --- DynamoDB item type ---

type ddbItem struct {
	PK           string `dynamodbav:"PK"`
	SK           string `dynamodbav:"SK"`
	GSI1PK       string `dynamodbav:"GSI1PK"`
	GSI1SK       string `dynamodbav:"GSI1SK"`
	Queue        string `dynamodbav:"queue"`
	Key          string `dynamodbav:"key"`
	Status       string `dynamodbav:"status"`
	Priority     int    `dynamodbav:"priority"`
	Attempts     int    `dynamodbav:"attempts"`
	MaxAttempts  int    `dynamodbav:"max_attempts"`
	NotBefore    string `dynamodbav:"not_before,omitempty"`
	LeaseExpires string `dynamodbav:"lease_expires,omitempty"`
	WorkerID     string `dynamodbav:"worker_id,omitempty"`
	ErrorMessage string `dynamodbav:"error_message,omitempty"`
	CreatedAt    string `dynamodbav:"created_at"`
	UpdatedAt    string `dynamodbav:"updated_at"`
	ClaimedAt    string `dynamodbav:"claimed_at,omitempty"`
	CompletedAt  string `dynamodbav:"completed_at,omitempty"`
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

	// Try to update existing pending item (merge priority upward).
	pk := itemPK(queue, key)
	_, err := s.ddb.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: &s.table,
		Key: map[string]dyntypes.AttributeValue{
			"PK": &dyntypes.AttributeValueMemberS{Value: pk},
			"SK": &dyntypes.AttributeValueMemberS{Value: itemSK},
		},
		ConditionExpression: aws.String("#status = :pending AND #priority < :newpriority"),
		UpdateExpression:    aws.String("SET #priority = :newpriority, #updated = :now, #gsi1sk = :gsi1sk"),
		ExpressionAttributeNames: map[string]string{
			"#status":   "status",
			"#priority": "priority",
			"#updated":  "updated_at",
			"#gsi1sk":   "GSI1SK",
		},
		ExpressionAttributeValues: map[string]dyntypes.AttributeValue{
			":pending":     &dyntypes.AttributeValueMemberS{Value: "pending"},
			":newpriority": &dyntypes.AttributeValueMemberN{Value: strconv.Itoa(priority)},
			":now":         &dyntypes.AttributeValueMemberS{Value: timeStr(now)},
			":gsi1sk":      &dyntypes.AttributeValueMemberS{Value: gsi1SK(priority, now)},
		},
	})
	if err == nil {
		return nil // priority merged upward
	}

	// Either doesn't exist or priority is already >= new. Put if not exists.
	item := ddbItem{
		PK:          pk,
		SK:          itemSK,
		GSI1PK:      gsi1PK(queue, store.StatusPending),
		GSI1SK:      gsi1SK(priority, now),
		Queue:       queue,
		Key:         key,
		Status:      "pending",
		Priority:    priority,
		MaxAttempts: 5,
		CreatedAt:   timeStr(now),
		UpdatedAt:   timeStr(now),
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
					"#gsi1pk = :gsi1pk, #gsi1sk = :gsi1sk, #updated = :now, " +
					"#worker = :empty, #lease = :empty, #errmsg = :empty, " +
					"#claimed_at = :empty, #completed_at = :empty, #not_before = :notbefore"),
			ExpressionAttributeNames: map[string]string{
				"#status": "status", "#priority": "priority", "#attempts": "attempts",
				"#gsi1pk": "GSI1PK", "#gsi1sk": "GSI1SK", "#updated": "updated_at",
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
				":gsi1pk":      &dyntypes.AttributeValueMemberS{Value: gsi1PK(queue, store.StatusPending)},
				":gsi1sk":      &dyntypes.AttributeValueMemberS{Value: gsi1SK(priority, now)},
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

func (s *Store) ClaimBatch(ctx context.Context, queue string, batchSize int, workerID string, leaseDuration time.Duration) ([]store.WorkItem, error) {
	// Query the ClaimIndex for pending items, ordered by priority DESC (inverted in GSI1SK).
	gsiPK := gsi1PK(queue, store.StatusPending)
	result, err := s.ddb.Query(ctx, &dynamodb.QueryInput{
		TableName:              &s.table,
		IndexName:              aws.String(gsiName),
		KeyConditionExpression: aws.String("GSI1PK = :pk"),
		ExpressionAttributeValues: map[string]dyntypes.AttributeValue{
			":pk": &dyntypes.AttributeValueMemberS{Value: gsiPK},
		},
		Limit: aws.Int32(int32(batchSize * 2)), // over-fetch to handle not-before filtering and races
	})
	if err != nil {
		return nil, fmt.Errorf("query claim index: %w", err)
	}

	// Check concurrency limit using strongly consistent scan on main table.
	cfg := s.getQueueConfig(ctx, queue)
	inProgress, _ := s.countInProgressConsistent(ctx, queue)
	remaining := cfg.MaxConcurrency - int(inProgress)
	if remaining <= 0 {
		return nil, nil
	}
	limit := min(batchSize, remaining)

	now := time.Now()
	leaseExp := now.Add(leaseDuration)

	var claimed []store.WorkItem
	for _, rawItem := range result.Items {
		if len(claimed) >= limit {
			break
		}

		// Re-check concurrency after each claim to prevent over-claiming
		// when multiple workers are claiming concurrently.
		if len(claimed) > 0 {
			currentInProgress, _ := s.countInProgressConsistent(ctx, queue)
			if int(currentInProgress) >= cfg.MaxConcurrency {
				break
			}
		}

		var item ddbItem
		if err := attributevalue.UnmarshalMap(rawItem, &item); err != nil {
			continue
		}

		// Filter out not-before items.
		if item.NotBefore != "" {
			nb, _ := time.Parse(time.RFC3339Nano, item.NotBefore)
			if now.Before(nb) {
				continue
			}
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
					"#claimed_at = :now, #updated = :now"),
			ExpressionAttributeNames: map[string]string{
				"#status":     "status",
				"#gsi1pk":     "GSI1PK",
				"#gsi1sk":     "GSI1SK",
				"#worker":     "worker_id",
				"#lease":      "lease_expires",
				"#attempts":   "attempts",
				"#claimed_at": "claimed_at",
				"#updated":    "updated_at",
			},
			ExpressionAttributeValues: map[string]dyntypes.AttributeValue{
				":pending": &dyntypes.AttributeValueMemberS{Value: "pending"},
				":claimed": &dyntypes.AttributeValueMemberS{Value: "claimed"},
				":gsi1pk":  &dyntypes.AttributeValueMemberS{Value: gsi1PK(queue, store.StatusClaimed)},
				":gsi1sk":  &dyntypes.AttributeValueMemberS{Value: gsi1SK(item.Priority, item.toWorkItem().CreatedAt)},
				":worker":  &dyntypes.AttributeValueMemberS{Value: workerID},
				":lease":   &dyntypes.AttributeValueMemberS{Value: timeStr(leaseExp)},
				":one":     &dyntypes.AttributeValueMemberN{Value: "1"},
				":now":     &dyntypes.AttributeValueMemberS{Value: timeStr(now)},
			},
			ReturnValues: dyntypes.ReturnValueAllNew,
		})
		if err != nil {
			// Condition failed — another dispatcher claimed it. Skip.
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
	return s.setTerminalStatus(ctx, queue, key, store.StatusSucceeded, "")
}

func (s *Store) Fail(ctx context.Context, queue, key string, errMsg string) error {
	return s.setTerminalStatus(ctx, queue, key, store.StatusFailed, errMsg)
}

func (s *Store) setTerminalStatus(ctx context.Context, queue, key string, status store.Status, errMsg string) error {
	now := time.Now()
	pk := itemPK(queue, key)

	updateExpr := "SET #status = :status, #gsi1pk = :gsi1pk, #completed = :now, #updated = :now" +
		", #lease = :empty"
	exprNames := map[string]string{
		"#status":    "status",
		"#gsi1pk":    "GSI1PK",
		"#completed": "completed_at",
		"#updated":   "updated_at",
		"#lease":     "lease_expires",
	}
	exprValues := map[string]dyntypes.AttributeValue{
		":claimed": &dyntypes.AttributeValueMemberS{Value: "claimed"},
		":running": &dyntypes.AttributeValueMemberS{Value: "running"},
		":status":  &dyntypes.AttributeValueMemberS{Value: string(status)},
		":gsi1pk":  &dyntypes.AttributeValueMemberS{Value: gsi1PK(queue, status)},
		":now":     &dyntypes.AttributeValueMemberS{Value: timeStr(now)},
		":empty":   &dyntypes.AttributeValueMemberS{Value: ""},
	}

	if errMsg != "" {
		updateExpr += ", #errmsg = :errmsg"
		exprNames["#errmsg"] = "error_message"
		exprValues[":errmsg"] = &dyntypes.AttributeValueMemberS{Value: errMsg}
	}

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
		return fmt.Errorf("set terminal status: %w", err)
	}

	s.writeHistory(ctx, queue, key, store.HistoryEntry{
		Queue: queue, Key: key, FromStatus: "running", ToStatus: string(status),
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

	// Read current item to get priority for GSI1SK.
	item, err := s.getItem(ctx, pk)
	if err != nil {
		return store.ErrNotFound
	}

	_, err = s.ddb.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: &s.table,
		Key: map[string]dyntypes.AttributeValue{
			"PK": &dyntypes.AttributeValueMemberS{Value: pk},
			"SK": &dyntypes.AttributeValueMemberS{Value: itemSK},
		},
		ConditionExpression: aws.String("#status IN (:claimed, :running, :failed)"),
		UpdateExpression: aws.String(
			"SET #status = :pending, #gsi1pk = :gsi1pk, #gsi1sk = :gsi1sk, " +
				"#worker = :empty, #lease = :empty, #errmsg = :empty, " +
				"#claimed_at = :empty, #completed_at = :empty, " +
				"#not_before = :notbefore, #updated = :now"),
		ExpressionAttributeNames: map[string]string{
			"#status":       "status",
			"#gsi1pk":       "GSI1PK",
			"#gsi1sk":       "GSI1SK",
			"#worker":       "worker_id",
			"#lease":        "lease_expires",
			"#errmsg":       "error_message",
			"#claimed_at":   "claimed_at",
			"#completed_at": "completed_at",
			"#not_before":   "not_before",
			"#updated":      "updated_at",
		},
		ExpressionAttributeValues: map[string]dyntypes.AttributeValue{
			":pending":   &dyntypes.AttributeValueMemberS{Value: "pending"},
			":claimed":   &dyntypes.AttributeValueMemberS{Value: "claimed"},
			":running":   &dyntypes.AttributeValueMemberS{Value: "running"},
			":failed":    &dyntypes.AttributeValueMemberS{Value: "failed"},
			":gsi1pk":    &dyntypes.AttributeValueMemberS{Value: gsi1PK(queue, store.StatusPending)},
			":gsi1sk":    &dyntypes.AttributeValueMemberS{Value: gsi1SK(item.Priority, item.toWorkItem().CreatedAt)},
			":empty":     &dyntypes.AttributeValueMemberS{Value: ""},
			":notbefore": &dyntypes.AttributeValueMemberS{Value: notBefore},
			":now":       &dyntypes.AttributeValueMemberS{Value: timeStr(now)},
		},
	})
	if err != nil {
		var condFail *dyntypes.ConditionalCheckFailedException
		if ok := errors.As(err, &condFail); ok {
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

	item, err := s.getItem(ctx, pk)
	if err != nil {
		return store.ErrNotFound
	}
	newAttempts := item.Attempts - 1
	if newAttempts < 0 {
		newAttempts = 0
	}

	_, err = s.ddb.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: &s.table,
		Key: map[string]dyntypes.AttributeValue{
			"PK": &dyntypes.AttributeValueMemberS{Value: pk},
			"SK": &dyntypes.AttributeValueMemberS{Value: itemSK},
		},
		ConditionExpression: aws.String("#status IN (:claimed, :running)"),
		UpdateExpression: aws.String(
			"SET #status = :pending, #gsi1pk = :gsi1pk, #gsi1sk = :gsi1sk, " +
				"#attempts = :attempts, #worker = :empty, #lease = :empty, " +
				"#errmsg = :empty, #claimed_at = :empty, #completed_at = :empty, " +
				"#not_before = :notbefore, #updated = :now"),
		ExpressionAttributeNames: map[string]string{
			"#status": "status", "#gsi1pk": "GSI1PK", "#gsi1sk": "GSI1SK",
			"#attempts": "attempts", "#worker": "worker_id", "#lease": "lease_expires",
			"#errmsg": "error_message", "#claimed_at": "claimed_at",
			"#completed_at": "completed_at", "#not_before": "not_before", "#updated": "updated_at",
		},
		ExpressionAttributeValues: map[string]dyntypes.AttributeValue{
			":pending":   &dyntypes.AttributeValueMemberS{Value: "pending"},
			":claimed":   &dyntypes.AttributeValueMemberS{Value: "claimed"},
			":running":   &dyntypes.AttributeValueMemberS{Value: "running"},
			":gsi1pk":    &dyntypes.AttributeValueMemberS{Value: gsi1PK(queue, store.StatusPending)},
			":gsi1sk":    &dyntypes.AttributeValueMemberS{Value: gsi1SK(item.Priority, item.toWorkItem().CreatedAt)},
			":attempts":  &dyntypes.AttributeValueMemberN{Value: strconv.Itoa(newAttempts)},
			":empty":     &dyntypes.AttributeValueMemberS{Value: ""},
			":notbefore": &dyntypes.AttributeValueMemberS{Value: timeStr(notBefore)},
			":now":       &dyntypes.AttributeValueMemberS{Value: timeStr(now)},
		},
	})
	if err != nil {
		var condFail *dyntypes.ConditionalCheckFailedException
		if ok := errors.As(err, &condFail); ok {
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

	_, err = s.ddb.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: &s.table,
		Key: map[string]dyntypes.AttributeValue{
			"PK": &dyntypes.AttributeValueMemberS{Value: pk},
			"SK": &dyntypes.AttributeValueMemberS{Value: itemSK},
		},
		ConditionExpression: aws.String("#status IN (:claimed, :running, :failed)"),
		UpdateExpression: aws.String(
			"SET #status = :dl, #gsi1pk = :gsi1pk, #gsi1sk = :gsi1sk, " +
				"#completed = :now, #updated = :now, #lease = :empty"),
		ExpressionAttributeNames: map[string]string{
			"#status": "status", "#gsi1pk": "GSI1PK", "#gsi1sk": "GSI1SK",
			"#completed": "completed_at", "#updated": "updated_at", "#lease": "lease_expires",
		},
		ExpressionAttributeValues: map[string]dyntypes.AttributeValue{
			":claimed": &dyntypes.AttributeValueMemberS{Value: "claimed"},
			":running": &dyntypes.AttributeValueMemberS{Value: "running"},
			":failed":  &dyntypes.AttributeValueMemberS{Value: "failed"},
			":dl":      &dyntypes.AttributeValueMemberS{Value: "dead_letter"},
			":gsi1pk":  &dyntypes.AttributeValueMemberS{Value: gsi1PK(queue, store.StatusDeadLetter)},
			":gsi1sk":  &dyntypes.AttributeValueMemberS{Value: gsi1SK(item.Priority, item.toWorkItem().CreatedAt)},
			":now":     &dyntypes.AttributeValueMemberS{Value: timeStr(now)},
			":empty":   &dyntypes.AttributeValueMemberS{Value: ""},
		},
	})
	if err != nil {
		var condFail *dyntypes.ConditionalCheckFailedException
		if ok := errors.As(err, &condFail); ok {
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
		UpdateExpression:    aws.String("SET #lease = :lease, #updated = :now"),
		ExpressionAttributeNames: map[string]string{
			"#status": "status", "#lease": "lease_expires", "#updated": "updated_at",
		},
		ExpressionAttributeValues: map[string]dyntypes.AttributeValue{
			":claimed": &dyntypes.AttributeValueMemberS{Value: "claimed"},
			":running": &dyntypes.AttributeValueMemberS{Value: "running"},
			":lease":   &dyntypes.AttributeValueMemberS{Value: timeStr(exp)},
			":now":     &dyntypes.AttributeValueMemberS{Value: timeStr(time.Now())},
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
	now := time.Now()
	pk := itemPK(queue, key)

	item, err := s.getItem(ctx, pk)
	if err != nil {
		return store.ErrNotFound
	}

	updateExpr := "SET #status = :to, #gsi1pk = :gsi1pk, #gsi1sk = :gsi1sk, #updated = :now"
	exprNames := map[string]string{
		"#status": "status", "#gsi1pk": "GSI1PK", "#gsi1sk": "GSI1SK", "#updated": "updated_at",
	}
	exprValues := map[string]dyntypes.AttributeValue{
		":from":   &dyntypes.AttributeValueMemberS{Value: string(from)},
		":to":     &dyntypes.AttributeValueMemberS{Value: string(to)},
		":gsi1pk": &dyntypes.AttributeValueMemberS{Value: gsi1PK(queue, to)},
		":gsi1sk": &dyntypes.AttributeValueMemberS{Value: gsi1SK(item.Priority, item.toWorkItem().CreatedAt)},
		":now":    &dyntypes.AttributeValueMemberS{Value: timeStr(now)},
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

	_, err = s.ddb.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:                 &s.table,
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
		Queue: queue, Key: key, FromStatus: string(from), ToStatus: string(to),
		WorkerID: o.WorkerID, CreatedAt: now,
	})
	return nil
}

// --- Queue Management ---

func (s *Store) EnsureQueue(ctx context.Context, queue string, cfg store.QueueConfig) error {
	cfgJSON, _ := json.Marshal(cfg)
	pk := "_queue#" + queue
	_, err := s.ddb.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: &s.table,
		Item: map[string]dyntypes.AttributeValue{
			"PK":     &dyntypes.AttributeValueMemberS{Value: pk},
			"SK":     &dyntypes.AttributeValueMemberS{Value: cfgSK},
			"config": &dyntypes.AttributeValueMemberS{Value: string(cfgJSON)},
		},
		ConditionExpression: aws.String("attribute_not_exists(PK)"),
	})
	if err != nil {
		// Already exists — that's fine.
		var condFail *dyntypes.ConditionalCheckFailedException
		if ok := errors.As(err, &condFail); ok {
			return nil
		}
		return fmt.Errorf("ensure queue: %w", err)
	}
	return nil
}

func (s *Store) RepairCounter(_ context.Context, _ string) error {
	return nil // no counter to repair — counts are derived from GSI queries
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

func (s *Store) CountByStatus(ctx context.Context, queue string) (map[store.Status]int64, error) {
	counts := make(map[store.Status]int64)
	for _, status := range []store.Status{
		store.StatusPending, store.StatusClaimed, store.StatusRunning,
		store.StatusSucceeded, store.StatusFailed, store.StatusDeadLetter,
	} {
		n, err := s.countByStatusSingle(ctx, queue, status)
		if err != nil {
			return nil, err
		}
		if n > 0 {
			counts[status] = n
		}
	}
	return counts, nil
}

// countInProgressConsistent uses a strongly consistent Scan to count claimed+running items.
// This avoids GSI eventual consistency issues that can cause over-claiming.
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
	result, err := s.ddb.Query(ctx, &dynamodb.QueryInput{
		TableName:              &s.table,
		IndexName:              aws.String(gsiName),
		KeyConditionExpression: aws.String("GSI1PK = :pk"),
		ExpressionAttributeValues: map[string]dyntypes.AttributeValue{
			":pk": &dyntypes.AttributeValueMemberS{Value: gsi1PK(queue, status)},
		},
		Select: dyntypes.SelectCount,
	})
	if err != nil {
		return 0, err
	}
	return int64(result.Count), nil
}

func (s *Store) List(ctx context.Context, filter store.ListFilter) ([]store.WorkItem, error) {
	limit := filter.Limit
	if limit <= 0 {
		limit = 100
	}

	if filter.Status != nil {
		return s.listByStatus(ctx, filter.Queue, *filter.Status, limit)
	}

	// List all statuses and merge.
	var all []store.WorkItem
	for _, status := range []store.Status{
		store.StatusPending, store.StatusClaimed, store.StatusRunning,
		store.StatusSucceeded, store.StatusFailed, store.StatusDeadLetter,
	} {
		items, _ := s.listByStatus(ctx, filter.Queue, status, limit)
		all = append(all, items...)
	}

	sort.Slice(all, func(i, j int) bool {
		if all[i].Priority != all[j].Priority {
			return all[i].Priority > all[j].Priority
		}
		return all[i].CreatedAt.Before(all[j].CreatedAt)
	})

	if limit > len(all) {
		limit = len(all)
	}
	return all[:limit], nil
}

func (s *Store) listByStatus(ctx context.Context, queue string, status store.Status, limit int) ([]store.WorkItem, error) {
	result, err := s.ddb.Query(ctx, &dynamodb.QueryInput{
		TableName:              &s.table,
		IndexName:              aws.String(gsiName),
		KeyConditionExpression: aws.String("GSI1PK = :pk"),
		ExpressionAttributeValues: map[string]dyntypes.AttributeValue{
			":pk": &dyntypes.AttributeValueMemberS{Value: gsi1PK(queue, status)},
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
		cfg := s.getQueueConfig(ctx, queueName)

		counts, _ := s.CountByStatus(ctx, queueName)
		qi := store.QueueInfo{
			Name:           queueName,
			MaxConcurrency: cfg.MaxConcurrency,
			MaxRetry:       cfg.MaxRetry,
			ComputeBackend: cfg.ComputeBackend,
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

	for _, item := range items {
		pk := itemPK(queue, item.Key)
		s.ddb.DeleteItem(ctx, &dynamodb.DeleteItemInput{
			TableName: &s.table,
			Key: map[string]dyntypes.AttributeValue{
				"PK": &dyntypes.AttributeValueMemberS{Value: pk},
				"SK": &dyntypes.AttributeValueMemberS{Value: itemSK},
			},
		})
	}
	return int64(len(items)), nil
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

// --- Events ---

func (s *Store) Subscribe(ctx context.Context, queue string) (<-chan store.Event, error) {
	// DynamoDB Streams could drive this, but for simplicity we poll the GSI.
	ch := make(chan store.Event, 64)
	go func() {
		defer close(ch)
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()

		var lastPending, lastClaimed int64
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				counts, _ := s.CountByStatus(ctx, queue)
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

// Verify interface compliance.
var _ store.Interface = (*Store)(nil)

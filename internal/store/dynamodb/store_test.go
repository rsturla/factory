package dynamodb_test

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	dyntypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/hummingbird-org/factory-workqueue/internal/store"
	"github.com/hummingbird-org/factory-workqueue/internal/store/conformance"
	storeddb "github.com/hummingbird-org/factory-workqueue/internal/store/dynamodb"
)

func TestDynamoDBConformance(t *testing.T) {
	ddbEndpoint := os.Getenv("DDB_TEST_ENDPOINT")
	if ddbEndpoint == "" {
		ddbEndpoint = "http://localhost:8000"
	}
	s3Endpoint := os.Getenv("S3_TEST_ENDPOINT")
	if s3Endpoint == "" {
		s3Endpoint = "http://localhost:9000"
	}
	s3Bucket := os.Getenv("S3_TEST_BUCKET")
	if s3Bucket == "" {
		s3Bucket = "factory-test"
	}

	ctx := context.Background()
	// DynamoDB Local accepts any credentials.
	ddbCfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion("us-east-1"),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(
			"test", "test", "",
		)),
	)
	if err != nil {
		t.Skipf("cannot load AWS config: %v", err)
	}

	ddbClient := dynamodb.NewFromConfig(ddbCfg, func(o *dynamodb.Options) {
		o.BaseEndpoint = &ddbEndpoint
	})

	// Verify DynamoDB is reachable.
	_, err = ddbClient.ListTables(ctx, &dynamodb.ListTablesInput{})
	if err != nil {
		t.Skipf("DynamoDB not reachable at %s: %v", ddbEndpoint, err)
	}

	// S3 uses its own credentials (rustfs defaults to rustfsadmin/rustfsadmin).
	s3AccessKey := os.Getenv("S3_ACCESS_KEY")
	if s3AccessKey == "" {
		s3AccessKey = "rustfsadmin"
	}
	s3SecretKey := os.Getenv("S3_SECRET_KEY")
	if s3SecretKey == "" {
		s3SecretKey = "rustfsadmin"
	}
	s3Cfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion("us-east-1"),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(
			s3AccessKey, s3SecretKey, "",
		)),
	)
	if err != nil {
		t.Skipf("cannot load S3 config: %v", err)
	}

	s3Client := awss3.NewFromConfig(s3Cfg, func(o *awss3.Options) {
		o.BaseEndpoint = &s3Endpoint
		o.UsePathStyle = true
	})

	// Verify S3 is reachable.
	_, err = s3Client.HeadBucket(ctx, &awss3.HeadBucketInput{Bucket: &s3Bucket})
	if err != nil {
		t.Skipf("S3 bucket %q not reachable at %s: %v", s3Bucket, s3Endpoint, err)
	}

	testNum := 0
	conformance.Run(t, func(t *testing.T) store.Interface {
		testNum++
		tableName := fmt.Sprintf("factory_test_%d", testNum)

		// Delete table if it exists from a previous run.
		ddbClient.DeleteTable(ctx, &dynamodb.DeleteTableInput{
			TableName: &tableName,
		})

		// Clean S3 history objects.
		cleanS3Prefix(t, s3Client, s3Bucket, "test/history/")

		s := storeddb.NewWithClients(ddbClient, s3Client, tableName, s3Bucket)
		if err := s.CreateTable(ctx); err != nil {
			t.Fatalf("CreateTable: %v", err)
		}

		// Wait for table to be active.
		waiter := dynamodb.NewTableExistsWaiter(ddbClient)
		waiter.Wait(ctx, &dynamodb.DescribeTableInput{TableName: &tableName}, 30_000_000_000) // 30s

		if err := s.EnsureQueue(ctx, "test", store.QueueConfig{
			MaxConcurrency: 10,
			MaxRetry:       5,
		}); err != nil {
			t.Fatalf("EnsureQueue: %v", err)
		}
		return s
	})
}

// setupShardedStore creates a fresh DynamoDB+S3 store with ClaimShards enabled.
func setupShardedStore(t *testing.T, shards int) store.Interface {
	t.Helper()
	ddbEndpoint := os.Getenv("DDB_TEST_ENDPOINT")
	if ddbEndpoint == "" {
		ddbEndpoint = "http://localhost:8000"
	}
	s3Endpoint := os.Getenv("S3_TEST_ENDPOINT")
	if s3Endpoint == "" {
		s3Endpoint = "http://localhost:9000"
	}
	s3Bucket := os.Getenv("S3_TEST_BUCKET")
	if s3Bucket == "" {
		s3Bucket = "factory-test"
	}

	ctx := context.Background()
	ddbCfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion("us-east-1"),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("test", "test", "")),
	)
	if err != nil {
		t.Skipf("cannot load AWS config: %v", err)
	}
	ddbClient := dynamodb.NewFromConfig(ddbCfg, func(o *dynamodb.Options) {
		o.BaseEndpoint = &ddbEndpoint
	})
	if _, err := ddbClient.ListTables(ctx, &dynamodb.ListTablesInput{}); err != nil {
		t.Skipf("DynamoDB not reachable at %s: %v", ddbEndpoint, err)
	}

	s3AccessKey := os.Getenv("S3_ACCESS_KEY")
	if s3AccessKey == "" {
		s3AccessKey = "rustfsadmin"
	}
	s3SecretKey := os.Getenv("S3_SECRET_KEY")
	if s3SecretKey == "" {
		s3SecretKey = "rustfsadmin"
	}
	s3Cfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion("us-east-1"),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(s3AccessKey, s3SecretKey, "")),
	)
	if err != nil {
		t.Skipf("cannot load S3 config: %v", err)
	}
	s3Client := awss3.NewFromConfig(s3Cfg, func(o *awss3.Options) {
		o.BaseEndpoint = &s3Endpoint
		o.UsePathStyle = true
	})
	if _, err := s3Client.HeadBucket(ctx, &awss3.HeadBucketInput{Bucket: &s3Bucket}); err != nil {
		t.Skipf("S3 bucket %q not reachable: %v", s3Bucket, err)
	}

	tableName := fmt.Sprintf("factory_shard_test_%d", shards)
	ddbClient.DeleteTable(ctx, &dynamodb.DeleteTableInput{TableName: &tableName})
	cleanS3Prefix(t, s3Client, s3Bucket, "test/history/")

	s := storeddb.NewWithClients(ddbClient, s3Client, tableName, s3Bucket)
	if err := s.CreateTable(ctx); err != nil {
		t.Fatalf("CreateTable: %v", err)
	}
	waiter := dynamodb.NewTableExistsWaiter(ddbClient)
	waiter.Wait(ctx, &dynamodb.DescribeTableInput{TableName: &tableName}, 30_000_000_000)

	if err := s.EnsureQueue(ctx, "test", store.QueueConfig{
		MaxConcurrency: 100,
		MaxRetry:       5,
		ClaimShards:    shards,
	}); err != nil {
		t.Fatalf("EnsureQueue: %v", err)
	}
	return s
}

func TestShardedClaimBatch(t *testing.T) {
	s := setupShardedStore(t, 4)
	ctx := context.Background()

	// Enqueue 20 items with varying priorities.
	for i := range 20 {
		if err := s.Enqueue(ctx, "test", fmt.Sprintf("shard-item-%02d", i), i); err != nil {
			t.Fatalf("Enqueue: %v", err)
		}
	}

	// Verify count sees all items across shards.
	counts, err := s.CountByStatus(ctx, "test")
	if err != nil {
		t.Fatalf("CountByStatus: %v", err)
	}
	if counts[store.StatusPending] != 20 {
		t.Errorf("pending count = %d, want 20", counts[store.StatusPending])
	}

	// Claim batch should find highest priority items across all shards.
	items, err := s.ClaimBatch(ctx, "test", 5, "worker-1", 5*time.Minute)
	if err != nil {
		t.Fatalf("ClaimBatch: %v", err)
	}
	if len(items) != 5 {
		t.Fatalf("claimed %d items, want 5", len(items))
	}

	// Verify priority ordering (highest first).
	for i := 1; i < len(items); i++ {
		if items[i].Priority > items[i-1].Priority {
			t.Errorf("item %d priority %d > item %d priority %d — not sorted",
				i, items[i].Priority, i-1, items[i-1].Priority)
		}
	}
}

func TestShardedRequeue(t *testing.T) {
	s := setupShardedStore(t, 4)
	ctx := context.Background()

	if err := s.Enqueue(ctx, "test", "requeue-key", 10); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	items, err := s.ClaimBatch(ctx, "test", 1, "w1", 5*time.Minute)
	if err != nil || len(items) != 1 {
		t.Fatalf("ClaimBatch: err=%v items=%d", err, len(items))
	}

	// Requeue — item should return to pending in its original shard.
	if err := s.Requeue(ctx, "test", "requeue-key"); err != nil {
		t.Fatalf("Requeue: %v", err)
	}

	// Should be claimable again.
	items, err = s.ClaimBatch(ctx, "test", 1, "w2", 5*time.Minute)
	if err != nil {
		t.Fatalf("ClaimBatch after requeue: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item after requeue, got %d", len(items))
	}
	if items[0].Key != "requeue-key" {
		t.Errorf("claimed key = %q, want requeue-key", items[0].Key)
	}
}

func TestShardedRequeueUndoAttempt(t *testing.T) {
	s := setupShardedStore(t, 4)
	ctx := context.Background()

	if err := s.Enqueue(ctx, "test", "undo-key", 5); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	items, err := s.ClaimBatch(ctx, "test", 1, "w1", 5*time.Minute)
	if err != nil || len(items) != 1 {
		t.Fatalf("ClaimBatch: err=%v items=%d", err, len(items))
	}
	if items[0].Attempts != 1 {
		t.Fatalf("attempts after claim = %d, want 1", items[0].Attempts)
	}

	if err := s.RequeueUndoAttempt(ctx, "test", "undo-key", time.Time{}); err != nil {
		t.Fatalf("RequeueUndoAttempt: %v", err)
	}

	items, err = s.ClaimBatch(ctx, "test", 1, "w2", 5*time.Minute)
	if err != nil || len(items) != 1 {
		t.Fatalf("ClaimBatch after undo: err=%v items=%d", err, len(items))
	}
	if items[0].Attempts != 1 {
		t.Errorf("attempts after undo+reclaim = %d, want 1 (undo decremented, claim incremented)", items[0].Attempts)
	}
}

func TestShardedTransitionToPending(t *testing.T) {
	s := setupShardedStore(t, 4)
	ctx := context.Background()

	if err := s.Enqueue(ctx, "test", "trans-key", 7); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	items, err := s.ClaimBatch(ctx, "test", 1, "w1", 5*time.Minute)
	if err != nil || len(items) != 1 {
		t.Fatalf("ClaimBatch: err=%v items=%d", err, len(items))
	}

	// Deadletter, then transition back to pending.
	if err := s.Deadletter(ctx, "test", "trans-key"); err != nil {
		t.Fatalf("Deadletter: %v", err)
	}
	if err := s.Transition(ctx, "test", "trans-key", store.StatusDeadLetter, store.StatusPending); err != nil {
		t.Fatalf("Transition to pending: %v", err)
	}

	// Should be claimable from sharded pending partition.
	items, err = s.ClaimBatch(ctx, "test", 1, "w2", 5*time.Minute)
	if err != nil {
		t.Fatalf("ClaimBatch after transition: %v", err)
	}
	if len(items) != 1 || items[0].Key != "trans-key" {
		t.Errorf("expected trans-key, got %v", items)
	}
}

func TestShardedFullLifecycle(t *testing.T) {
	s := setupShardedStore(t, 8)
	ctx := context.Background()

	// Enqueue 100 items across 8 shards.
	for i := range 100 {
		if err := s.Enqueue(ctx, "test", fmt.Sprintf("life-%03d", i), i%20); err != nil {
			t.Fatalf("Enqueue %d: %v", i, err)
		}
	}

	counts, err := s.CountByStatus(ctx, "test")
	if err != nil {
		t.Fatalf("CountByStatus: %v", err)
	}
	if counts[store.StatusPending] != 100 {
		t.Errorf("pending = %d, want 100", counts[store.StatusPending])
	}

	// Claim and complete all items in batches.
	var totalClaimed int
	for {
		items, err := s.ClaimBatch(ctx, "test", 10, "worker", 5*time.Minute)
		if err != nil {
			t.Fatalf("ClaimBatch: %v", err)
		}
		if len(items) == 0 {
			break
		}
		totalClaimed += len(items)
		for _, item := range items {
			if err := s.Complete(ctx, "test", item.Key); err != nil {
				t.Fatalf("Complete %s: %v", item.Key, err)
			}
		}
	}
	if totalClaimed != 100 {
		t.Errorf("total claimed = %d, want 100", totalClaimed)
	}

	// Wait for count cache (5s TTL) to expire before verifying final counts.
	time.Sleep(6 * time.Second)

	counts, err = s.CountByStatus(ctx, "test")
	if err != nil {
		t.Fatalf("CountByStatus after: %v", err)
	}
	if counts[store.StatusPending] != 0 {
		t.Errorf("pending after complete = %d, want 0", counts[store.StatusPending])
	}
	if counts[store.StatusSucceeded] != 100 {
		t.Errorf("succeeded = %d, want 100", counts[store.StatusSucceeded])
	}
}

func TestShardedListByStatus(t *testing.T) {
	s := setupShardedStore(t, 4)
	ctx := context.Background()

	for i := range 12 {
		if err := s.Enqueue(ctx, "test", fmt.Sprintf("list-%02d", i), i); err != nil {
			t.Fatalf("Enqueue: %v", err)
		}
	}

	pending := store.StatusPending
	items, err := s.List(ctx, store.ListFilter{Queue: "test", Status: &pending, Limit: 5})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(items) != 5 {
		t.Errorf("listed %d items, want 5", len(items))
	}
	// Verify priority ordering.
	for i := 1; i < len(items); i++ {
		if items[i].Priority > items[i-1].Priority {
			t.Errorf("list not sorted by priority: item %d=%d > item %d=%d",
				i, items[i].Priority, i-1, items[i-1].Priority)
		}
	}
}

// TestShardedDistribution verifies items actually land in different shard
// partitions by scanning the raw DynamoDB table and inspecting GSI1PK values.
func TestShardedDistribution(t *testing.T) {
	const shardCount = 4
	s := setupShardedStore(t, shardCount)
	ctx := context.Background()

	for i := range 100 {
		if err := s.Enqueue(ctx, "test", fmt.Sprintf("dist-%03d", i), i%10); err != nil {
			t.Fatalf("Enqueue: %v", err)
		}
	}

	// Scan raw table to inspect GSI1PK values.
	ddbEndpoint := os.Getenv("DDB_TEST_ENDPOINT")
	if ddbEndpoint == "" {
		ddbEndpoint = "http://localhost:8000"
	}
	ddbCfg, _ := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion("us-east-1"),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("test", "test", "")),
	)
	ddbClient := dynamodb.NewFromConfig(ddbCfg, func(o *dynamodb.Options) {
		o.BaseEndpoint = &ddbEndpoint
	})

	tableName := fmt.Sprintf("factory_shard_test_%d", shardCount)
	result, err := ddbClient.Scan(ctx, &dynamodb.ScanInput{
		TableName:        &tableName,
		FilterExpression: aws.String("SK = :sk"),
		ExpressionAttributeValues: map[string]dyntypes.AttributeValue{
			":sk": &dyntypes.AttributeValueMemberS{Value: "ITEM"},
		},
	})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}

	distribution := make(map[string]int)
	for _, item := range result.Items {
		gsi1pk, ok := item["GSI1PK"].(*dyntypes.AttributeValueMemberS)
		if !ok {
			continue
		}
		distribution[gsi1pk.Value]++
	}

	t.Logf("Shard distribution across %d items:", len(result.Items))
	shardedCount := 0
	for pk, count := range distribution {
		parts := strings.Split(pk, "#")
		if len(parts) == 3 {
			shardedCount += count
			t.Logf("  %s: %d items (shard %s)", pk, count, parts[2])
		} else {
			t.Logf("  %s: %d items (unsharded)", pk, count)
		}
	}

	if shardedCount == 0 {
		t.Fatal("FAIL: no items landed in sharded partitions — sharding not working")
	}
	if len(distribution) < 2 {
		t.Fatal("FAIL: all items in single partition — sharding not distributing")
	}

	// Verify pending_gsi1pk is set and matches GSI1PK.
	for _, item := range result.Items {
		gsi1pk := item["GSI1PK"].(*dyntypes.AttributeValueMemberS).Value
		pendGPK, ok := item["pending_gsi1pk"].(*dyntypes.AttributeValueMemberS)
		if !ok {
			t.Fatal("pending_gsi1pk attribute missing on item")
		}
		if pendGPK.Value != gsi1pk {
			t.Errorf("pending_gsi1pk (%s) != GSI1PK (%s)", pendGPK.Value, gsi1pk)
		}
	}

	t.Logf("Result: %d/%d items in sharded partitions, spread across %d partitions",
		shardedCount, len(result.Items), len(distribution))
}

func cleanS3Prefix(t *testing.T, client *awss3.Client, bucket, prefix string) {
	t.Helper()
	ctx := context.Background()
	paginator := awss3.NewListObjectsV2Paginator(client, &awss3.ListObjectsV2Input{
		Bucket: &bucket,
		Prefix: &prefix,
	})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return // bucket might not exist yet
		}
		for _, obj := range page.Contents {
			client.DeleteObject(ctx, &awss3.DeleteObjectInput{
				Bucket: &bucket,
				Key:    obj.Key,
			})
		}
	}
}

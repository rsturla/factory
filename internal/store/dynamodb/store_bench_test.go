package dynamodb_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/hummingbird-org/factory-workqueue/internal/store"
	storeddb "github.com/hummingbird-org/factory-workqueue/internal/store/dynamodb"
)

func TestMain(m *testing.M) {
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	os.Exit(m.Run())
}

func setupBench(b *testing.B) *storeddb.Store {
	b.Helper()

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
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(
			"test", "test", "",
		)),
	)
	if err != nil {
		b.Skipf("cannot load AWS config: %v", err)
	}

	ddbClient := dynamodb.NewFromConfig(ddbCfg, func(o *dynamodb.Options) {
		o.BaseEndpoint = &ddbEndpoint
	})

	// Verify DynamoDB is reachable.
	_, err = ddbClient.ListTables(ctx, &dynamodb.ListTablesInput{})
	if err != nil {
		b.Skipf("DynamoDB not reachable at %s: %v", ddbEndpoint, err)
	}

	// S3 credentials (rustfs defaults).
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
		b.Skipf("cannot load S3 config: %v", err)
	}

	s3Client := awss3.NewFromConfig(s3Cfg, func(o *awss3.Options) {
		o.BaseEndpoint = &s3Endpoint
		o.UsePathStyle = true
	})

	// Verify S3 is reachable.
	_, err = s3Client.HeadBucket(ctx, &awss3.HeadBucketInput{Bucket: &s3Bucket})
	if err != nil {
		b.Skipf("S3 bucket %q not reachable at %s: %v", s3Bucket, s3Endpoint, err)
	}

	tableName := fmt.Sprintf("factory_bench_%d", time.Now().UnixNano())

	s := storeddb.NewWithClients(ddbClient, s3Client, tableName, s3Bucket)
	if err := s.CreateTable(ctx); err != nil {
		b.Fatalf("CreateTable: %v", err)
	}

	// Wait for table to be active.
	waiter := dynamodb.NewTableExistsWaiter(ddbClient)
	if err := waiter.Wait(ctx, &dynamodb.DescribeTableInput{TableName: &tableName}, 30*time.Second); err != nil {
		b.Fatalf("wait for table: %v", err)
	}

	if err := s.EnsureQueue(ctx, "bench", store.QueueConfig{
		MaxConcurrency: 100000,
		MaxRetry:       5,
	}); err != nil {
		b.Fatalf("EnsureQueue: %v", err)
	}

	b.Cleanup(func() {
		ddbClient.DeleteTable(context.Background(), &dynamodb.DeleteTableInput{
			TableName: &tableName,
		})
	})

	return s
}

func BenchmarkEnqueue(b *testing.B) {
	s := setupBench(b)
	ctx := context.Background()

	b.ResetTimer()
	for i := range b.N {
		if err := s.Enqueue(ctx, "bench", fmt.Sprintf("enq-%08d", i), i%10); err != nil {
			b.Fatalf("Enqueue: %v", err)
		}
	}
}

func BenchmarkClaimBatch(b *testing.B) {
	s := setupBench(b)
	ctx := context.Background()

	const batchSize = 10
	for i := range b.N * batchSize {
		if err := s.Enqueue(ctx, "bench", fmt.Sprintf("cb-%08d", i), i%10); err != nil {
			b.Fatalf("Enqueue: %v", err)
		}
	}

	b.ResetTimer()
	for range b.N {
		items, err := s.ClaimBatch(ctx, "bench", batchSize, "w1", 5*time.Minute)
		if err != nil {
			b.Fatalf("ClaimBatch: %v", err)
		}
		for _, item := range items {
			s.Complete(ctx, "bench", item.Key)
		}
	}
}

func BenchmarkComplete(b *testing.B) {
	s := setupBench(b)
	ctx := context.Background()

	for i := range b.N {
		s.Enqueue(ctx, "bench", fmt.Sprintf("cmp-%08d", i), 0)
	}
	s.ClaimBatch(ctx, "bench", b.N, "w1", time.Hour)

	b.ResetTimer()
	for i := range b.N {
		s.Complete(ctx, "bench", fmt.Sprintf("cmp-%08d", i))
	}
}

func BenchmarkItemLifecycle(b *testing.B) {
	s := setupBench(b)
	ctx := context.Background()

	b.ResetTimer()
	for i := range b.N {
		key := fmt.Sprintf("bench-%08d", i)
		if err := s.Enqueue(ctx, "bench", key, i%10); err != nil {
			b.Fatalf("Enqueue: %v", err)
		}
		items, err := s.ClaimBatch(ctx, "bench", 1, "w1", 5*time.Minute)
		if err != nil || len(items) == 0 {
			b.Fatalf("ClaimBatch: err=%v items=%d", err, len(items))
		}
		if err := s.Transition(ctx, "bench", key, store.StatusClaimed, store.StatusRunning); err != nil {
			b.Fatalf("Transition: %v", err)
		}
		if err := s.Complete(ctx, "bench", key); err != nil {
			b.Fatalf("Complete: %v", err)
		}
	}
}

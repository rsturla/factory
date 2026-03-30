package dynamodb_test

import (
	"context"
	"fmt"
	"os"
	"testing"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/hummingbird-org/factory/internal/store"
	"github.com/hummingbird-org/factory/internal/store/conformance"
	storeddb "github.com/hummingbird-org/factory/internal/store/dynamodb"
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
			ComputeBackend: "kubernetes",
		}); err != nil {
			t.Fatalf("EnsureQueue: %v", err)
		}
		return s
	})
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

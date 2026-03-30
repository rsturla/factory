package s3_test

import (
	"context"
	"os"
	"testing"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/hummingbird-org/factory/internal/store"
	"github.com/hummingbird-org/factory/internal/store/conformance"
	stores3 "github.com/hummingbird-org/factory/internal/store/s3"
)

func TestS3Conformance(t *testing.T) {
	endpoint := os.Getenv("S3_TEST_ENDPOINT")
	if endpoint == "" {
		endpoint = "http://localhost:9000"
	}
	bucket := os.Getenv("S3_TEST_BUCKET")
	if bucket == "" {
		bucket = "factory-test"
	}

	// Verify connectivity before running tests.
	ctx := context.Background()
	cfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion("us-east-1"),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(
			"rustfsadmin", "rustfsadmin", "",
		)),
	)
	if err != nil {
		t.Skipf("cannot load AWS config: %v", err)
	}

	client := awss3.NewFromConfig(cfg, func(o *awss3.Options) {
		o.BaseEndpoint = &endpoint
		o.UsePathStyle = true
	})

	_, err = client.HeadBucket(ctx, &awss3.HeadBucketInput{Bucket: &bucket})
	if err != nil {
		t.Skipf("S3 bucket %q not reachable at %s: %v", bucket, endpoint, err)
	}

	conformance.Run(t, func(t *testing.T) store.Interface {
		// Clean the bucket before each test.
		cleanBucket(t, client, bucket)

		s := stores3.NewWithClient(client, bucket)
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

func cleanBucket(t *testing.T, client *awss3.Client, bucket string) {
	t.Helper()
	ctx := context.Background()

	paginator := awss3.NewListObjectsV2Paginator(client, &awss3.ListObjectsV2Input{
		Bucket: &bucket,
	})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			t.Fatalf("list objects for cleanup: %v", err)
		}
		for _, obj := range page.Contents {
			client.DeleteObject(ctx, &awss3.DeleteObjectInput{
				Bucket: &bucket,
				Key:    obj.Key,
			})
		}
	}
}

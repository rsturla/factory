package artifact

import (
	"bytes"
	"context"
	"fmt"
	"io"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// S3Store implements Store using AWS S3 or compatible services (MinIO).
type S3Store struct {
	client *s3.Client
	bucket string
}

// NewS3Store creates S3-backed artifact store.
func NewS3Store(ctx context.Context, cfg Config) (*S3Store, error) {
	// Build AWS config
	var opts []func(*config.LoadOptions) error

	// Custom endpoint for MinIO/non-AWS S3
	if cfg.Endpoint != "" {
		opts = append(opts, config.WithEndpointResolverWithOptions(
			aws.EndpointResolverWithOptionsFunc(
				func(service, region string, options ...interface{}) (aws.Endpoint, error) {
					return aws.Endpoint{
						URL:               cfg.Endpoint,
						HostnameImmutable: true,
					}, nil
				},
			),
		))
	}

	// Region
	if cfg.Region != "" {
		opts = append(opts, config.WithRegion(cfg.Region))
	}

	// Credentials
	if cfg.AccessKey != "" && cfg.SecretKey != "" {
		opts = append(opts, config.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(cfg.AccessKey, cfg.SecretKey, ""),
		))
	}

	awsCfg, err := config.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("load AWS config: %w", err)
	}

	return &S3Store{
		client: s3.NewFromConfig(awsCfg),
		bucket: cfg.Bucket,
	}, nil
}

// Upload stores artifact in S3 and returns URL.
func (s *S3Store) Upload(ctx context.Context, stageID string, data io.Reader) (string, error) {
	key := fmt.Sprintf("artifacts/%s/output.tar.gz", stageID)

	// Read data into buffer (needed for PutObject)
	buf := new(bytes.Buffer)
	n, err := io.Copy(buf, data)
	if err != nil {
		return "", fmt.Errorf("read data: %w", err)
	}

	// Upload to S3
	_, err = s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:        aws.String(s.bucket),
		Key:           aws.String(key),
		Body:          bytes.NewReader(buf.Bytes()),
		ContentLength: aws.Int64(n),
		ContentType:   aws.String("application/gzip"),
	})
	if err != nil {
		return "", fmt.Errorf("upload to S3: %w", err)
	}

	// Return S3 URL
	url := fmt.Sprintf("s3://%s/%s", s.bucket, key)
	return url, nil
}

// Download retrieves artifact from S3.
func (s *S3Store) Download(ctx context.Context, url string) (io.ReadCloser, error) {
	// Parse s3://bucket/key URL
	bucket, key, err := parseS3URL(url)
	if err != nil {
		return nil, err
	}

	// Get object from S3
	result, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, fmt.Errorf("download from S3: %w", err)
	}

	return result.Body, nil
}

// Delete removes artifact from S3.
func (s *S3Store) Delete(ctx context.Context, url string) error {
	bucket, key, err := parseS3URL(url)
	if err != nil {
		return err
	}

	_, err = s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return fmt.Errorf("delete from S3: %w", err)
	}

	return nil
}

// GetSize returns artifact size.
func (s *S3Store) GetSize(ctx context.Context, url string) (int64, error) {
	bucket, key, err := parseS3URL(url)
	if err != nil {
		return 0, err
	}

	result, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return 0, fmt.Errorf("head S3 object: %w", err)
	}

	return aws.ToInt64(result.ContentLength), nil
}

// parseS3URL extracts bucket and key from s3://bucket/key URL.
func parseS3URL(url string) (bucket, key string, err error) {
	if len(url) < 5 || url[:5] != "s3://" {
		return "", "", fmt.Errorf("invalid S3 URL format: %s", url)
	}

	path := url[5:] // Remove "s3://"

	// Find first slash
	slashIdx := -1
	for i, c := range path {
		if c == '/' {
			slashIdx = i
			break
		}
	}

	if slashIdx == -1 {
		return "", "", fmt.Errorf("invalid S3 URL format (no key): %s", url)
	}

	bucket = path[:slashIdx]
	key = path[slashIdx+1:]

	if bucket == "" || key == "" {
		return "", "", fmt.Errorf("invalid S3 URL format (empty bucket or key): %s", url)
	}

	return bucket, key, nil
}

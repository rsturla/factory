package blob

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
)

// S3Store implements Store on an S3-compatible backend.
type S3Store struct {
	client *s3.Client
	bucket string
	prefix string
}

type S3Config struct {
	Bucket   string
	Endpoint string
	Region   string
	Prefix   string
}

func NewS3Store(ctx context.Context, cfg S3Config) (*S3Store, error) {
	if cfg.Bucket == "" {
		return nil, fmt.Errorf("blob/s3: bucket name is required")
	}
	awsCfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(cfg.Region))
	if err != nil {
		return nil, fmt.Errorf("blob/s3: load config: %w", err)
	}

	var clientOpts []func(*s3.Options)
	if cfg.Endpoint != "" {
		clientOpts = append(clientOpts, func(o *s3.Options) {
			o.BaseEndpoint = aws.String(cfg.Endpoint)
			o.UsePathStyle = true
		})
	}

	return &S3Store{
		client: s3.NewFromConfig(awsCfg, clientOpts...),
		bucket: cfg.Bucket,
		prefix: strings.TrimSuffix(cfg.Prefix, "/"),
	}, nil
}

func (s *S3Store) fullKey(key string) string {
	if s.prefix == "" {
		return key
	}
	return s.prefix + "/" + key
}

const maxBlobSize = 256 << 20 // 256 MiB

func (s *S3Store) validateKey(key string) error {
	if key == "" {
		return fmt.Errorf("blob/s3: empty key")
	}
	if strings.ContainsRune(key, 0) {
		return fmt.Errorf("blob/s3: null byte in key")
	}
	for _, seg := range strings.Split(key, "/") {
		if seg == ".." {
			return fmt.Errorf("blob/s3: path traversal in key %q", key)
		}
	}
	if strings.HasPrefix(key, "/") {
		return fmt.Errorf("blob/s3: absolute key %q", key)
	}
	return nil
}

func (s *S3Store) Put(ctx context.Context, key string, data []byte) error {
	if err := s.validateKey(key); err != nil {
		return err
	}
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(s.fullKey(key)),
		Body:   bytes.NewReader(data),
	})
	if err != nil {
		return fmt.Errorf("blob/s3: put %s: %w", key, err)
	}
	return nil
}

func (s *S3Store) Get(ctx context.Context, key string) ([]byte, error) {
	if err := s.validateKey(key); err != nil {
		return nil, err
	}
	resp, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(s.fullKey(key)),
	})
	if err != nil {
		if isS3NotFound(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("blob/s3: get %s: %w", key, err)
	}
	defer resp.Body.Close()
	data, readErr := io.ReadAll(io.LimitReader(resp.Body, maxBlobSize))
	if readErr != nil {
		return nil, fmt.Errorf("blob/s3: read body %s: %w", key, readErr)
	}
	return data, nil
}

func (s *S3Store) Exists(ctx context.Context, key string) (bool, error) {
	if err := s.validateKey(key); err != nil {
		return false, err
	}
	_, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(s.fullKey(key)),
	})
	if err != nil {
		if isS3NotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("blob/s3: exists %s: %w", key, err)
	}
	return true, nil
}

func isS3NotFound(err error) bool {
	var nsk *types.NoSuchKey
	if errors.As(err, &nsk) {
		return true
	}
	var notFound *types.NotFound
	if errors.As(err, &notFound) {
		return true
	}
	// HeadObject returns 404 as a smithy API error, not a typed error.
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) && apiErr.ErrorCode() == "NotFound" {
		return true
	}
	return false
}

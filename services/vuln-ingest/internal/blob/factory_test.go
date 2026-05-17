package blob

import (
	"context"
	"testing"
)

func TestNew_LocalDefault(t *testing.T) {
	cfg := Config{
		Backend:  "local",
		LocalDir: t.TempDir(),
	}
	s, err := New(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s.(*LocalStore); !ok {
		t.Fatalf("expected *LocalStore, got %T", s)
	}
}

func TestNew_EmptyBackendDefaultsToLocal(t *testing.T) {
	cfg := Config{
		Backend:  "",
		LocalDir: t.TempDir(),
	}
	s, err := New(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s.(*LocalStore); !ok {
		t.Fatalf("expected *LocalStore, got %T", s)
	}
}

func TestNew_UnknownBackend(t *testing.T) {
	cfg := Config{Backend: "redis"}
	_, err := New(context.Background(), cfg)
	if err == nil {
		t.Fatal("expected error for unknown backend")
	}
}

func TestConfigFromEnv_Defaults(t *testing.T) {
	// Unset all blob env vars to test defaults.
	for _, k := range []string{"BLOB_BACKEND", "BLOB_LOCAL_DIR", "DATA_DIR", "BLOB_BUCKET", "BLOB_ENDPOINT", "BLOB_REGION", "BLOB_PREFIX"} {
		t.Setenv(k, "")
	}

	cfg := ConfigFromEnv()
	if cfg.Backend != "local" {
		t.Errorf("backend: got %q, want 'local'", cfg.Backend)
	}
	if cfg.LocalDir != "/data" {
		t.Errorf("local dir: got %q, want /data", cfg.LocalDir)
	}
	if cfg.S3.Region != "us-east-1" {
		t.Errorf("region: got %q, want us-east-1", cfg.S3.Region)
	}
}

func TestConfigFromEnv_DataDirFallback(t *testing.T) {
	t.Setenv("BLOB_LOCAL_DIR", "")
	t.Setenv("DATA_DIR", "/custom/data")
	t.Setenv("BLOB_BACKEND", "")

	cfg := ConfigFromEnv()
	if cfg.LocalDir != "/custom/data" {
		t.Errorf("local dir: got %q, want /custom/data", cfg.LocalDir)
	}
}

func TestConfigFromEnv_S3(t *testing.T) {
	t.Setenv("BLOB_BACKEND", "s3")
	t.Setenv("BLOB_BUCKET", "my-bucket")
	t.Setenv("BLOB_ENDPOINT", "http://minio:9000")
	t.Setenv("BLOB_REGION", "eu-west-1")
	t.Setenv("BLOB_PREFIX", "vulns/")

	cfg := ConfigFromEnv()
	if cfg.Backend != "s3" {
		t.Errorf("backend: got %q", cfg.Backend)
	}
	if cfg.S3.Bucket != "my-bucket" {
		t.Errorf("bucket: got %q", cfg.S3.Bucket)
	}
	if cfg.S3.Endpoint != "http://minio:9000" {
		t.Errorf("endpoint: got %q", cfg.S3.Endpoint)
	}
	if cfg.S3.Region != "eu-west-1" {
		t.Errorf("region: got %q", cfg.S3.Region)
	}
	if cfg.S3.Prefix != "vulns/" {
		t.Errorf("prefix: got %q", cfg.S3.Prefix)
	}
}

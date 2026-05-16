package artifact

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// LocalStore implements Store using local filesystem.
// For development and testing without S3.
type LocalStore struct {
	baseDir string
}

// NewLocalStore creates filesystem-backed artifact store.
func NewLocalStore(baseDir string) (*LocalStore, error) {
	// Create base directory if doesn't exist
	if err := os.MkdirAll(baseDir, 0755); err != nil {
		return nil, fmt.Errorf("create base directory: %w", err)
	}

	return &LocalStore{
		baseDir: baseDir,
	}, nil
}

// Upload stores artifact on local filesystem and returns file:// URL.
func (s *LocalStore) Upload(ctx context.Context, stageID string, data io.Reader) (string, error) {
	// Create artifact directory
	artifactDir := filepath.Join(s.baseDir, "artifacts", stageID)
	if err := os.MkdirAll(artifactDir, 0755); err != nil {
		return "", fmt.Errorf("create artifact directory: %w", err)
	}

	// Write to file
	artifactPath := filepath.Join(artifactDir, "output.tar.gz")
	f, err := os.Create(artifactPath)
	if err != nil {
		return "", fmt.Errorf("create artifact file: %w", err)
	}
	defer f.Close()

	// Copy data
	n, err := io.Copy(f, data)
	if err != nil {
		return "", fmt.Errorf("write artifact: %w", err)
	}

	// Return file:// URL
	url := fmt.Sprintf("file://%s", artifactPath)

	// Log size for debugging
	if n > 0 {
		// Size logged by caller
	}

	return url, nil
}

// Download retrieves artifact from local filesystem.
func (s *LocalStore) Download(ctx context.Context, url string) (io.ReadCloser, error) {
	// Parse file:// URL
	path, err := parseFileURL(url)
	if err != nil {
		return nil, err
	}

	// Open file
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open artifact file: %w", err)
	}

	return f, nil
}

// Delete removes artifact from filesystem.
func (s *LocalStore) Delete(ctx context.Context, url string) error {
	path, err := parseFileURL(url)
	if err != nil {
		return err
	}

	if err := os.Remove(path); err != nil {
		return fmt.Errorf("delete artifact: %w", err)
	}

	return nil
}

// GetSize returns artifact size.
func (s *LocalStore) GetSize(ctx context.Context, url string) (int64, error) {
	path, err := parseFileURL(url)
	if err != nil {
		return 0, err
	}

	info, err := os.Stat(path)
	if err != nil {
		return 0, fmt.Errorf("stat artifact: %w", err)
	}

	return info.Size(), nil
}

// parseFileURL extracts path from file:// URL.
func parseFileURL(url string) (string, error) {
	if len(url) < 7 || url[:7] != "file://" {
		return "", fmt.Errorf("invalid file URL format: %s", url)
	}

	return url[7:], nil
}

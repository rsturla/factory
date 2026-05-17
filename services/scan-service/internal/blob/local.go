package blob

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// LocalStore implements Store on the local filesystem.
type LocalStore struct {
	baseDir string
}

func NewLocalStore(baseDir string) (*LocalStore, error) {
	abs, err := filepath.Abs(baseDir)
	if err != nil {
		return nil, fmt.Errorf("blob/local: resolve base dir: %w", err)
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return nil, fmt.Errorf("blob/local: create base dir: %w", err)
	}
	return &LocalStore{baseDir: abs}, nil
}

func (l *LocalStore) Put(_ context.Context, key string, data []byte) error {
	path, err := l.safePath(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("blob/local: mkdir: %w", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("blob/local: write %s: %w", key, err)
	}
	return nil
}

func (l *LocalStore) Get(_ context.Context, key string) ([]byte, error) {
	path, err := l.safePath(key)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("blob/local: read %s: %w", key, err)
	}
	return data, nil
}

func (l *LocalStore) Exists(_ context.Context, key string) (bool, error) {
	path, err := l.safePath(key)
	if err != nil {
		return false, err
	}
	_, err = os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("blob/local: stat %s: %w", key, err)
	}
	return true, nil
}

func (l *LocalStore) safePath(key string) (string, error) {
	if key == "" {
		return "", fmt.Errorf("blob/local: empty key")
	}
	if strings.ContainsRune(key, 0) {
		return "", fmt.Errorf("blob/local: null byte in key")
	}
	cleaned := filepath.FromSlash(key)
	full := filepath.Clean(filepath.Join(l.baseDir, cleaned))
	if full == l.baseDir {
		return "", fmt.Errorf("blob/local: key %q resolves to base directory", key)
	}
	if !strings.HasPrefix(full+string(os.PathSeparator), l.baseDir+string(os.PathSeparator)) {
		return "", fmt.Errorf("blob/local: path traversal in key %q", key)
	}
	return full, nil
}

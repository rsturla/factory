package blob_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hummingbird-org/vuln-ingest/internal/blob"
)

func FuzzLocalStore_SafePath(t *testing.F) {
	t.Add("nvd/CVE-2024-0001.json")
	t.Add("../../etc/passwd")
	t.Add("../escape")
	t.Add("foo/../../../bar")
	t.Add("/absolute/path")
	t.Add("normal/key.json")
	t.Add("")
	t.Add("a/b/c/d/e/f.json")
	t.Add(strings.Repeat("a/", 100) + "deep.json")
	t.Add("foo\x00bar")
	t.Add("foo\\..\\bar")

	t.Fuzz(func(t *testing.T, key string) {
		dir := t.TempDir()
		s, err := blob.NewLocalStore(dir)
		if err != nil {
			t.Skip()
		}
		ctx := context.Background()

		putErr := s.Put(ctx, key, []byte("test"))

		if putErr == nil {
			// If Put succeeded, the file must be inside baseDir.
			entries := collectFiles(t, dir)
			for _, path := range entries {
				abs, _ := filepath.Abs(path)
				absDir, _ := filepath.Abs(dir)
				if !strings.HasPrefix(abs, absDir+string(os.PathSeparator)) {
					t.Errorf("file %q escaped base dir %q", abs, absDir)
				}
			}

			// Get must succeed for anything we Put.
			_, getErr := s.Get(ctx, key)
			if getErr != nil {
				t.Errorf("Get after successful Put failed: %v", getErr)
			}
		}
	})
}

func collectFiles(t *testing.T, dir string) []string {
	t.Helper()
	var files []string
	filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if !info.IsDir() {
			files = append(files, path)
		}
		return nil
	})
	return files
}

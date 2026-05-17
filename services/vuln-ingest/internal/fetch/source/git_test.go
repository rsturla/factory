package source_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/hummingbird-org/vuln-ingest/internal/blob"
	"github.com/hummingbird-org/vuln-ingest/internal/fetch/source"
)

// initBareRepo creates a bare git repo and returns its path.
func initBareRepo(t *testing.T, dir string) string {
	t.Helper()
	bare := filepath.Join(dir, "repo.git")
	run(t, "git", "init", "--bare", bare)
	return bare
}

// cloneAndCommit clones bare, adds files, commits, returns the commit SHA.
func cloneAndCommit(t *testing.T, bare, workDir string, files map[string]string, msg string) string {
	t.Helper()
	run(t, "git", "clone", bare, workDir)
	run(t, "git", "-C", workDir, "config", "user.email", "test@test.com")
	run(t, "git", "-C", workDir, "config", "user.name", "Test")

	for name, content := range files {
		full := filepath.Join(workDir, name)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	run(t, "git", "-C", workDir, "add", "-A")
	run(t, "git", "-C", workDir, "commit", "-m", msg)
	run(t, "git", "-C", workDir, "push", "origin", "main")

	out, err := exec.Command("git", "-C", workDir, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	return trimOutput(out)
}

// addAndPush adds files to an existing clone, commits, pushes, returns SHA.
func addAndPush(t *testing.T, workDir string, files map[string]string, msg string) string {
	t.Helper()
	for name, content := range files {
		full := filepath.Join(workDir, name)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run(t, "git", "-C", workDir, "add", "-A")
	run(t, "git", "-C", workDir, "commit", "-m", msg)
	run(t, "git", "-C", workDir, "push", "origin", "main")

	out, err := exec.Command("git", "-C", workDir, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	return trimOutput(out)
}

func run(t *testing.T, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("%s %v: %v", name, args, err)
	}
}

func trimOutput(b []byte) string {
	s := string(b)
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}

func testBlobs(t *testing.T) blob.Store {
	t.Helper()
	s, err := blob.NewLocalStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestGitSource_Bootstrap(t *testing.T) {
	dir := t.TempDir()
	bare := initBareRepo(t, dir)

	work := filepath.Join(dir, "work")
	cloneAndCommit(t, bare, work, map[string]string{
		"advisories/GHSA-0001.json": `{"id":"GHSA-0001"}`,
		"advisories/GHSA-0002.json": `{"id":"GHSA-0002"}`,
		"README.md":                 "# test",
	}, "initial")

	blobs := testBlobs(t)
	scratch := t.TempDir()
	gs := source.NewGitSource("testgh", bare, "advisories", "main", "*.json", scratch)

	result, err := gs.Fetch(context.Background(), blobs, "")
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	if result.ItemCount != 2 {
		t.Errorf("items: got %d, want 2", result.ItemCount)
	}
	if result.NewCheckpoint == "" {
		t.Error("checkpoint empty")
	}

	// Verify blobs written with correct content.
	wantContent := map[string]string{
		"testgh/advisories/GHSA-0001.json": `{"id":"GHSA-0001"}`,
		"testgh/advisories/GHSA-0002.json": `{"id":"GHSA-0002"}`,
	}
	for key, want := range wantContent {
		data, err := blobs.Get(context.Background(), key)
		if err != nil {
			t.Errorf("blob %q not found: %v", key, err)
			continue
		}
		if string(data) != want {
			t.Errorf("blob %q: got %q, want %q", key, data, want)
		}
	}

	// README should NOT be in blobs (filtered by *.json glob).
	_, err = blobs.Get(context.Background(), "testgh/README.md")
	if err == nil {
		t.Error("README.md should have been filtered out by glob")
	}
}

func TestGitSource_IncrementalDiff(t *testing.T) {
	dir := t.TempDir()
	bare := initBareRepo(t, dir)

	work := filepath.Join(dir, "work")
	sha1 := cloneAndCommit(t, bare, work, map[string]string{
		"data/CVE-0001.json": `{"id":"CVE-0001"}`,
	}, "first")

	blobs := testBlobs(t)
	scratch := t.TempDir()
	gs := source.NewGitSource("testrepo", bare, "data", "main", "*.json", scratch)

	// First fetch — bootstrap.
	result1, err := gs.Fetch(context.Background(), blobs, "")
	if err != nil {
		t.Fatalf("first fetch: %v", err)
	}
	if result1.NewCheckpoint != sha1 {
		t.Errorf("checkpoint: got %q, want %q", result1.NewCheckpoint, sha1)
	}

	// Add a second file and push.
	sha2 := addAndPush(t, work, map[string]string{
		"data/CVE-0002.json": `{"id":"CVE-0002"}`,
	}, "second")

	// Second fetch — incremental.
	result2, err := gs.Fetch(context.Background(), blobs, sha1)
	if err != nil {
		t.Fatalf("incremental fetch: %v", err)
	}

	if result2.NewCheckpoint != sha2 {
		t.Errorf("checkpoint: got %q, want %q", result2.NewCheckpoint, sha2)
	}
	if result2.ItemCount != 1 {
		t.Errorf("items: got %d, want 1 (only new file)", result2.ItemCount)
	}

	// Verify new blob written.
	data, err := blobs.Get(context.Background(), "testrepo/data/CVE-0002.json")
	if err != nil {
		t.Fatalf("new blob not found: %v", err)
	}
	if string(data) != `{"id":"CVE-0002"}` {
		t.Errorf("blob content: got %q", data)
	}
}

func TestGitSource_NoChanges(t *testing.T) {
	dir := t.TempDir()
	bare := initBareRepo(t, dir)

	work := filepath.Join(dir, "work")
	sha := cloneAndCommit(t, bare, work, map[string]string{
		"a.json": `{"id":"A"}`,
	}, "only commit")

	blobs := testBlobs(t)
	scratch := t.TempDir()
	gs := source.NewGitSource("nochg", bare, ".", "main", "*.json", scratch)

	// First fetch clones.
	_, err := gs.Fetch(context.Background(), blobs, "")
	if err != nil {
		t.Fatal(err)
	}

	// Second fetch with same checkpoint — no changes.
	result, err := gs.Fetch(context.Background(), blobs, sha)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.ChangedFiles) != 0 {
		t.Errorf("expected 0 changes, got %d", len(result.ChangedFiles))
	}
	if result.NewCheckpoint != sha {
		t.Errorf("checkpoint should be unchanged")
	}
}

func TestGitSource_GlobFiltering(t *testing.T) {
	dir := t.TempDir()
	bare := initBareRepo(t, dir)

	work := filepath.Join(dir, "work")
	cloneAndCommit(t, bare, work, map[string]string{
		"vulns/CVE-001.yaml": "id: CVE-001",
		"vulns/CVE-002.yaml": "id: CVE-002",
		"vulns/README.md":    "# readme",
		"vulns/data.txt":     "data",
	}, "mixed files")

	blobs := testBlobs(t)
	scratch := t.TempDir()
	gs := source.NewGitSource("yamlsrc", bare, "vulns", "main", "*.yaml", scratch)

	result, err := gs.Fetch(context.Background(), blobs, "")
	if err != nil {
		t.Fatal(err)
	}
	if result.ItemCount != 2 {
		t.Errorf("expected 2 yaml files, got %d: %v", result.ItemCount, result.ChangedFiles)
	}
}

func TestGitSource_SubdirFiltering(t *testing.T) {
	dir := t.TempDir()
	bare := initBareRepo(t, dir)

	work := filepath.Join(dir, "work")
	cloneAndCommit(t, bare, work, map[string]string{
		"advisories/a.json": `{"id":"A"}`,
		"other/b.json":      `{"id":"B"}`,
		"root.json":         `{"id":"ROOT"}`,
	}, "subdir test")

	blobs := testBlobs(t)
	scratch := t.TempDir()
	gs := source.NewGitSource("subdir", bare, "advisories", "main", "*.json", scratch)

	result, err := gs.Fetch(context.Background(), blobs, "")
	if err != nil {
		t.Fatal(err)
	}
	if result.ItemCount != 1 {
		t.Errorf("expected 1 file from advisories/, got %d: %v", result.ItemCount, result.ChangedFiles)
	}
}

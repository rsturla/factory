package source

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/hummingbird-org/vuln-ingest/internal/blob"
)

// GitSource handles git-based vulnerability feeds.
// Parameterized to cover cvelistV5, GHSA, RUSTSEC, govuln, PyPA, PSF.
type GitSource struct {
	name       string
	subDir     string
	fileGlob   string
	scratchDir string
}

func NewGitSource(name, subDir, fileGlob, scratchDir string) *GitSource {
	return &GitSource{
		name:       name,
		subDir:     subDir,
		fileGlob:   fileGlob,
		scratchDir: scratchDir,
	}
}

func (g *GitSource) Name() string { return g.name }

func (g *GitSource) Fetch(ctx context.Context, blobs blob.Store, checkpoint string) (FetchResult, error) {
	repoDir := filepath.Join(g.scratchDir, g.name)
	log := slog.With("source", g.name)

	if _, err := os.Stat(filepath.Join(repoDir, ".git")); os.IsNotExist(err) {
		return FetchResult{}, fmt.Errorf("repo not found at %s (git-mirror must sync first)", repoDir)
	}

	head, err := gitOutput(ctx, repoDir, "rev-parse", "HEAD")
	if err != nil {
		return FetchResult{}, fmt.Errorf("rev-parse HEAD: %w", err)
	}

	if head == checkpoint {
		log.Info("no changes")
		return FetchResult{NewCheckpoint: head}, nil
	}

	var changedFiles []string

	if checkpoint == "" {
		changedFiles, err = g.listAllFiles(ctx, repoDir)
	} else {
		changedFiles, err = g.diffFiles(ctx, repoDir, checkpoint, head)
	}
	if err != nil {
		return FetchResult{}, err
	}

	var keys []string
	for _, f := range changedFiles {
		filePath := filepath.Join(repoDir, f)

		// Reject symlinks to prevent reading files outside the repo.
		fi, statErr := os.Lstat(filePath)
		if statErr != nil {
			log.Warn("stat changed file failed, skipping", "file", f, "error", statErr)
			continue
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			log.Warn("skipping symlink", "file", f)
			continue
		}

		data, readErr := os.ReadFile(filePath)
		if readErr != nil {
			log.Warn("read changed file failed, skipping", "file", f, "error", readErr)
			continue
		}

		key := g.name + "/" + f
		if putErr := blobs.Put(ctx, key, data); putErr != nil {
			return FetchResult{}, fmt.Errorf("put %s: %w", key, putErr)
		}
		keys = append(keys, key)
	}

	shortHead := head
	if len(shortHead) > 12 {
		shortHead = shortHead[:12]
	}
	log.Info("fetched changes", "count", len(keys), "checkpoint", shortHead)

	return FetchResult{
		ChangedFiles:  keys,
		NewCheckpoint: head,
		ItemCount:     len(keys),
	}, nil
}

func (g *GitSource) listAllFiles(ctx context.Context, repoDir string) ([]string, error) {
	args := []string{"-C", repoDir, "ls-files"}
	if g.subDir != "." && g.subDir != "" {
		args = append(args, g.subDir)
	}

	out, err := gitOutput(ctx, repoDir, args[2:]...)
	if err != nil {
		return nil, fmt.Errorf("ls-files: %w", err)
	}

	return filterByGlob(strings.Split(strings.TrimSpace(out), "\n"), g.fileGlob), nil
}

func (g *GitSource) diffFiles(ctx context.Context, repoDir, from, to string) ([]string, error) {
	args := []string{"diff", "--name-only", "--diff-filter=ACMR", from + ".." + to}
	if g.subDir != "." && g.subDir != "" {
		args = append(args, "--", g.subDir)
	}

	out, err := gitOutput(ctx, repoDir, args...)
	if err != nil {
		// If the old commit was garbage-collected (shallow clone), fall back to full list.
		slog.Warn("git diff failed, falling back to full list", "error", err)
		return g.listAllFiles(ctx, repoDir)
	}

	if strings.TrimSpace(out) == "" {
		return nil, nil
	}

	return filterByGlob(strings.Split(strings.TrimSpace(out), "\n"), g.fileGlob), nil
}

func gitOutput(ctx context.Context, repoDir string, args ...string) (string, error) {
	fullArgs := append([]string{"-C", repoDir}, args...)
	cmd := exec.CommandContext(ctx, "git", fullArgs...)
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func filterByGlob(files []string, glob string) []string {
	if glob == "" || glob == "*" {
		return files
	}

	var result []string
	for _, f := range files {
		if f == "" {
			continue
		}
		matched, _ := filepath.Match(glob, filepath.Base(f))
		if matched {
			result = append(result, f)
		}
	}
	return result
}

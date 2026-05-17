package source

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// GitSource handles git-based vulnerability feeds.
// Parameterized to cover cvelistV5, GHSA, RUSTSEC, govuln, PyPA, PSF.
type GitSource struct {
	name     string
	repoURL  string
	subDir   string
	branch   string
	fileGlob string
}

func NewGitSource(name, repoURL, subDir, branch, fileGlob string) *GitSource {
	return &GitSource{
		name:     name,
		repoURL:  repoURL,
		subDir:   subDir,
		branch:   branch,
		fileGlob: fileGlob,
	}
}

func (g *GitSource) Name() string { return g.name }

func (g *GitSource) Fetch(ctx context.Context, dataDir string, checkpoint string) (FetchResult, error) {
	repoDir := filepath.Join(dataDir, g.name)
	log := slog.With("source", g.name, "repo", g.repoURL)

	if _, err := os.Stat(filepath.Join(repoDir, ".git")); os.IsNotExist(err) {
		log.Info("cloning repository")
		if err := g.clone(ctx, repoDir); err != nil {
			return FetchResult{}, fmt.Errorf("clone: %w", err)
		}
	} else {
		log.Info("pulling repository")
		if err := g.pull(ctx, repoDir); err != nil {
			return FetchResult{}, fmt.Errorf("pull: %w", err)
		}
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
		// First sync: list all matching files.
		changedFiles, err = g.listAllFiles(ctx, repoDir)
	} else {
		// Diff since last checkpoint.
		changedFiles, err = g.diffFiles(ctx, repoDir, checkpoint, head)
	}
	if err != nil {
		return FetchResult{}, err
	}

	// Prefix with source name for resolve queue key format.
	var keys []string
	for _, f := range changedFiles {
		keys = append(keys, g.name+"/"+f)
	}

	log.Info("fetched changes", "count", len(keys), "checkpoint", head[:12])

	return FetchResult{
		ChangedFiles:  keys,
		NewCheckpoint: head,
		ItemCount:     len(keys),
	}, nil
}

func (g *GitSource) clone(ctx context.Context, dir string) error {
	args := []string{"clone", "--depth=1", "--single-branch", "--branch", g.branch, g.repoURL, dir}
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func (g *GitSource) pull(ctx context.Context, dir string) error {
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "pull", "--ff-only")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
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

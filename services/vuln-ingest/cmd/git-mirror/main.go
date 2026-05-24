package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

type repo struct {
	name   string
	url    string
	branch string
}

var defaultRepos = []repo{
	{"cvelistv5", "https://github.com/CVEProject/cvelistV5.git", "main"},
	{"ghsa", "https://github.com/github/advisory-database.git", "main"},
	{"rustsec", "https://github.com/rustsec/advisory-db.git", "osv"},
	{"govuln", "https://github.com/golang/vulndb.git", "master"},
	{"pypa", "https://github.com/pypa/advisory-database.git", "main"},
	{"psf", "https://github.com/psf/advisory-database.git", "main"},
	{"kernel", "https://git.kernel.org/pub/scm/linux/security/vulns.git", "master"},
	{"anchore-nvd-overrides", "https://github.com/anchore/nvd-data-overrides.git", "main"},
	{"vendor-notes-debian", "https://salsa.debian.org/security-tracker-team/security-tracker.git", "master"},
}

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	dataDir := envOr("DATA_DIR", "/data")
	timeout := 10 * time.Minute

	syncCtx, syncCancel := context.WithTimeout(ctx, timeout)
	defer syncCancel()

	var errors int
	for _, r := range defaultRepos {
		if err := syncRepo(syncCtx, dataDir, r); err != nil {
			slog.Error("sync failed", "repo", r.name, "error", err)
			errors++
		}
	}

	if errors > 0 {
		slog.Error("git mirror completed with errors", "failed", errors, "total", len(defaultRepos))
		os.Exit(1)
	}

	slog.Info("git mirror completed", "repos", len(defaultRepos))
}

func syncRepo(ctx context.Context, dataDir string, r repo) error {
	repoDir := filepath.Join(dataDir, r.name)
	log := slog.With("repo", r.name)

	if _, err := os.Stat(filepath.Join(repoDir, ".git")); os.IsNotExist(err) {
		log.Info("cloning")
		return cloneRepo(ctx, r.url, r.branch, repoDir)
	}

	log.Info("pulling")
	return pullRepo(ctx, repoDir)
}

func cloneRepo(ctx context.Context, url, branch, dir string) error {
	cmd := exec.CommandContext(ctx, "git", "clone", "--depth=1", "--single-branch", "--branch", branch, url, dir)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("clone %s: %w", url, err)
	}
	return nil
}

func pullRepo(ctx context.Context, dir string) error {
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "pull", "--ff-only")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("pull %s: %w", dir, err)
	}
	return nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

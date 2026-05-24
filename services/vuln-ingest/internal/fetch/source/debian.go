package source

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/hummingbird-org/vuln-ingest/internal/blob"
)

// DebianSource clones the Debian Security Tracker git repo and parses the
// plaintext data/CVE/list file. This file contains NOTE lines with upstream
// fix commit URLs, advisory references, and per-release package status — data
// not available from the JSON API.
type DebianSource struct {
	index         VendorNoteIndex
	gitScratchDir string
}

func NewDebianSource(idx VendorNoteIndex, gitScratchDir string) *DebianSource {
	return &DebianSource{
		index:         idx,
		gitScratchDir: gitScratchDir,
	}
}

func (d *DebianSource) Name() string { return "vendor-notes-debian" }

const (
	debianRepoURL = "https://salsa.debian.org/security-tracker-team/security-tracker.git"
	debianCVEList = "data/CVE/list"
	debianBranch  = "master"
	debianHashKey = "vendor-notes-debian/content-hashes.json"
)

func (d *DebianSource) Fetch(ctx context.Context, blobs blob.Store, checkpoint string) (FetchResult, error) {
	log := slog.With("source", "vendor-notes-debian")
	repoDir := filepath.Join(d.gitScratchDir, "vendor-notes-debian")

	if _, err := os.Stat(filepath.Join(repoDir, ".git")); os.IsNotExist(err) {
		log.Info("cloning debian security tracker")
		if err := d.clone(ctx, repoDir); err != nil {
			return FetchResult{}, fmt.Errorf("clone: %w", err)
		}
	} else {
		log.Info("pulling debian security tracker")
		if err := d.pull(ctx, repoDir); err != nil {
			return FetchResult{}, fmt.Errorf("pull: %w", err)
		}
	}

	head, err := d.gitOutput(ctx, repoDir, "rev-parse", "HEAD")
	if err != nil {
		return FetchResult{}, fmt.Errorf("rev-parse HEAD: %w", err)
	}

	if head == checkpoint {
		log.Info("no changes")
		return FetchResult{NewCheckpoint: head}, nil
	}

	listPath := filepath.Join(repoDir, debianCVEList)
	entries, err := parseDebianCVEList(listPath)
	if err != nil {
		return FetchResult{}, fmt.Errorf("parse CVE list: %w", err)
	}

	prevHashes := d.loadContentHashes(ctx, blobs)

	var changed []vendorNoteBatchEntry
	newHashes := make(map[string]string, len(entries))

	for cveID, content := range entries {
		contentJSON, err := json.Marshal(content)
		if err != nil {
			return FetchResult{}, fmt.Errorf("marshal debian content for %s: %w", cveID, err)
		}

		hash := fmt.Sprintf("%x", sha256.Sum256(contentJSON))
		newHashes[cveID] = hash

		if prevHashes[cveID] != hash {
			changed = append(changed, vendorNoteBatchEntry{CVEID: cveID, Content: contentJSON})
		}
	}

	if err := d.saveContentHashes(ctx, blobs, newHashes); err != nil {
		log.Warn("failed to save content hashes", "error", err)
	}

	if len(changed) == 0 {
		log.Info("no debian note changes")
		return FetchResult{NewCheckpoint: head}, nil
	}

	keys, err := writeChunkedBatches(ctx, blobs, "debian", changed, "vendor-notes-debian")
	if err != nil {
		return FetchResult{}, err
	}

	log.Info("debian vendor notes fetched", "changed", len(changed), "total", len(entries))
	return FetchResult{
		ChangedFiles:  keys,
		NewCheckpoint: head,
		ItemCount:     len(changed),
	}, nil
}

func (d *DebianSource) loadContentHashes(ctx context.Context, blobs blob.Store) map[string]string {
	data, err := blobs.Get(ctx, debianHashKey)
	if err != nil {
		return nil
	}
	var hashes map[string]string
	if err := json.Unmarshal(data, &hashes); err != nil {
		return nil
	}
	return hashes
}

func (d *DebianSource) saveContentHashes(ctx context.Context, blobs blob.Store, hashes map[string]string) error {
	data, err := json.Marshal(hashes)
	if err != nil {
		return err
	}
	return blobs.Put(ctx, debianHashKey, data)
}

func (d *DebianSource) clone(ctx context.Context, dir string) error {
	args := []string{"clone", "--depth=1", "--single-branch", "--branch", debianBranch, debianRepoURL, dir}
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func (d *DebianSource) pull(ctx context.Context, dir string) error {
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "pull", "--ff-only")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func (d *DebianSource) gitOutput(ctx context.Context, repoDir string, args ...string) (string, error) {
	fullArgs := append([]string{"-C", repoDir}, args...)
	cmd := exec.CommandContext(ctx, "git", fullArgs...)
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

type debianCVEContent struct {
	Description string                       `json:"description,omitempty"`
	Notes       []string                     `json:"notes,omitempty"`
	Advisories  []string                     `json:"advisories,omitempty"`
	Packages    map[string][]debianPkgStatus `json:"packages,omitempty"`
}

type debianPkgStatus struct {
	Release string `json:"release,omitempty"`
	Version string `json:"version,omitempty"`
	Status  string `json:"status,omitempty"`
	Note    string `json:"note,omitempty"`
}

var (
	cveLineRe     = regexp.MustCompile(`^(CVE-\d{4}-\d+)\s*(.*)`)
	noteLineRe    = regexp.MustCompile(`^\s+NOTE:\s*(.+)`)
	advisoryRe    = regexp.MustCompile(`^\s+\{((?:DSA|DLA)-\d+-\d+)\}`)
	unstablePkgRe = regexp.MustCompile(`^\s+-\s+(\S+)\s+(.*)`)
	releasePkgRe  = regexp.MustCompile(`^\s+\[(\S+)\]\s+-\s+(\S+)\s+(.*)`)
	statusTagRe   = regexp.MustCompile(`<([^>]+)>`)
)

func parseDebianCVEList(path string) (map[string]*debianCVEContent, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	result := make(map[string]*debianCVEContent)
	var currentCVE string
	var current *debianCVEContent

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 1<<20), 1<<20)

	for scanner.Scan() {
		line := scanner.Text()

		if m := cveLineRe.FindStringSubmatch(line); m != nil {
			currentCVE = m[1]
			desc := strings.TrimSpace(m[2])
			desc = strings.TrimPrefix(desc, "(")
			desc = strings.TrimSuffix(desc, ")")
			current = &debianCVEContent{
				Description: desc,
				Packages:    make(map[string][]debianPkgStatus),
			}
			result[currentCVE] = current
			continue
		}

		if current == nil {
			continue
		}

		if !strings.HasPrefix(line, "\t") && !strings.HasPrefix(line, "  ") {
			current = nil
			currentCVE = ""
			continue
		}

		if m := noteLineRe.FindStringSubmatch(line); m != nil {
			current.Notes = append(current.Notes, strings.TrimSpace(m[1]))
			continue
		}

		if m := advisoryRe.FindStringSubmatch(line); m != nil {
			current.Advisories = append(current.Advisories, m[1])
			continue
		}

		if m := releasePkgRe.FindStringSubmatch(line); m != nil {
			release := m[1]
			pkg := m[2]
			rest := strings.TrimSpace(m[3])

			status := debianPkgStatus{Release: release}

			if sm := statusTagRe.FindStringSubmatch(rest); sm != nil {
				status.Status = sm[1]
				remainder := strings.TrimSpace(statusTagRe.ReplaceAllString(rest, ""))
				remainder = strings.TrimPrefix(remainder, "(")
				remainder = strings.TrimSuffix(remainder, ")")
				if remainder != "" {
					status.Note = remainder
				}
			} else {
				status.Version = rest
				status.Status = "fixed"
			}

			current.Packages[pkg] = append(current.Packages[pkg], status)
			continue
		}

		if m := unstablePkgRe.FindStringSubmatch(line); m != nil {
			pkg := m[1]
			rest := strings.TrimSpace(m[2])

			status := debianPkgStatus{Release: "unstable"}

			if sm := statusTagRe.FindStringSubmatch(rest); sm != nil {
				status.Status = sm[1]
			} else {
				status.Version = rest
				status.Status = "fixed"
			}

			current.Packages[pkg] = append(current.Packages[pkg], status)
			continue
		}
	}

	return result, scanner.Err()
}

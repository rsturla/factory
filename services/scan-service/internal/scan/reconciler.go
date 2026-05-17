package scan

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/rsturla/factory/services/scan-service/internal/blob"
	"github.com/rsturla/factory/services/scan-service/internal/model"
	"github.com/rsturla/factory/services/scan-service/internal/store"

	"github.com/hummingbird-org/factory-workqueue/sdk/go/reconciler"
)

// Reconciler processes scan work items by running a vulnerability scanner
// against an SBOM fetched from blob storage.
type Reconciler struct {
	store    store.Store
	blobs    blob.Store
	scanners map[string]Scanner
}

// NewReconciler creates a new scan Reconciler.
func NewReconciler(s store.Store, blobs blob.Store, scanners map[string]Scanner) *Reconciler {
	return &Reconciler{
		store:    s,
		blobs:    blobs,
		scanners: scanners,
	}
}

// Reconcile processes a scan work item.
//
// Key format: "grype|sha256:bbb|linux/amd64"
//   - First segment before | is the scanner name
//   - Remaining segments are the platform key (digest|os/arch)
func (r *Reconciler) Reconcile(ctx context.Context, req reconciler.ProcessRequest) (reconciler.ProcessResponse, error) {
	log := slog.With("key", req.Key, "attempt", req.Attempt)

	scannerName, platformKey, err := parseKey(req.Key)
	if err != nil {
		log.Error("invalid scan key", "error", err)
		return reconciler.Reject(fmt.Sprintf("invalid scan key: %v", err)), nil
	}

	scanner, ok := r.scanners[scannerName]
	if !ok {
		log.Error("unknown scanner", "scanner", scannerName)
		return reconciler.Reject(fmt.Sprintf("unknown scanner: %s", scannerName)), nil
	}

	// Extract digest from platform key (format: "sha256:xxx|linux/amd64")
	digest, _, err := parsePlatformKey(platformKey)
	if err != nil {
		log.Error("invalid platform key", "error", err)
		return reconciler.Reject(fmt.Sprintf("invalid platform key: %v", err)), nil
	}

	// Read SBOM from blob store
	blobKey := sbomBlobKey(digest)
	sbomBytes, err := r.blobs.Get(ctx, blobKey)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			log.Warn("SBOM not found in blob store, catalog may not have processed this platform yet", "blob_key", blobKey)
			return reconciler.Reject(fmt.Sprintf("SBOM not found: %s", blobKey)), nil
		}
		return reconciler.ProcessResponse{}, fmt.Errorf("get sbom blob: %w", err)
	}

	log.Info("scanning platform", "scanner", scannerName, "digest", digest, "sbom_bytes", len(sbomBytes))

	startedAt := time.Now()
	findings, meta, err := scanner.Scan(ctx, sbomBytes)
	completedAt := time.Now()

	if err != nil {
		errStr := err.Error()
		if strings.Contains(errStr, "not found") {
			log.Warn("scanner reported not found, rejecting", "error", err)
			return reconciler.Reject(fmt.Sprintf("scanner error: %v", err)), nil
		}
		return reconciler.ProcessResponse{}, fmt.Errorf("scan: %w", err)
	}

	// Count findings by severity
	var criticalCount, highCount, mediumCount, lowCount int
	for _, f := range findings {
		switch strings.ToLower(f.Severity) {
		case "critical":
			criticalCount++
		case "high":
			highCount++
		case "medium":
			mediumCount++
		case "low":
			lowCount++
		}
	}

	// Generate a deterministic scan ID
	scanID := fmt.Sprintf("%x", sha256.Sum256([]byte(platformKey+"|"+scannerName+"|"+meta.DBVersion+"|"+startedAt.Format(time.RFC3339Nano))))[:16]

	scan := model.Scan{
		ID:            scanID,
		PlatformID:    platformKey,
		Scanner:       scannerName,
		DBVersion:     meta.DBVersion,
		StartedAt:     startedAt,
		CompletedAt:   completedAt,
		VulnCount:     len(findings),
		CriticalCount: criticalCount,
		HighCount:     highCount,
		MediumCount:   mediumCount,
		LowCount:      lowCount,
		Status:        "completed",
	}

	if err := r.store.UpsertScan(ctx, scan); err != nil {
		return reconciler.ProcessResponse{}, fmt.Errorf("upsert scan: %w", err)
	}

	// Convert scanner findings to model findings
	modelFindings := make([]model.Finding, len(findings))
	for i, f := range findings {
		modelFindings[i] = model.Finding{
			ScanID:         scanID,
			VulnID:         f.VulnID,
			Severity:       f.Severity,
			PackageName:    f.PackageName,
			PackageVersion: f.PackageVersion,
			PackageType:    f.PackageType,
			FixedVersion:   f.FixedVersion,
		}
	}

	if len(modelFindings) > 0 {
		if err := r.store.UpsertFindings(ctx, scanID, modelFindings); err != nil {
			return reconciler.ProcessResponse{}, fmt.Errorf("upsert findings: %w", err)
		}
	}

	log.Info("scan complete",
		"scanner", scannerName,
		"platform", platformKey,
		"db_version", meta.DBVersion,
		"vulns", len(findings),
		"critical", criticalCount,
		"high", highCount,
		"medium", mediumCount,
		"low", lowCount,
		"duration", completedAt.Sub(startedAt),
	)

	return reconciler.Completed(), nil
}

// parseKey splits a scan key into scanner name and platform key.
// Input: "grype|sha256:bbb|linux/amd64"
// Output: "grype", "sha256:bbb|linux/amd64"
func parseKey(key string) (scannerName, platformKey string, err error) {
	idx := strings.Index(key, "|")
	if idx < 0 {
		return "", "", fmt.Errorf("missing | separator in key %q", key)
	}
	scannerName = key[:idx]
	platformKey = key[idx+1:]
	if scannerName == "" {
		return "", "", fmt.Errorf("empty scanner name in key %q", key)
	}
	if platformKey == "" {
		return "", "", fmt.Errorf("empty platform key in key %q", key)
	}
	return scannerName, platformKey, nil
}

// parsePlatformKey splits a platform key into digest and os/arch.
// Input: "sha256:bbb|linux/amd64"
// Output: "sha256:bbb", "linux/amd64"
func parsePlatformKey(key string) (digest, osArch string, err error) {
	idx := strings.Index(key, "|")
	if idx < 0 {
		return "", "", fmt.Errorf("missing | separator in platform key %q", key)
	}
	digest = key[:idx]
	osArch = key[idx+1:]
	if digest == "" {
		return "", "", fmt.Errorf("empty digest in platform key %q", key)
	}
	if osArch == "" {
		return "", "", fmt.Errorf("empty os/arch in platform key %q", key)
	}
	return digest, osArch, nil
}

// sbomBlobKey returns the blob store key for an SBOM given a platform digest.
func sbomBlobKey(platformDigest string) string {
	clean := strings.TrimPrefix(platformDigest, "sha256:")
	return "sboms/" + clean + ".spdx.json"
}

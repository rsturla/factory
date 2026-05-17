package analyze

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"log/slog"
	"strings"

	"github.com/anchore/syft/syft/format"

	"github.com/rsturla/factory/services/catalog/internal/blob"
	"github.com/rsturla/factory/services/catalog/internal/discover"
	"github.com/rsturla/factory/services/catalog/internal/model"
	"github.com/rsturla/factory/services/catalog/internal/store"

	"github.com/hummingbird-org/factory-workqueue/sdk/go/reconciler"
)

// Reconciler reads a pre-generated SBOM from blob storage, extracts
// packages and OCI config, and writes the results to the database.
type Reconciler struct {
	store store.Store
	blobs blob.Store
}

func NewReconciler(s store.Store, blobs blob.Store) *Reconciler {
	return &Reconciler{store: s, blobs: blobs}
}

func (r *Reconciler) Reconcile(ctx context.Context, req reconciler.ProcessRequest) (reconciler.ProcessResponse, error) {
	log := slog.With("key", req.Key, "attempt", req.Attempt)

	digest, osArch, err := discover.ParsePlatformKey(req.Key)
	if err != nil {
		log.Error("invalid platform key", "error", err)
		return reconciler.Reject(fmt.Sprintf("invalid platform key: %v", err)), nil
	}

	log.Info("analyzing platform", "digest", digest, "os_arch", osArch)

	platform, err := r.store.GetPlatform(ctx, digest)
	if err != nil {
		return reconciler.ProcessResponse{}, fmt.Errorf("get platform: %w", err)
	}
	if platform == nil {
		log.Warn("platform not found, skipping")
		return reconciler.Completed(), nil
	}

	blobKey := sbomBlobKey(digest)
	sbomBytes, err := r.blobs.Get(ctx, blobKey)
	if err != nil {
		return reconciler.ProcessResponse{}, fmt.Errorf("read sbom blob %s: %w", blobKey, err)
	}

	bom, _, _, err := format.Decode(bytes.NewReader(sbomBytes))
	if err != nil {
		return reconciler.ProcessResponse{}, fmt.Errorf("decode sbom: %w", err)
	}

	packages := extractPackages(bom)

	var packageIDs []string
	for _, pkg := range packages {
		id, err := r.store.UpsertPackage(ctx, pkg)
		if err != nil {
		return reconciler.ProcessResponse{}, fmt.Errorf("upsert package %s: %w", pkg.PURL, err)
		}
		packageIDs = append(packageIDs, id)
	}

	if err := r.store.AssociatePackages(ctx, platform.ID, packageIDs); err != nil {
		return reconciler.ProcessResponse{}, fmt.Errorf("associate packages: %w", err)
	}

	contentHash := fmt.Sprintf("%x", sha256.Sum256(sbomBytes))
	if err := r.store.UpsertSBOM(ctx, model.SBOM{
		PlatformID:  platform.ID,
		Source:      "syft",
		Format:      "spdx-json",
		ContentHash: contentHash,
		Raw:         sbomBytes,
	}); err != nil {
		return reconciler.ProcessResponse{}, fmt.Errorf("upsert sbom: %w", err)
	}

	log.Info("analysis complete", "platform", platform.ID, "arch", platform.Architecture, "packages", len(packages))
	return reconciler.Completed(), nil
}

func sbomBlobKey(platformDigest string) string {
	clean := strings.TrimPrefix(platformDigest, "sha256:")
	return "sboms/" + clean + ".spdx.json"
}

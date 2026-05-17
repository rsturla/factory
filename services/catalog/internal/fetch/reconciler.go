package fetch

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/rsturla/factory/services/catalog/internal/blob"
	"github.com/rsturla/factory/services/catalog/internal/discover"
	"github.com/rsturla/factory/services/catalog/internal/model"
	"github.com/rsturla/factory/services/catalog/internal/store"

	"github.com/hummingbird-org/factory-workqueue/sdk/go/reconciler"
)

// Reconciler fetches container images, generates SBOMs via Syft, stores
// them to blob storage, and enqueues the platform key for analysis.
type Reconciler struct {
	store   store.Store
	blobs   blob.Store
	scanner Scanner
	enqueue *reconciler.EnqueueClient
	queue   string // analyze queue name
}

func NewReconciler(s store.Store, blobs blob.Store, scanner Scanner, enqueue *reconciler.EnqueueClient, analyzeQueue string) *Reconciler {
	return &Reconciler{
		store:   s,
		blobs:   blobs,
		scanner: scanner,
		enqueue: enqueue,
		queue:   analyzeQueue,
	}
}

func (r *Reconciler) Reconcile(ctx context.Context, req reconciler.ProcessRequest) (reconciler.ProcessResponse, error) {
	log := slog.With("key", req.Key, "attempt", req.Attempt)

	digest, osArch, err := discover.ParsePlatformKey(req.Key)
	if err != nil {
		log.Error("invalid platform key", "error", err)
		return reconciler.Reject(fmt.Sprintf("invalid platform key: %v", err)), nil
	}

	log.Info("fetching platform SBOM", "digest", digest, "os_arch", osArch)

	platform, err := r.store.GetPlatform(ctx, digest)
	if err != nil {
		return reconciler.ProcessResponse{}, fmt.Errorf("get platform: %w", err)
	}
	if platform == nil {
		log.Warn("platform not found, skipping")
		return reconciler.Completed(), nil
	}

	img, err := r.store.GetImage(ctx, platform.ImageID)
	if err != nil {
		return reconciler.ProcessResponse{}, fmt.Errorf("get image: %w", err)
	}
	if img == nil {
		log.Warn("image not found, skipping")
		return reconciler.Completed(), nil
	}

	imageRef := buildImageRef(img, digest)
	log.Info("scanning image", "ref", imageRef, "arch", platform.Architecture)

	output, err := r.scanner.Scan(ctx, imageRef)
	if err != nil {
		errStr := err.Error()
		if strings.Contains(errStr, "MANIFEST_UNKNOWN") ||
			strings.Contains(errStr, "not found") ||
			strings.Contains(errStr, "does not exist") {
			log.Warn("image not found, rejecting", "error", err)
			return reconciler.Reject(fmt.Sprintf("image not found: %v", err)), nil
		}
		return reconciler.ProcessResponse{}, fmt.Errorf("scan: %w", err)
	}

	if output.Config != nil {
		platform.Config = output.Config
		if err := r.store.UpsertPlatform(ctx, *platform); err != nil {
			return reconciler.ProcessResponse{}, fmt.Errorf("upsert platform config: %w", err)
		}
	}

	blobKey := sbomBlobKey(digest)
	if err := r.blobs.Put(ctx, blobKey, output.SBOM); err != nil {
		return reconciler.ProcessResponse{}, fmt.Errorf("store sbom blob: %w", err)
	}

	if err := r.enqueue.Enqueue(ctx, r.queue, req.Key, 0); err != nil {
		return reconciler.ProcessResponse{}, fmt.Errorf("enqueue analyze: %w", err)
	}

	log.Info("fetch complete", "platform", platform.ID, "arch", platform.Architecture, "sbom_bytes", len(output.SBOM))
	return reconciler.Completed(), nil
}

func buildImageRef(img *model.Image, digest string) string {
	if img != nil && len(img.Tags) > 0 {
		t := img.Tags[0]
		return t.Registry + "/" + t.Repository + "@" + digest
	}
	return digest
}

func sbomBlobKey(platformDigest string) string {
	// Strip the "sha256:" prefix for the filename to keep paths clean.
	clean := strings.TrimPrefix(platformDigest, "sha256:")
	return "sboms/" + clean + ".spdx.json"
}

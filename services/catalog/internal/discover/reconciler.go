package discover

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/containers/image/v5/docker"
	"github.com/containers/image/v5/docker/reference"
	"github.com/containers/image/v5/manifest"
	"github.com/containers/image/v5/types"
	"github.com/opencontainers/go-digest"

	"github.com/rsturla/factory/services/catalog/internal/model"
	"github.com/rsturla/factory/services/catalog/internal/store"

	"github.com/hummingbird-org/factory-workqueue/sdk/go/reconciler"
)

type Reconciler struct {
	store           store.Store
	enqueueFetch    *reconciler.EnqueueClient
	enqueueDiscover *reconciler.EnqueueClient
	fetchQueue      string
	discoverQueue   string
}

func NewReconciler(s store.Store, enqueueDiscover, enqueueFetch *reconciler.EnqueueClient, discoverQueue, fetchQueue string) *Reconciler {
	return &Reconciler{
		store:           s,
		enqueueFetch:    enqueueFetch,
		enqueueDiscover: enqueueDiscover,
		fetchQueue:      fetchQueue,
		discoverQueue:   discoverQueue,
	}
}

func (r *Reconciler) Reconcile(ctx context.Context, req reconciler.ProcessRequest) (reconciler.ProcessResponse, error) {
	log := slog.With("key", req.Key, "attempt", req.Attempt)

	registry, repository, tag, err := parseImageRef(req.Key)
	if err != nil {
		log.Error("invalid image ref", "error", err)
		return reconciler.Reject(fmt.Sprintf("invalid image ref: %v", err)), nil
	}

	if tag == "" {
		log.Info("discovering repository tags", "registry", registry, "repo", repository)
		return r.discoverRepo(ctx, log, registry, repository)
	}

	log.Info("discovering tagged image", "registry", registry, "repo", repository, "tag", tag)
	return r.discoverTaggedImage(ctx, log, registry, repository, tag)
}

func (r *Reconciler) discoverRepo(ctx context.Context, log *slog.Logger, registry, repository string) (reconciler.ProcessResponse, error) {
	repoRef, err := docker.ParseReference("//" + registry + "/" + repository)
	if err != nil {
		return reconciler.ProcessResponse{}, fmt.Errorf("parse repo ref: %w", err)
	}
	tags, err := docker.GetRepositoryTags(ctx, &types.SystemContext{}, repoRef)
	if err != nil {
		return reconciler.ProcessResponse{}, fmt.Errorf("list tags: %w", err)
	}

	log.Info("discovered tags", "count", len(tags))

	var enqueued int
	for _, tag := range tags {
		if skipTag(tag) {
			continue
		}
		ref := fmt.Sprintf("%s/%s:%s", registry, repository, tag)
		if err := r.enqueueDiscover.Enqueue(ctx, r.discoverQueue, ref, 0); err != nil {
		return reconciler.ProcessResponse{}, fmt.Errorf("enqueue tag %s: %w", ref, err)
		}
		enqueued++
	}

	log.Info("enqueued tags for discovery", "enqueued", enqueued, "skipped", len(tags)-enqueued)
	return reconciler.Completed(), nil
}

func (r *Reconciler) discoverTaggedImage(ctx context.Context, log *slog.Logger, registry, repository, tag string) (reconciler.ProcessResponse, error) {
	fullRef := fmt.Sprintf("%s/%s:%s", registry, repository, tag)
	ref, err := docker.ParseReference("//" + fullRef)
	if err != nil {
		return reconciler.ProcessResponse{}, fmt.Errorf("parse docker ref: %w", err)
	}

	sysCtx := &types.SystemContext{}
	src, err := ref.NewImageSource(ctx, sysCtx)
	if err != nil {
		return reconciler.ProcessResponse{}, fmt.Errorf("create image source: %w", err)
	}
	defer src.Close()

	manifestBytes, mimeType, err := src.GetManifest(ctx, nil)
	if err != nil {
		return reconciler.ProcessResponse{}, fmt.Errorf("get manifest: %w", err)
	}

	indexDigest := digest.FromBytes(manifestBytes).String()

	img := model.Image{
		ID:     indexDigest,
		Digest: indexDigest,
	}
	if err := r.store.UpsertImage(ctx, img); err != nil {
		return reconciler.ProcessResponse{}, fmt.Errorf("upsert image: %w", err)
	}

	if err := r.store.UpsertTag(ctx, indexDigest, model.Tag{
		Registry:   registry,
		Repository: repository,
		Tag:        tag,
	}); err != nil {
		return reconciler.ProcessResponse{}, fmt.Errorf("upsert tag: %w", err)
	}

	existing, err := r.store.GetImage(ctx, indexDigest)
	if err != nil {
		return reconciler.ProcessResponse{}, fmt.Errorf("check existing: %w", err)
	}
	if existing != nil && len(existing.Platforms) > 0 {
		// Check if all platforms have SBOMs — if not, re-enqueue unfetched ones
		var unfetched []string
		for _, p := range existing.Platforms {
			has, _ := r.store.HasSBOM(ctx, p.ID)
			if !has {
				unfetched = append(unfetched, platformKey(p))
			}
		}
		if len(unfetched) == 0 {
			log.Info("digest already cataloged, tag updated", "digest", indexDigest)
			return reconciler.Completed(), nil
		}
		log.Info("re-enqueuing platforms missing SBOMs", "digest", indexDigest, "count", len(unfetched))
		for _, k := range unfetched {
			if err := r.enqueueFetch.Enqueue(ctx, r.fetchQueue, k, 0); err != nil {
				return reconciler.ProcessResponse{}, fmt.Errorf("enqueue unfetched %s: %w", k, err)
			}
		}
		return reconciler.Completed(), nil
	}

	var platformKeys []string

	if manifest.MIMETypeIsMultiImage(mimeType) {
		platforms, err := parsePlatforms(manifestBytes, indexDigest)
		if err != nil {
			return reconciler.ProcessResponse{}, err
		}
		for _, p := range platforms {
			if err := r.store.UpsertPlatform(ctx, p); err != nil {
		return reconciler.ProcessResponse{}, fmt.Errorf("upsert platform: %w", err)
			}
			key := platformKey(p)
			platformKeys = append(platformKeys, key)
			log.Info("discovered platform", "digest", p.ID, "arch", p.Architecture)
		}
	} else {
		p := model.Platform{
			ID:           indexDigest,
			ImageID:      indexDigest,
			OS:           "linux",
			Architecture: "amd64",
		}
		if err := r.store.UpsertPlatform(ctx, p); err != nil {
		return reconciler.ProcessResponse{}, fmt.Errorf("upsert platform: %w", err)
		}
		platformKeys = append(platformKeys, platformKey(p))
		log.Info("discovered single-arch platform", "digest", p.ID)
	}

	for _, k := range platformKeys {
		if err := r.enqueueFetch.Enqueue(ctx, r.fetchQueue, k, 0); err != nil {
		return reconciler.ProcessResponse{}, fmt.Errorf("enqueue platform %s: %w", k, err)
		}
	}

	log.Info("discover complete", "digest", indexDigest, "platforms", len(platformKeys))
	return reconciler.Completed(), nil
}

func skipTag(tag string) bool {
	if strings.HasPrefix(tag, "sha256-") {
		return true
	}
	if strings.HasSuffix(tag, "-source") {
		return true
	}
	for _, suffix := range []string{".sig", ".att", ".sbom", ".src"} {
		if strings.HasSuffix(tag, suffix) {
			return true
		}
	}
	return false
}

func parsePlatforms(manifestBytes []byte, imageID string) ([]model.Platform, error) {
	index, err := manifest.OCI1IndexFromManifest(manifestBytes)
	if err == nil {
		var platforms []model.Platform
		for _, m := range index.Manifests {
			if m.Platform == nil {
				continue
			}
			platforms = append(platforms, model.Platform{
				ID:           m.Digest.String(),
				ImageID:      imageID,
				OS:           m.Platform.OS,
				Architecture: m.Platform.Architecture,
				Variant:      m.Platform.Variant,
			})
		}
		return platforms, nil
	}

	list, err2 := manifest.Schema2ListFromManifest(manifestBytes)
	if err2 != nil {
		return nil, fmt.Errorf("parse manifest index: %w (oci: %v)", err2, err)
	}
	var platforms []model.Platform
	for _, m := range list.Manifests {
		platforms = append(platforms, model.Platform{
			ID:           m.Digest.String(),
			ImageID:      imageID,
			OS:           m.Platform.OS,
			Architecture: m.Platform.Architecture,
			Variant:      m.Platform.Variant,
		})
	}
	return platforms, nil
}

func platformKey(p model.Platform) string {
	arch := p.OS + "/" + p.Architecture
	if p.Variant != "" {
		arch += "/" + p.Variant
	}
	return p.ID + "|" + arch
}

func parseImageRef(ref string) (registry, repo, tag string, err error) {
	named, err := reference.ParseNormalizedNamed(ref)
	if err != nil {
		return "", "", "", fmt.Errorf("parse reference: %w", err)
	}
	domain := reference.Domain(named)
	path := reference.Path(named)
	if digested, ok := named.(reference.Digested); ok {
		return domain, path, digested.Digest().String(), nil
	}
	if tagged, ok := named.(reference.Tagged); ok {
		return domain, path, tagged.Tag(), nil
	}
	return domain, path, "", nil
}

func ParsePlatformKey(key string) (digest, osArch string, err error) {
	parts := strings.SplitN(key, "|", 2)
	if len(parts) != 2 {
		return "", "", fmt.Errorf("invalid platform key: %s", key)
	}
	return parts[0], parts[1], nil
}

package store

import (
	"context"

	"github.com/rsturla/factory/services/catalog/internal/model"
)

type Store interface {
	// Images
	UpsertImage(ctx context.Context, img model.Image) error
	GetImage(ctx context.Context, id string) (*model.Image, error)
	GetImageByDigest(ctx context.Context, digest string) (*model.Image, error)
	ListImages(ctx context.Context, limit, offset int) ([]model.Image, int, error)

	// Tags
	UpsertTag(ctx context.Context, imageID string, tag model.Tag) error
	ListTagsByImage(ctx context.Context, imageID string) ([]model.Tag, error)
	GetImageByTag(ctx context.Context, registry, repository, tag string) (*model.Image, error)

	// Platforms
	UpsertPlatform(ctx context.Context, p model.Platform) error
	GetPlatform(ctx context.Context, id string) (*model.Platform, error)
	ListPlatformsByImage(ctx context.Context, imageID string) ([]model.Platform, error)

	// Packages
	UpsertPackage(ctx context.Context, pkg model.Package) (string, error)
	AssociatePackages(ctx context.Context, platformID string, packageIDs []string) error
	ListPackagesByPlatform(ctx context.Context, platformID string) ([]model.Package, error)
	SearchPackages(ctx context.Context, name string, limit int) ([]model.Package, error)
	GetImagesByPackage(ctx context.Context, purl string) ([]model.Image, error)
	GetImagesByPackageName(ctx context.Context, name, version string, limit int) ([]model.Image, error)
	DiffPackages(ctx context.Context, fromPlatformID, toPlatformID string) (added []model.Package, removed []model.Package, err error)

	// SBOMs
	UpsertSBOM(ctx context.Context, sbom model.SBOM) error
	GetSBOM(ctx context.Context, platformID, source string) (*model.SBOM, error)
	HasSBOM(ctx context.Context, platformID string) (bool, error)

	// Checkpoints
	GetCheckpoint(ctx context.Context, source string) (string, error)
	UpdateCheckpoint(ctx context.Context, source, value string) error

	// Health
	Ping(ctx context.Context) error
	Close()
}

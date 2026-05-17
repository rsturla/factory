package model

import "time"

// Image represents a container image manifest index tracked by the catalog.
// The ID is the index manifest digest — the universal identifier across registries.
type Image struct {
	ID        string     `json:"id"`
	Digest    string     `json:"digest"`
	Platforms []Platform `json:"platforms,omitempty"`
	Tags      []Tag      `json:"tags,omitempty"`
}

// Tag records where an image digest is referenced.
// Multiple tags and registries can point to the same digest.
type Tag struct {
	Registry   string `json:"registry"`
	Repository string `json:"repository"`
	Tag        string `json:"tag"`
}

// Platform represents an OS/architecture variant within an image index.
// The ID is the platform manifest digest.
type Platform struct {
	ID           string            `json:"id"`
	ImageID      string            `json:"image_id"`
	OS           string            `json:"os"`
	Architecture string            `json:"architecture"`
	Variant      string            `json:"variant,omitempty"`
	Config       *PlatformConfig   `json:"config,omitempty"`
	Packages     []Package         `json:"packages,omitempty"`
}

// PlatformConfig holds OCI image configuration metadata.
type PlatformConfig struct {
	User           string            `json:"user,omitempty"`
	WorkingDir     string            `json:"working_dir,omitempty"`
	Entrypoint     []string          `json:"entrypoint,omitempty"`
	Cmd            []string          `json:"cmd,omitempty"`
	Env            []string          `json:"env,omitempty"`
	Labels         map[string]string `json:"labels,omitempty"`
	ExposedPorts   []string          `json:"exposed_ports,omitempty"`
	CompressedSize int64             `json:"compressed_size"`
	LayerCount     int               `json:"layer_count"`
	CreatedAt      *time.Time        `json:"created_at,omitempty"`
}

// Package represents an installed software package discovered in a platform.
type Package struct {
	ID        string `json:"id"`
	PURL      string `json:"purl"`
	Type      string `json:"type"`
	Name      string `json:"name"`
	Version   string `json:"version"`
	Namespace string `json:"namespace,omitempty"`
}

// SBOM represents a software bill of materials for a specific platform.
type SBOM struct {
	ID          string `json:"id"`
	PlatformID  string `json:"platform_id"`
	Source      string `json:"source"`
	Format      string `json:"format"`
	ContentHash string `json:"content_hash"`
	Raw         []byte `json:"raw,omitempty"`
}

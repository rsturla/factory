package imageconfig

import (
	"encoding/json"
	"sort"
	"time"

	"github.com/anchore/syft/syft/sbom"
	"github.com/anchore/syft/syft/source"

	"github.com/rsturla/factory/services/catalog/internal/model"
)

type ociImageConfig struct {
	Created string `json:"created"`
	Config  struct {
		User         string              `json:"User"`
		WorkingDir   string              `json:"WorkingDir"`
		Entrypoint   []string            `json:"Entrypoint"`
		Cmd          []string            `json:"Cmd"`
		Env          []string            `json:"Env"`
		Labels       map[string]string   `json:"Labels"`
		ExposedPorts map[string]struct{} `json:"ExposedPorts"`
	} `json:"config"`
}

func Extract(bom *sbom.SBOM) *model.PlatformConfig {
	meta, ok := bom.Source.Metadata.(source.ImageMetadata)
	if !ok {
		return nil
	}

	config := &model.PlatformConfig{
		Labels:     meta.Labels,
		LayerCount: len(meta.Layers),
	}

	for _, layer := range meta.Layers {
		config.CompressedSize += layer.Size
	}

	if len(meta.RawConfig) > 0 {
		var oci ociImageConfig
		if err := json.Unmarshal(meta.RawConfig, &oci); err == nil {
			config.User = oci.Config.User
			config.WorkingDir = oci.Config.WorkingDir
			config.Entrypoint = oci.Config.Entrypoint
			config.Cmd = oci.Config.Cmd
			config.Env = oci.Config.Env
			if config.Labels == nil {
				config.Labels = oci.Config.Labels
			}
			for port := range oci.Config.ExposedPorts {
				config.ExposedPorts = append(config.ExposedPorts, port)
			}
			sort.Strings(config.ExposedPorts)
			if oci.Created != "" {
				if t, err := time.Parse(time.RFC3339Nano, oci.Created); err == nil {
					config.CreatedAt = &t
				}
			}
		}
	}

	return config
}

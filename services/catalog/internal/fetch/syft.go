package fetch

import (
	"bytes"
	"context"
	"fmt"
	"io"

	"github.com/anchore/syft/syft"
	"github.com/anchore/syft/syft/format/spdxjson"

	"github.com/rsturla/factory/services/catalog/internal/imageconfig"
	"github.com/rsturla/factory/services/catalog/internal/model"
)

type ScanOutput struct {
	SBOM   []byte
	Config *model.PlatformConfig
}

type Scanner interface {
	Scan(ctx context.Context, imageRef string) (*ScanOutput, error)
}

type SyftScanner struct{}

func NewSyftScanner() *SyftScanner {
	return &SyftScanner{}
}

func (s *SyftScanner) Scan(ctx context.Context, imageRef string) (*ScanOutput, error) {
	src, err := syft.GetSource(ctx, imageRef, syft.DefaultGetSourceConfig().
		WithDefaultImagePullSource("registry"))
	if err != nil {
		return nil, fmt.Errorf("get source %s: %w", imageRef, err)
	}
	if closer, ok := src.(io.Closer); ok {
		defer closer.Close()
	}

	bom, err := syft.CreateSBOM(ctx, src, nil)
	if err != nil {
		return nil, fmt.Errorf("create sbom: %w", err)
	}

	config := imageconfig.Extract(bom)

	encoder, err := spdxjson.NewFormatEncoderWithConfig(spdxjson.DefaultEncoderConfig())
	if err != nil {
		return nil, fmt.Errorf("create encoder: %w", err)
	}

	var buf bytes.Buffer
	if err := encoder.Encode(&buf, *bom); err != nil {
		return nil, fmt.Errorf("encode sbom: %w", err)
	}

	return &ScanOutput{
		SBOM:   buf.Bytes(),
		Config: config,
	}, nil
}

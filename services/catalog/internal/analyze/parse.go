package analyze

import (
	"fmt"

	"github.com/anchore/syft/syft/sbom"

	"github.com/rsturla/factory/services/catalog/internal/model"
)

func extractPackages(bom *sbom.SBOM) []model.Package {
	var packages []model.Package

	for p := range bom.Artifacts.Packages.Enumerate() {
		pkg := model.Package{
			Name:    p.Name,
			Version: p.Version,
			Type:    string(p.Type),
		}

		if p.PURL != "" {
			pkg.PURL = p.PURL
		} else {
			pkg.PURL = fmt.Sprintf("pkg:%s/%s@%s", p.Type, p.Name, p.Version)
		}

		for _, l := range p.Locations.ToSlice() {
			if l.AccessPath != "" {
				pkg.Namespace = l.AccessPath
				break
			}
		}

		packages = append(packages, pkg)
	}

	return packages
}


package sbom

import (
	"context"

	"github.com/anchore/syft/syft"
	"github.com/anchore/syft/syft/artifact"
	"github.com/anchore/syft/syft/format/spdxjson"
	"github.com/anchore/syft/syft/pkg"
	"github.com/anchore/syft/syft/sbom"
	"github.com/anchore/syft/syft/source/sourceproviders"
	_ "modernc.org/sqlite" // required for Syft's RPM cataloguer
)

var sourceConfig = syft.DefaultGetSourceConfig().WithSources(sourceproviders.DirTag)

var createSBOMConfig = syft.DefaultCreateSBOMConfig()

var encoderConfig = spdxjson.DefaultEncoderConfig()

type SyftPackage struct {
	PURL             string
	DependencyOfPURL string
	Checksums        []string
}

// Performs a syft scan on the root directory and returns a slice of SyftPackage structs.
func SyftScan(root string) ([]SyftPackage, error) {
	ctx := context.Background()

	src, err := syft.GetSource(ctx, root, sourceConfig)
	if err != nil {
		return []SyftPackage{}, err
	}

	sbom, err := syft.CreateSBOM(ctx, src, createSBOMConfig)
	if err != nil {
		return []SyftPackage{}, err
	}

	return getTopLevelPackages(sbom), nil
}

// Get a slice of SyftPackage structs of "top level" packages. These are packages
// that have a direct CONTAINS relationship from the document root.
func getTopLevelPackages(sbom *sbom.SBOM) (packages []SyftPackage) {
	// collect pkg IDs of packages that are contained directly by the document root
	topLevelPkgIds := make(map[artifact.ID]bool)
	for _, rel := range sbom.Relationships {
		if rel.Type == artifact.ContainsRelationship && rel.From.ID() == artifact.ID(sbom.Source.ID) {
			topLevelPkgIds[rel.To.ID()] = true
		}
	}

	idToPackage := getIdToPackageMap(sbom)
	for pkg := range sbom.Artifacts.Packages.Enumerate() {
		if !topLevelPkgIds[pkg.ID()] {
			continue
		}

		// Try to get the PURL that is package is a dependency of. This is used to differentiate
		// between the same packages with, that originate from different packages.
		dependencyOfPurl := ""
		for _, pkgRel := range sbom.RelationshipsForPackage(pkg, artifact.DependencyOfRelationship) {
			if pkgRel.From.ID() == pkg.ID() {
				dependencyOfPurl = idToPackage[pkgRel.To.ID()].PURL
				break
			}
		}

		checksums := getPackageChecksums(sbom, &pkg)
		packages = append(packages, SyftPackage{
			PURL:             pkg.PURL,
			Checksums:        checksums,
			DependencyOfPURL: dependencyOfPurl,
		})
	}

	return packages
}

// Create a translation map between IDs and their associated packages
// in the SBOM for faster retrieval.
func getIdToPackageMap(sbom *sbom.SBOM) (res map[artifact.ID]pkg.Package) {
	res = make(map[artifact.ID]pkg.Package)
	for pkg := range sbom.Artifacts.Packages.Enumerate() {
		res[pkg.ID()] = pkg
	}
	return res
}

func getPackageChecksums(sbom *sbom.SBOM, p *pkg.Package) []string {
	// TODO: implement if we need higher resolution for package matching
	return []string{}
}

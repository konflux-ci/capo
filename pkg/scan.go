package capo

import (
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/konflux-ci/capo/internal/sbom"
	"github.com/konflux-ci/capo/pkg/containerfile"

	"go.podman.io/storage"
	"go.podman.io/storage/pkg/reexec"
)

type packageSource struct {
	// Stage alias of this stage or empty string
	// if this is only an external stage, i.e. there are COPY commands
	// only in the form of 'COPY --from=image:tag ... ...'.
	alias string

	// pullspec of this stage.
	pullspec string

	// Slice of paths to content in the layer/image which should be syft-scanned
	sources []string
}

type PackageMetadata struct {
	Packages []PackageMetadataItem `json:"packages"`
}

type PackageMetadataItem struct {
	PackageURL string `json:"purl"`

	// Slice of checksums, with checksum type prefixed (e.g. "sha256:deadbeef").
	// Omitted if syft didn't provide any checksums.
	Checksums []string `json:"checksums,omitempty"`

	// PURL of the package that this package is a dependency of.
	// Used for resolution of relationships if one package is
	// found multiple times as a dependency of different packages.
	DependencyOfPURL string `json:"dependency_of_purl,omitempty"`

	// Type of origin of this package, can be "builder" or "intermediate".
	OriginType string `json:"origin_type"`

	// Pullspec of the image which is this package's origin.
	Pullspec string `json:"pullspec"`

	// Alias of the stage of this package's origin.
	// Omitted if this package is from an external image.
	StageAlias string `json:"stage_alias,omitempty"`
}

func setupStore() (storage.Store, error) {
	// The containers/storage library requires this to run for some operations
	if reexec.Init() {
		return nil, fmt.Errorf("Failed to init reexec during containers/storage setup.")
	}

	opts, err := storage.DefaultStoreOptions()
	if err != nil {
		return nil, fmt.Errorf("Failed to create default container/storage options.")
	}

	store, err := storage.GetStore(opts)
	if err != nil {
		return nil, fmt.Errorf("Failed to create default container/storage store.")
	}

	return store, nil
}

// Scan reads the passed containerfile stages, resolves true content origin,
// extracts relevant content from buildah storage and scans it using syft.
// Returns a PackageMetadata struct containing packages and their origin information
// for resolution by Mobster.
func Scan(
	stages []containerfile.Stage,
) (PackageMetadata, error) {
	var res PackageMetadata

	store, err := setupStore()
	if err != nil {
		return PackageMetadata{}, err
	}

	for _, pkgSource := range getPackageSources(stages) {
		stagePkgItems, err := scanSource(store, pkgSource)
		if err != nil {
			return PackageMetadata{}, fmt.Errorf("Failed to scan source %+v with error: %v.", pkgSource, err)
		}

		res.Packages = append(res.Packages, stagePkgItems...)
	}

	return res, nil
}

// getPackageSources uses the passed containerfile stages and returns a slice of
// packageSource structs, specifying which COPY-ied content originates from which builder stage.
func getPackageSources(stages []containerfile.Stage) []packageSource {
	var res []packageSource
	aliasToStage := make(map[string]*containerfile.Stage)

	// use index iteration to get consistent references to stages
	// since we use the references as map keys
	for i := range stages[:len(stages)-1] {
		st := &stages[i]
		aliasToStage[st.Alias] = st
	}

	// The following code block reads all the builder COPY-ies in the final stage
	// and recursively traces their content to their respective origins in previous stages.
	// Builds a map between references to containerfile stages and the sources used in the COPY.
	final := &stages[len(stages)-1]
	stageToSources := make(map[*containerfile.Stage][]string)
	for _, cp := range final.Copies {
		for _, source := range cp.Sources {
			// the copy is builder type only if there's no builder stage with alias equal to the cp.from
			// otherwise the cp.from is a pullspec and it is an external copy
			if _, isBuilder := aliasToStage[cp.From]; isBuilder {
				traceSource(source, aliasToStage[cp.From], stageToSources, aliasToStage)
			} else {
				external := containerfile.Stage{
					Alias:    "",
					Pullspec: cp.From,
					Copies:   []containerfile.Copy{},
				}
				stageToSources[&external] = append(stageToSources[&external], source)
			}
		}
	}

	// construct builder package sources
	for i := range stages[:len(stages)-1] {
		stage := &stages[i]
		res = append(res, packageSource{
			alias:    stage.Alias,
			pullspec: stage.Pullspec,
			sources:  stageToSources[stage],
		})

		// the processed stage must be deleted from stageToSources so it only
		// contains "external" stages after builder stages are constructed.
		// These are then processed in the next code block below.
		delete(stageToSources, stage)
	}

	// construct external package sources
	for st, sources := range stageToSources {
		res = append(res, packageSource{
			alias:    st.Alias,
			pullspec: st.Pullspec,
			sources:  sources,
		})
	}

	return res
}

// traceSource takes a source path and the stage it was found in and recursively
// traces its origin up the builder stages. Once the true origin of the source
// path is found it modifies the passed accumulator so that pointers to stages map
// to the source paths that originated in them.
// aliasToStage is a mapping of stage aliases to stage pointers to use for lookups
// when resolving COPY commands.
func traceSource(
	source string,
	currStage *containerfile.Stage,
	acc map[*containerfile.Stage][]string,
	aliasToStage map[string]*containerfile.Stage,
) {
	isDirectory := strings.HasSuffix(source, "/")

	foundAncestor := false
	for _, cp := range currStage.Copies {
		if strings.HasPrefix(cp.Destination, source) {
			foundAncestor = true
			for _, s := range cp.Sources {
				traceSource(s, aliasToStage[cp.From], acc, aliasToStage)
			}
		}
	}

	// If the source is a directory, we want to add it to the accumulator
	// even if we traced some of the sources. This is because the directory could
	// contain mixed content - some from this stage, some copied from previous stages.
	if isDirectory || !foundAncestor {
		acc[currStage] = append(acc[currStage], source)
	}
}

// scanSource uses the passed initialized storage.Store struct to syft scan content
// from the passed packageSource. Returns a slice of PackageMetadataItem structs specifying
// origins of packages.
func scanSource(store storage.Store, pkgSource packageSource) ([]PackageMetadataItem, error) {
	// builder content is content that is present in a builder stage base image
	builderContentPath, err := os.MkdirTemp("", "")
	if err != nil {
		return nil, err
	}

	// intermediate content is content that created in a builder stage base during the build
	intermediateContentPath, err := os.MkdirTemp("", "")
	if err != nil {
		return nil, err
	}

	// if in debug mode, print the paths to saved content
	// and don't remove the temporary directories
	debugMode := os.Getenv("CAPO_DEBUG") != ""
	if debugMode {
		log.Printf("[DEBUG] Builder %s content path: %s", pkgSource.pullspec, builderContentPath)
		log.Printf("[DEBUG] Intermediate %s content path: %s", pkgSource.pullspec, intermediateContentPath)
	} else {
		defer os.RemoveAll(builderContentPath)
		defer os.RemoveAll(intermediateContentPath)
	}

	err = getContent(store, pkgSource, builderContentPath, intermediateContentPath)
	if err != nil {
		return nil, err
	}

	intermediatePkgs, err := sbom.SyftScan(intermediateContentPath)
	if err != nil {
		return nil, err
	}

	builderPkgs, err := sbom.SyftScan(builderContentPath)
	if err != nil {
		return nil, err
	}

	return getPackageMetadata(pkgSource, builderPkgs, intermediatePkgs), nil
}

// getPackageMetadata uses the passed packageSource and its builder and intermediate
// packages to return a slice of PackageMetadataItem structs to signify package origins.
func getPackageMetadata(
	pkgSource packageSource,
	builderPkgs []sbom.SyftPackage,
	intermediatePkgs []sbom.SyftPackage,
) []PackageMetadataItem {
	var res []PackageMetadataItem

	for _, bpkg := range builderPkgs {
		res = append(res, PackageMetadataItem{
			Pullspec:         pkgSource.pullspec,
			StageAlias:       pkgSource.alias,
			PackageURL:       bpkg.PURL,
			DependencyOfPURL: bpkg.DependencyOfPURL,
			Checksums:        bpkg.Checksums,
			OriginType:       "builder",
		})
	}

	for _, ipkg := range intermediatePkgs {
		res = append(res, PackageMetadataItem{
			Pullspec:         pkgSource.pullspec,
			StageAlias:       pkgSource.alias,
			PackageURL:       ipkg.PURL,
			DependencyOfPURL: ipkg.DependencyOfPURL,
			Checksums:        ipkg.Checksums,
			OriginType:       "intermediate",
		})
	}

	return res
}

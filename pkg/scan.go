package capo

import (
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/konflux-ci/capo/internal/sbom"
	"github.com/konflux-ci/capo/pkg/containerfile"
	"github.com/konflux-ci/capo/pkg/storageclient"

	"go.podman.io/image/v5/docker/reference"
	"go.podman.io/storage"
	"go.podman.io/storage/pkg/reexec"
)

type packageSource struct {
	// Stage alias of this stage or empty string
	// if this is only an external stage, i.e. there are COPY commands
	// only in the form of 'COPY --from=image:tag ... ...'.
	alias string

	// Pullspec of this stage as it appeared in the containerfile.
	pullspec string

	// Pullspec of the base of this stage with digest instead of tag.
	digestBase string

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

	// Type of origin of this package: "builder" (from a builder stage's base image),
	// "intermediate" (created during the build in a builder stage), or "external"
	// (from an external image referenced via COPY --from or RUN --mount).
	OriginType string `json:"origin_type"`

	// Pullspec of the image with digest which is this package's origin.
	Pullspec string `json:"pullspec"`

	// Alias of the stage of this package's origin.
	// Omitted if this package is from an external image.
	StageAlias string `json:"stage_alias,omitempty"`
}

var ErrStorageSetup = errors.New("error while setting up buildah storage")
var ErrPullspecResolve = errors.New("failed to resolve pullspec")
var ErrOCIConfig = errors.New("failed to get OCIImageConfig")

func SetupStore() (storage.Store, error) {
	// The containers/storage library requires this to run for some operations
	if reexec.Init() {
		return nil, fmt.Errorf("%w: failed to init reexec", ErrStorageSetup)
	}

	opts, err := storage.DefaultStoreOptions()
	if err != nil {
		return nil, fmt.Errorf("%w: failed to create default storage options: %w", ErrStorageSetup, err)
	}

	store, err := storage.GetStore(opts)
	if err != nil {
		return nil, fmt.Errorf("%w: failed to create storage: %w", ErrStorageSetup, err)
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
	res := PackageMetadata{
		Packages: make([]PackageMetadataItem, 0),
	}

	store, err := SetupStore()
	if err != nil {
		return PackageMetadata{}, err
	}

	// Tech debt: in this function, we use both the storageclient (for
	// resolving pullspecs and fetching OCIImageConfigs) that uses
	// storage.Store internally and the raw storage.Store struct. This was
	// done for ease of testing some features via a mock client. Ideally we
	// would only have the storageclient implementation, so we had full control
	// over unit testing.
	storageClient := storageclient.NewBuildahClient(store)

	resolvedPullspecs, err := resolvePullspecs(storageClient, stages)
	if err != nil {
		return PackageMetadata{}, err
	}

	pkgSources, mountOnlySources, err := getPackageSources(storageClient, stages, resolvedPullspecs)
	if err != nil {
		return PackageMetadata{}, err
	}

	packages, err := scanWithMountReattribution(stages, pkgSources, mountOnlySources, store)
	if err != nil {
		return PackageMetadata{}, err
	}

	res.Packages = packages
	return res, nil
}

// resolvePullspecs uses the containers store to create a mapping between pullspecs
// used in the containerfile and pullspecs with resolved digest instead of tags.
// Resolved pullspecs in base images of stages and --from flags in copies within stages.
func resolvePullspecs(storageClient storageclient.Client, stages []containerfile.Stage) (map[string]string, error) {
	res := make(map[string]string)

	for _, stage := range stages[:len(stages)-1] {
		if storageclient.IsSpecialBase(stage.Base) {
			res[stage.Base] = stage.Base
			continue
		}
		if err := resolveIfNew(res, storageClient, stage.Base); err != nil {
			return nil, err
		}
	}

	for _, stage := range stages {
		for _, cp := range stage.Copies {
			if cp.Type == containerfile.CopyTypeBuilder {
				continue
			}
			if err := resolveIfNew(res, storageClient, cp.From); err != nil {
				return nil, err
			}
		}
		for _, mount := range stage.Mounts {
			if mount.Type == containerfile.MountTypeBuilder {
				continue
			}
			if err := resolveIfNew(res, storageClient, mount.From); err != nil {
				return nil, err
			}
		}
	}

	return res, nil
}

func resolveIfNew(res map[string]string, storageClient storageclient.Client, pullspec string) error {
	if _, ok := res[pullspec]; ok {
		return nil
	}
	resolved, err := resolvePullspec(storageClient, pullspec)
	if err != nil {
		return err
	}
	res[pullspec] = resolved
	return nil
}

// resolvePullspec uses the passed containers store to resolve a pullspec from a containerfile
// into a canonical pullspec with digest without tag. Transport prefixes (e.g. "docker://")
// are stripped and not included in the result.
func resolvePullspec(storageClient storageclient.Client, pullspec string) (string, error) {
	pullspec = storageclient.StripTransport(pullspec)

	digest, err := storageClient.ResolveDigest(pullspec)
	if err != nil {
		return "", fmt.Errorf("%w %q: %w", ErrPullspecResolve, pullspec, err)
	}

	ref, err := reference.ParseNamed(pullspec)
	if err != nil {
		return "", fmt.Errorf("%w %q: %w", ErrPullspecResolve, pullspec, err)
	}

	// remove tags if present and add the digest
	final, err := reference.WithDigest(reference.TrimNamed(ref), digest)
	if err != nil {
		return "", fmt.Errorf("%w %q: %w", ErrPullspecResolve, pullspec, err)
	}

	return final.String(), nil
}

// getPackageSources uses the passed containerfile stages and returns two slices
// of packageSource structs. The first slice contains sources whose findings are
// reported in the output. The second slice contains mount-only sources: builder
// stages referenced only via RUN --mount in non-final stages, scanned solely for
// re-attribution of packages to their mount origin. Uses the passed storageclient.Client to get
// OCIImageConfigs of base images to get their default workdirs for relative path
// resolution in copy destinations.
func getPackageSources(
	storageClient storageclient.Client,
	stages []containerfile.Stage,
	resolvedPullspecs map[string]string,
) ([]packageSource, []packageSource, error) {
	aliasToStage := make(map[string]*containerfile.Stage)
	for i := range stages[:len(stages)-1] {
		st := &stages[i]
		aliasToStage[st.Alias] = st
	}

	baseToWorkdir, err := resolveBaseWorkdirs(storageClient, stages)
	if err != nil {
		return nil, nil, err
	}

	stageToSources, mountStageSources := collectStageSources(stages, aliasToStage, baseToWorkdir)

	return buildPackageSourceSlices(stages, stageToSources, mountStageSources, resolvedPullspecs)
}

// resolveBaseWorkdirs builds a map from base image pullspec to its initial
// working directory, used for resolving relative COPY destinations.
func resolveBaseWorkdirs(
	storageClient storageclient.Client,
	stages []containerfile.Stage,
) (map[string]string, error) {
	baseToWorkdir := make(map[string]string)
	for _, s := range stages {
		if storageclient.IsSpecialBase(s.Base) {
			continue
		}

		cfg, err := storageClient.GetImageConfig(s.Base)
		if err != nil {
			return nil, fmt.Errorf("%w for %q", ErrOCIConfig, s.Base)
		}

		baseToWorkdir[s.Base] = cfg.Config.Workdir
	}
	return baseToWorkdir, nil
}

// traceSource takes a source path and the stage it was found in and recursively
// traces its origin up the builder stages. Once the true origin of the source
// path is found it modifies the passed accumulator so that pointers to stages map
// to the source paths that originated in them.
// aliasToStage is a mapping of stage aliases to stage pointers to use for lookups
// when resolving COPY commands.
// baseToWorkdir is a mapping of bases of stages in the containerfile and their
// respective initial working directories.
func traceSource(
	source string,
	currStage *containerfile.Stage,
	acc map[*containerfile.Stage][]string,
	aliasToStage map[string]*containerfile.Stage,
	baseToWorkdir map[string]string,
) {
	coversMultipleFiles := strings.HasSuffix(source, "/") || strings.ContainsAny(source, "*?[]")

	baseWorkdir, ok := baseToWorkdir[currStage.Base]
	// if unset, the default working directory in a stage is the root directory
	if !ok {
		baseWorkdir = "/"
	}

	foundAncestor := false
	for _, cp := range currStage.Copies {
		dest := cp.Destination
		if !filepath.IsAbs(cp.Destination) {
			if cp.Workdir != "" {
				dest = filepath.Join(cp.Workdir, cp.Destination)
			} else {
				dest = filepath.Join(baseWorkdir, cp.Destination)
			}
		}
		sourceCoversDestination := isPathUnderPattern(source, dest)
		destinationCoversSource := isPathUnderPattern(dest, source)
		if sourceCoversDestination || destinationCoversSource {
			foundAncestor = true
			if sourceCoversDestination && source != dest {
				// source covers destination but is not the same path, so it covers multiple files
				coversMultipleFiles = true
			}
			for _, s := range cp.Sources {
				traceSource(s, aliasToStage[cp.From], acc, aliasToStage, baseToWorkdir)
			}
		}
	}

	// If the source covers multiple files (directory, wildcard, or broader than
	// a matched COPY destination), add it to the accumulator even if we traced
	// some ancestors. The source could contain mixed content - some from this
	// stage, some copied from previous stages.
	if coversMultipleFiles || !foundAncestor {
		acc[currStage] = append(acc[currStage], source)
	}
}

// mergeStageSources merges mount-derived source paths into direct source paths,
// deduplicating entries. Returns nil if directSources is nil and there are no
// mount sources to merge.
func mergeStageSources(directSources, mountSources []string) []string {
	if mountSources == nil {
		return directSources
	}
	if directSources == nil {
		return nil
	}
	for _, s := range mountSources {
		if !slices.Contains(directSources, s) {
			directSources = append(directSources, s)
		}
	}
	return directSources
}

// scanSource uses the passed initialized storage.Store struct to syft scan content
// from the passed packageSource. Returns a slice of PackageMetadataItem structs specifying
// origins of packages.
func scanSource(
	store storage.Store,
	pkgSource packageSource,
) (_ []PackageMetadataItem, err error) {
	// builder content is content that is present in a builder stage base image
	builderContentPath, err := os.MkdirTemp("", "")
	if err != nil {
		return nil, fmt.Errorf("%w: failed to create temp directory: %w", ErrIO, err)
	}

	// intermediate content is content that created in a builder stage base during the build
	intermediateContentPath, err := os.MkdirTemp("", "")
	if err != nil {
		return nil, fmt.Errorf("%w: failed to create temp directory: %w", ErrIO, err)
	}

	// if in debug mode, print the paths to saved content
	// and don't remove the temporary directories
	debugMode := os.Getenv("CAPO_DEBUG") != ""
	if debugMode {
		log.Printf("[DEBUG] Builder %s content path: %s", pkgSource.pullspec, builderContentPath)
		log.Printf("[DEBUG] Intermediate %s content path: %s", pkgSource.pullspec, intermediateContentPath)
	} else {
		defer func() {
			removeErr := errors.Join(
				os.RemoveAll(builderContentPath),
				os.RemoveAll(intermediateContentPath),
			)
			if err == nil {
				err = removeErr
			}
		}()
	}

	err = getContent(store, pkgSource, builderContentPath, intermediateContentPath)
	if err != nil {
		return nil, err
	}

	intermediatePkgs, err := sbom.SyftScan(intermediateContentPath)
	if err != nil {
		return nil, fmt.Errorf("failed to scan intermediate content: %w", err)
	}

	builderPkgs, err := sbom.SyftScan(builderContentPath)
	if err != nil {
		return nil, fmt.Errorf("failed to scan builder content: %w", err)
	}

	return getPackageMetadata(
		pkgSource, builderPkgs, intermediatePkgs,
	), nil
}

// getPackageMetadata uses the passed packageSource and its builder and intermediate
// packages to return a slice of PackageMetadataItem structs to signify package origins.
// External sources (alias == "") use OriginType "external" for all packages.
func getPackageMetadata(
	pkgSource packageSource,
	builderPkgs []sbom.SyftPackage,
	intermediatePkgs []sbom.SyftPackage,
) []PackageMetadataItem {
	isExternal := pkgSource.alias == ""
	res := make([]PackageMetadataItem, 0, len(builderPkgs)+len(intermediatePkgs))

	for _, bpkg := range builderPkgs {
		originType := "builder"
		if isExternal {
			originType = "external"
		}
		res = append(res, PackageMetadataItem{
			Pullspec:         pkgSource.digestBase,
			StageAlias:       pkgSource.alias,
			PackageURL:       bpkg.PURL,
			DependencyOfPURL: bpkg.DependencyOfPURL,
			Checksums:        bpkg.Checksums,
			OriginType:       originType,
		})
	}

	for _, ipkg := range intermediatePkgs {
		originType := "intermediate"
		if isExternal {
			originType = "external"
		}
		res = append(res, PackageMetadataItem{
			Pullspec:         pkgSource.digestBase,
			StageAlias:       pkgSource.alias,
			PackageURL:       ipkg.PURL,
			DependencyOfPURL: ipkg.DependencyOfPURL,
			Checksums:        ipkg.Checksums,
			OriginType:       originType,
		})
	}

	return res
}

// mountConsumer identifies a specific package within a consuming stage,
// used to look up whether it came from a mounted source.
type mountConsumer struct {
	stageAlias string
	packageURL string
}

// mountOrigin holds the mount source's identity for a package. When a stage
// uses RUN --mount and we find the same package in both the stage's
// intermediate layer and the mount source, this struct tells us where the
// package actually came from so we can correct its reported origin.
type mountOrigin struct {
	digestBase         string
	providerStageAlias string
	originType         string
	// When true, the mount source is already in the reportable results,
	// so we deduplicate (drop) the consuming stage's copy. When false,
	// we overwrite the consuming stage's origin fields with the mount source's.
	reported bool
}

// scanWithMountReattribution scans all package sources and applies mount
// re-attribution. For each stage with RUN --mount, intermediate packages
// matching a mount source are either re-attributed (origin details overwritten)
// or deduplicated (dropped), depending on whether the mount source is already
// in pkgSources (directly reported) or only in mountOnlySources.
func scanWithMountReattribution(
	stages []containerfile.Stage,
	pkgSources []packageSource,
	mountOnlySources []packageSource,
	store storage.Store,
) ([]PackageMetadataItem, error) {
	// Map mount reference (stage alias or pullspec) to the aliases of
	// stages that consume it via RUN --mount.
	consumers := make(map[string][]string)
	for _, stage := range stages {
		for _, mount := range stage.Mounts {
			consumers[mount.From] = append(consumers[mount.From], stage.Alias)
		}
	}

	origins := make(map[mountConsumer]mountOrigin)

	// Scan mount-only sources and register their packages (reported=false).
	for _, src := range mountOnlySources {
		items, err := scanSource(store, src)
		if err != nil {
			return nil, err
		}
		registerMountPackages(origins, consumers, src, items, false)
	}

	// Scan reportable sources, register their packages for mount lookups
	// (reported=true), and collect all scanned items per source.
	sourcesItems := make([][]PackageMetadataItem, 0, len(pkgSources))
	for _, src := range pkgSources {
		items, err := scanSource(store, src)
		if err != nil {
			return nil, err
		}
		registerMountPackages(origins, consumers, src, items, true)
		sourcesItems = append(sourcesItems, items)
	}

	// Apply re-attribution: for each intermediate package that matches a
	// mount origin, either overwrite its origin fields or drop it.
	var packages []PackageMetadataItem
	for i, src := range pkgSources {
		for _, item := range sourcesItems[i] {
			if item.OriginType != "intermediate" {
				packages = append(packages, item)
				continue
			}
			origin, fromMount := origins[mountConsumer{stageAlias: src.alias, packageURL: item.PackageURL}]
			if !fromMount {
				packages = append(packages, item)
				continue
			}
			if origin.reported {
				log.Printf("Deduplicating intermediate package %s from stage %q (mount source already reported).",
					item.PackageURL, src.alias)
				continue
			}
			log.Printf("Re-attributing intermediate package %s from stage %q to mount source %q.",
				item.PackageURL, src.alias, origin.providerStageAlias)
			item.Pullspec = origin.digestBase
			item.StageAlias = origin.providerStageAlias
			item.OriginType = origin.originType
			packages = append(packages, item)
		}
	}

	return packages, nil
}

// registerMountPackages populates the origins map for a single scan result
// that may be referenced as a mount source. For each package in the result,
// it is registered under every consuming stage that mounts from this source.
func registerMountPackages(
	origins map[mountConsumer]mountOrigin,
	consumers map[string][]string,
	source packageSource,
	items []PackageMetadataItem,
	reported bool,
) {
	refKey := source.alias
	if refKey == "" {
		refKey = source.pullspec
	}
	stageAliases := consumers[refKey]
	if len(stageAliases) == 0 {
		return
	}
	for _, stageAlias := range stageAliases {
		for _, pkg := range items {
			key := mountConsumer{stageAlias: stageAlias, packageURL: pkg.PackageURL}
			if _, exists := origins[key]; !exists {
				origins[key] = mountOrigin{
					digestBase:         source.digestBase,
					providerStageAlias: source.alias,
					originType:         pkg.OriginType,
					reported:           reported,
				}
			}
		}
	}
}

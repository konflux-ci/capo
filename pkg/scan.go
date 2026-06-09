package capo

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/konflux-ci/capo/internal/sbom"
	"github.com/konflux-ci/capo/pkg/containerfile"
	"github.com/konflux-ci/capo/pkg/storageclient"

	"github.com/opencontainers/go-digest"
	"go.podman.io/image/v5/docker/reference"
	"go.podman.io/storage"
	"go.podman.io/storage/pkg/reexec"
)

// BuilderPackageSourceRoot represents a non-chained builder stage or the root of a
// chain of stages that share the same external base image. Chained stages
// (FROM parent-alias AS child-alias) are attached as descendants.
type BuilderPackageSourceRoot struct {
	// Index of this builder stage.
	index int
	// Stage alias.
	alias string
	// Base image pullspec as it appeared in the containerfile.
	pullspec string
	// Base image pullspec with resolved digest.
	digestBase string
	// Paths to content that should be syft-scanned.
	sources []string
	// Chained stages that use this stage (or its descendants) as base.
	descendants []*BuilderPackageSourceNode
}

// BuilderPackageSourceNode represents a chained builder stage - a descendant of a
// BuilderPackageSourceRoot or another BuilderPackageSourceNode.
type BuilderPackageSourceNode struct {
	// Index of this builder stage.
	index int
	// Stage alias.
	alias string
	// Paths to content that should be syft-scanned.
	sources []string
	// Further chained stages.
	descendants []*BuilderPackageSourceNode
}

// ExternalPackageSource is used for external image sources (COPY --from=image:tag)
// that are not builder stages.
type ExternalPackageSource struct {
	// Base image pullspec as it appeared in the COPY --from=pullspec instruction.
	pullspec string
	// Base image pullspec with resolved digest.
	digestBase string
	// Paths to content that should be syft-scanned.
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

	// Type of origin of this package, can be "builder", "intermediate" or "external".
	OriginType string `json:"origin_type"`

	// Pullspec of the image with digest which is this package's origin.
	Pullspec string `json:"pullspec"`

	// Alias of the stage of this package's origin.
	// Omitted if this package is from an external image.
	StageAlias string `json:"stage_alias,omitempty"`
}

var ErrStorageSetup = errors.New("[ERR_STORAGE_SETUP] failed to set up container storage")
var ErrPullspecResolve = errors.New("[ERR_PULLSPEC_RESOLVE] failed to resolve pullspec")
var ErrOCIConfig = errors.New("[ERR_OCI_CONFIG] failed to get OCI image config")
var ErrSBOMScan = errors.New("[ERR_SBOM_SCAN] SBOM scan failed")
var ErrUnsupportedMount = errors.New("[ERR_UNSUPPORTED_MOUNT] unsupported mount type")

// ErrDuplicateAlias is returned when two stages in a Containerfile share
// the same alias. Buildah behavior for duplicate aliases is undefined
// (see https://github.com/containers/buildah/issues/6731), so capo skips
// builder content identification to avoid producing incorrect results.
var ErrDuplicateAlias = errors.New("[ERR_DUPLICATE_ALIAS] duplicate stage alias")

// Scanner exposes methods used for scanning of buildah image builds, assigning
// image origins to SBOM packages present in a built image.
type Scanner struct {
	logger  *slog.Logger
	sclient storageclient.Client
	store   storage.Store
}

// Enable Scanner to use the functional options pattern for configuration
type Option func(*Scanner)

// Configure the Scanner to use the passed *slog.Logger for its logging.
func WithLogger(l *slog.Logger) Option {
	return func(s *Scanner) {
		s.logger = l
	}
}

// Create a new Scanner with the specified options or fail if an error occurred
// while trying to set up the containers/storage store.
func NewScanner(opts ...Option) (*Scanner, error) {
	// Tech debt: Scanner uses both the storageclient (for
	// resolving pullspecs and fetching OCIImageConfigs) that uses
	// storage.Store internally and the raw storage.Store struct. This was
	// done for ease of testing some features via a mock client. Ideally we
	// would only have the storageclient implementation, so we had full control
	// over unit testing.
	store, err := setupStore()
	if err != nil {
		return nil, err
	}

	sclient := storageclient.NewBuildahClient(store)

	s := &Scanner{
		logger:  slog.Default(),
		sclient: sclient,
		store:   store,
	}

	for _, o := range opts {
		o(s)
	}

	return s, nil
}

func setupStore() (storage.Store, error) {
	// The containers/storage library requires this to run for some operations
	if reexec.Init() {
		return nil, fmt.Errorf("failed to init reexec: %w", ErrStorageSetup)
	}

	opts, err := storage.DefaultStoreOptions()
	if err != nil {
		return nil, fmt.Errorf("failed to create default storage options: %w: %w", err, ErrStorageSetup)
	}

	store, err := storage.GetStore(opts)
	if err != nil {
		return nil, fmt.Errorf("failed to create storage: %w: %w", err, ErrStorageSetup)
	}

	return store, nil
}

func checkUnsupportedFeatures(stages []containerfile.Stage) error {
	seenAliases := make(map[string]bool)
	for _, stage := range stages {
		if stage.Index != -1 && seenAliases[stage.Alias] {
			return fmt.Errorf(
				"stage alias %q is used more than once: %w",
				stage.Alias, ErrDuplicateAlias,
			)
		}
		seenAliases[stage.Alias] = true

		for _, mount := range stage.Mounts {
			if mount.MountType == containerfile.MountTypeBind && mount.FromRaw != "" {
				return fmt.Errorf(
					"builder content resolution is unsupported for RUN --mount=type=bind: %w",
					ErrUnsupportedMount,
				)
			}
		}
	}
	return nil
}

// Scan reads the passed containerfile stages, resolves true content origin,
// extracts relevant content from buildah storage and scans it using syft.
// Returns a PackageMetadata struct containing packages and their origin information
// for resolution by Mobster.
func (s *Scanner) Scan(
	cf containerfile.Containerfile,
) (PackageMetadata, error) {
	if err := checkUnsupportedFeatures(cf.Stages); err != nil {
		return PackageMetadata{}, err
	}

	res := PackageMetadata{
		Packages: make([]PackageMetadataItem, 0),
	}
	s.logger.Debug("parsed containerfile stages", "stages", cf.Stages)

	digests, err := getImageDigests(s.sclient, cf.Stages)
	if err != nil {
		return PackageMetadata{}, err
	}

	roots, externals, err := getPackageSources(s.sclient, cf.Stages, digests)
	if err != nil {
		return PackageMetadata{}, err
	}

	for _, root := range roots {
		rootPkgItems, err := s.scanBuilderStageTree(root)
		if err != nil {
			return PackageMetadata{}, fmt.Errorf("failed to scan tree for stage %q: %w", root.alias, err)
		}
		res.Packages = append(res.Packages, rootPkgItems...)
	}

	for _, ext := range externals {
		extPkgItems, err := s.scanExternalSource(ext)
		if err != nil {
			return PackageMetadata{}, fmt.Errorf("failed to scan external source %+v: %w", ext, err)
		}
		res.Packages = append(res.Packages, extPkgItems...)
	}

	return res, nil
}

// Map all pullspecs found in the containerfile to their current digests in
// container storage. Chained stages are skipped (their Base is already the
// root pullspec, resolved by the parser).
func getImageDigests(
	storageClient storageclient.Client, stages []containerfile.Stage,
) (map[string]digest.Digest, error) {
	res := make(map[string]digest.Digest)

	for _, stage := range stages[:len(stages)-1] {
		// This deduplication check covers both duplicate pullspecs across
		// the containerfile and implicitly skips chained stages (their root
		// stage already resolved the shared base pullspec).
		if _, ok := res[stage.Base]; !ok {
			if storageclient.IsSpecialBase(stage.Base) {
				continue
			}

			dig, err := storageClient.ResolveDigest(stage.Base)
			if err != nil {
				return res, fmt.Errorf("failed to resolve pullspec %q: %w: %w", stage.Base, err, ErrPullspecResolve)
			}

			res[stage.Base] = dig
		}
	}

	for _, stage := range stages {
		for _, cp := range stage.Copies {
			if cp.Type == containerfile.CopyTypeBuilder {
				continue
			}

			if _, ok := res[cp.From]; !ok {
				dig, err := storageClient.ResolveDigest(cp.From)
				if err != nil {
					return res, fmt.Errorf("failed to resolve pullspec %q: %w: %w", cp.From, err, ErrPullspecResolve)
				}

				res[cp.From] = dig
			}
		}
	}

	return res, nil
}

// Attach a digest to a pullspec while removing the tag. Can fail if the passed
// pullspec or digest are not structurally valid.
func attachDigest(pullspec string, dig digest.Digest) (string, error) {
	ref, err := reference.ParseNamed(pullspec)
	if err != nil {
		return "", fmt.Errorf("failed to parse image reference %q: %w: %w", pullspec, err, ErrPullspecResolve)
	}

	// remove tags if present and add the digest
	final, err := reference.WithDigest(reference.TrimNamed(ref), dig)
	if err != nil {
		return "", fmt.Errorf("failed to attach digest to %q: %w: %w", pullspec, err, ErrPullspecResolve)
	}

	return final.String(), nil
}

// getPackageSources traces content origins from the final stage through builder
// stages and returns a tree of BuilderPackageSourceRoot (one per non-chained builder
// stage) with chained stages attached as BuilderPackageSourceNode descendants.
// External COPY --from sources are returned separately.
// Uses the passed storageclient.Client to get OCIImageConfigs of base images
// to get their default workdirs for relative path resolution in copy destinations.
func getPackageSources(
	storageClient storageclient.Client,
	stages []containerfile.Stage,
	digests map[string]digest.Digest,
) ([]BuilderPackageSourceRoot, []ExternalPackageSource, error) {
	// lookup maps for traceSource
	aliasToIndex := make(map[string]int)
	indexToStage := make(map[int]*containerfile.Stage)
	for i := range stages[:len(stages)-1] {
		st := &stages[i]
		aliasToIndex[st.Alias] = st.Index
		indexToStage[st.Index] = st
		// also map numeric index so COPY --from=<index> resolves
		// correctly even when stage aliases are present
		aliasToIndex[strconv.Itoa(i)] = st.Index
	}

	// mapping of bases used in the containerfile to their initial working
	// directories
	baseToWorkdir := make(map[string]string)
	for _, s := range stages[:len(stages)-1] {
		if storageclient.IsSpecialBase(s.Base) {
			continue
		}

		cfg, err := storageClient.GetImageConfig(s.Base)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to get OCI image config for %q: %w", s.Base, ErrOCIConfig)
		}

		baseToWorkdir[s.Base] = cfg.Config.Workdir
	}

	// The following code block reads all the builder COPY-ies in the final stage
	// and recursively traces their content to their respective origins in previous stages.
	// Builds a map between stage indices and the source paths that originated in them.
	final := &stages[len(stages)-1]
	builderStageAcc := make(map[int][]string)
	externalAcc := make(map[string][]string)

	for _, cp := range final.Copies {
		for _, source := range cp.Sources {
			// the copy is builder type only if there's no builder stage with alias equal to the cp.from
			// otherwise the cp.from is a pullspec and it is an external copy
			// Multiple copies from same external image (multiple COPY instructions referencing same image,
			// not sources) are grouped under same pullspec.
			if idx, isBuilder := aliasToIndex[cp.From]; isBuilder {
				traceSource(source, idx, builderStageAcc, indexToStage, aliasToIndex, externalAcc, baseToWorkdir)
			} else {
				externalAcc[cp.From] = append(externalAcc[cp.From], source)
			}
		}
	}

	roots, err := buildSourceTree(stages, builderStageAcc, aliasToIndex, digests)
	if err != nil {
		return nil, nil, err
	}

	// construct external package sources
	externals := make([]ExternalPackageSource, 0)
	for pullspec, sources := range externalAcc {
		dig, exists := digests[pullspec]
		var digestBase string
		if exists {
			var err error
			digestBase, err = attachDigest(storageclient.StripTransport(pullspec), dig)
			if err != nil {
				return nil, nil, err
			}
		} else {
			digestBase = pullspec
		}

		externals = append(externals, ExternalPackageSource{
			pullspec:   pullspec,
			digestBase: digestBase,
			sources:    sources,
		})
	}

	return roots, externals, nil
}

// buildSourceTree constructs a tree of BuilderPackageSourceRoot (non-chained stages)
// with BuilderPackageSourceNode descendants (chained stages) from the traced sources.
func buildSourceTree(
	stages []containerfile.Stage,
	builderStageAcc map[int][]string,
	aliasToIndex map[string]int,
	digests map[string]digest.Digest,
) ([]BuilderPackageSourceRoot, error) {
	rootByIndex := make(map[int]*BuilderPackageSourceRoot)
	nodeByIndex := make(map[int]*BuilderPackageSourceNode)

	for _, builderStage := range stages[:len(stages)-1] {
		isChained := builderStage.Base != builderStage.BaseRef
		sources := builderStageAcc[builderStage.Index]

		if !isChained {
			dig, exists := digests[builderStage.Base]
			var digestBase string
			if exists {
				var err error
				digestBase, err = attachDigest(storageclient.StripTransport(builderStage.Base), dig)
				if err != nil {
					return nil, err
				}
			} else {
				digestBase = builderStage.Base
			}

			root := &BuilderPackageSourceRoot{
				index:      builderStage.Index,
				alias:      builderStage.Alias,
				pullspec:   builderStage.Base,
				digestBase: digestBase,
				sources:    sources,
			}
			rootByIndex[builderStage.Index] = root
		} else {
			node := &BuilderPackageSourceNode{
				index:   builderStage.Index,
				alias:   builderStage.Alias,
				sources: sources,
			}
			nodeByIndex[builderStage.Index] = node

			// attach to parent — parent can be a root or another node
			parentIdx := aliasToIndex[builderStage.BaseRef]
			if parentRoot, ok := rootByIndex[parentIdx]; ok {
				parentRoot.descendants = append(parentRoot.descendants, node)
			} else if parentNode, ok := nodeByIndex[parentIdx]; ok {
				parentNode.descendants = append(parentNode.descendants, node)
			}
		}
	}

	// collect roots in stage order after building the tree (rootByIndex is a map
	// which does not guarantee iteration order)
	roots := make([]BuilderPackageSourceRoot, 0, len(rootByIndex))
	for _, stage := range stages[:len(stages)-1] {
		if root, ok := rootByIndex[stage.Index]; ok {
			roots = append(roots, *root)
		}
	}

	return roots, nil
}

// traceSource recursively traces a source path through builder stage COPY
// commands to find its true origin. Maps stage indices to source paths in acc.
// External COPY --from references in builder stages are collected in externalAcc.
// baseToWorkdir is a mapping of bases of stages in the containerfile and their
// respective initial working directories.
func traceSource(
	source string,
	stageIndex int,
	acc map[int][]string,
	indexToStage map[int]*containerfile.Stage,
	aliasToIndex map[string]int,
	externalAcc map[string][]string,
	baseToWorkdir map[string]string,
) {
	currStage := indexToStage[stageIndex]
	coversMultipleFiles := strings.HasSuffix(source, "/") || strings.ContainsAny(source, "*?[]")

	baseWorkdir, ok := baseToWorkdir[currStage.Base]
	// if unset, the default working directory in a stage is the root directory
	if !ok {
		baseWorkdir = "/"
	}

	foundAncestor := false
	for _, cp := range currStage.Copies {
		dest := ""
		if filepath.IsAbs(cp.Destination) {
			dest = cp.Destination
		} else {
			dest = resolveRelativeDestination(cp, baseWorkdir)
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
				if nextIdx, ok := aliasToIndex[cp.From]; ok {
					// builder stage - continue tracing
					traceSource(s, nextIdx, acc, indexToStage, aliasToIndex, externalAcc, baseToWorkdir)
				} else {
					// external image - add as external source
					externalAcc[cp.From] = append(externalAcc[cp.From], s)
				}
			}
		}
	}

	// If the source covers multiple files (directory, wildcard, or broader than
	// a matched COPY destination), add it to the accumulator even if we traced
	// some ancestors. The source could contain mixed content - some from this
	// stage, some copied from previous stages.
	if coversMultipleFiles || !foundAncestor {
		acc[stageIndex] = append(acc[stageIndex], source)
	}

	// chained stage — propagate source to parent for builder content scanning
	if parentIdx, ok := aliasToIndex[currStage.BaseRef]; ok {
		traceSource(source, parentIdx, acc, indexToStage, aliasToIndex, externalAcc, baseToWorkdir)
	}
}

// Get the true destination of a COPY command, resolving relative paths.
// cp is the copy command to resolve the destination of.
// baseWorkdir is the working directory of the base image the COPY command
// appeared in.
func resolveRelativeDestination(cp containerfile.Copy, baseWorkdir string) string {
	// If no WORKDIR command precedes the COPY, its destination is relative to
	// the base image working directory.
	if cp.Workdir == "" {
		return filepath.Join(baseWorkdir, cp.Destination)
	}

	// If an absolute WORKDIR command precedes the COPY, the destination is
	// relative to that WORKDIR.
	if filepath.IsAbs(cp.Workdir) {
		return filepath.Join(cp.Workdir, cp.Destination)
	}

	// If the WORKDIR command preceding the COPY contained a relative path, and
	// the COPY's destination is relative, we join all three paths to get the
	// absolute path.
	// This is possible because the Workdir field always contains a relative to
	// the stage's working directory.
	return filepath.Join(baseWorkdir, cp.Workdir, cp.Destination)
}

// scanBuilderStageTree scans a BuilderPackageSourceRoot and all its descendants. For the root,
// both builder base content and intermediate content are extracted. For
// descendants, only intermediate content is extracted (diffed against parent's
// intermediate layer, or builder base if parent has no intermediate).
func (s *Scanner) scanBuilderStageTree(
	root BuilderPackageSourceRoot,
) ([]PackageMetadataItem, error) {
	res := make([]PackageMetadataItem, 0)

	// root scan
	rootItems, err := s.scanSource(root.pullspec, root.alias, root.digestBase, root.sources)
	if err != nil {
		return nil, err
	}
	res = append(res, rootItems...)

	// root's chain descendants scan
	if len(root.descendants) > 0 {
		// Resolve the initial diff base for descendants. Descendants diff their
		// intermediate image against the nearest ancestor with an intermediate.
		// If nearest ancestor has an intermediate, use it; otherwise fall back
		// to its builder base image.
		imgId, err := s.store.Lookup(storageclient.StripTransport(root.pullspec))
		if err != nil {
			return nil, fmt.Errorf("could not find image %q in buildah storage: %w", root.pullspec, ErrImageNotFound)
		}
		builderBaseImage, err := s.store.Image(imgId)
		if err != nil {
			return nil, fmt.Errorf("could not find image %q in buildah storage: %w", root.pullspec, ErrImageNotFound)
		}

		// root's intermediate image — use as initial diff base if it exists
		rootDiffBase := builderBaseImage
		rootIntermediate, found, _ := s.findIntermediateImage(root.alias)
		if found {
			rootDiffBase = rootIntermediate
		}

		// scan direct chained children; scanDescendants recurses into their subtrees.
		// A root can have multiple direct descendants, e.g.:
		//   FROM fedora AS root
		//   FROM root AS left  - descendant1
		//   FROM root AS right - descendant2
		for _, desc := range root.descendants {
			descItems, err := s.scanDescendants(desc, rootDiffBase, root.digestBase)
			if err != nil {
				return nil, err
			}
			res = append(res, descItems...)
		}
	}

	return res, nil
}

// scanDescendants recursively scans chained stage descendants, extracting
// only intermediate content (diffed against diffBase - the nearest ancestor's
// intermediate image or the builder base image).
func (s *Scanner) scanDescendants(
	node *BuilderPackageSourceNode,
	diffBase *storage.Image,
	rootDigestBase string,
) ([]PackageMetadataItem, error) {
	res := make([]PackageMetadataItem, 0)

	intermediateContentPath, err := os.MkdirTemp("", "")
	if err != nil {
		return nil, fmt.Errorf("%w: failed to create temp directory: %w", ErrIO, err)
	}
	defer func() { _ = os.RemoveAll(intermediateContentPath) }()

	// getDescendantContent returns the intermediate image for this node
	// (or diffBase unchanged if node has no intermediate = empty stage)
	nextDiffBase, intermediate, err := s.getDescendantContent(
		node.alias, diffBase, node.sources, intermediateContentPath,
	)
	if err != nil {
		return nil, err
	}

	if len(intermediate) > 0 {
		s.logContent("intermediate (chained)", intermediate, node.alias)

		intermediatePkgs, err := sbom.SyftScan(intermediateContentPath)
		if err != nil {
			return nil, fmt.Errorf("failed to scan intermediate content for %q: %w", node.alias, err)
		}

		for _, ipkg := range intermediatePkgs {
			res = append(res, PackageMetadataItem{
				Pullspec:         rootDigestBase,
				StageAlias:       node.alias,
				PackageURL:       ipkg.PURL,
				DependencyOfPURL: ipkg.DependencyOfPURL,
				Checksums:        ipkg.Checksums,
				OriginType:       "intermediate",
			})
		}
	}

	// recurse into further chained stages, e.g.:
	//   FROM root AS left    ← current node
	//   FROM left AS child1
	//   FROM left AS child2
	for _, child := range node.descendants {
		childItems, err := s.scanDescendants(child, nextDiffBase, rootDigestBase)
		if err != nil {
			return nil, err
		}
		res = append(res, childItems...)
	}

	return res, nil
}

// scanExternalSource scans content from an external image (COPY --from=image:tag).
// External images have no intermediate layer - only the image content is extracted
// and reported with origin_type "external".
func (s *Scanner) scanExternalSource(
	ext ExternalPackageSource,
) (_ []PackageMetadataItem, err error) {
	contentPath, err := os.MkdirTemp("", "")
	if err != nil {
		return nil, fmt.Errorf("failed to create temp directory: %w: %w", err, ErrIO)
	}

	debugMode := os.Getenv("CAPO_DEBUG") != ""
	if debugMode {
		s.logger.Debug("external content path", "pullspec", ext.pullspec, "path", contentPath)
	} else {
		defer func() {
			removeErr := os.RemoveAll(contentPath)
			if err == nil {
				err = removeErr
			}
		}()
	}

	err = s.getExternalContent(ext.pullspec, ext.sources, contentPath)
	if err != nil {
		return nil, err
	}

	pkgs, err := sbom.SyftScan(contentPath)
	if err != nil {
		return nil, fmt.Errorf("failed to scan external content: %w: %w", err, ErrSBOMScan)
	}

	return getPackageMetadata("", ext.digestBase, "external", pkgs, nil), nil
}

// scanSource extracts content for a stage from buildah storage, scans it
// with syft, and returns package metadata items.
func (s *Scanner) scanSource(
	pullspec string,
	stageAlias string,
	digestBase string,
	sources []string,
) (_ []PackageMetadataItem, err error) {
	// builder content is content that is present in a builder stage base image
	builderContentPath, err := os.MkdirTemp("", "")
	if err != nil {
		return nil, fmt.Errorf("failed to create temp directory: %w: %w", err, ErrIO)
	}

	// intermediate content is content that created in a builder stage base during the build
	intermediateContentPath, err := os.MkdirTemp("", "")
	if err != nil {
		return nil, fmt.Errorf("failed to create temp directory: %w: %w", err, ErrIO)
	}

	// if in debug mode, print the paths to saved content
	// and don't remove the temporary directories
	debugMode := os.Getenv("CAPO_DEBUG") != ""
	if debugMode {
		s.logger.Debug("builder content path", "pullspec", pullspec, "path", builderContentPath)
		s.logger.Debug("intermediate content path", "pullspec", pullspec, "path", intermediateContentPath)
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

	err = s.getContent(pullspec, stageAlias, sources, builderContentPath, intermediateContentPath)
	if err != nil {
		return nil, err
	}

	intermediatePkgs, err := sbom.SyftScan(intermediateContentPath)
	if err != nil {
		return nil, fmt.Errorf("failed to scan intermediate content: %w: %w", err, ErrSBOMScan)
	}

	builderPkgs, err := sbom.SyftScan(builderContentPath)
	if err != nil {
		return nil, fmt.Errorf("failed to scan builder content: %w: %w", err, ErrSBOMScan)
	}

	return getPackageMetadata(
		stageAlias, digestBase, "builder", builderPkgs, intermediatePkgs,
	), nil
}

// getPackageMetadata maps scanned packages to PackageMetadataItem structs
// with the given origin information.
func getPackageMetadata(
	stageAlias string,
	digestBase string,
	builderOriginType string,
	builderPkgs []sbom.SyftPackage,
	intermediatePkgs []sbom.SyftPackage,
) []PackageMetadataItem {
	res := make([]PackageMetadataItem, 0, len(builderPkgs)+len(intermediatePkgs))

	for _, bpkg := range builderPkgs {
		res = append(res, PackageMetadataItem{
			Pullspec:         digestBase,
			StageAlias:       stageAlias,
			PackageURL:       bpkg.PURL,
			DependencyOfPURL: bpkg.DependencyOfPURL,
			Checksums:        bpkg.Checksums,
			OriginType:       builderOriginType,
		})
	}

	for _, ipkg := range intermediatePkgs {
		res = append(res, PackageMetadataItem{
			Pullspec:         digestBase,
			StageAlias:       stageAlias,
			PackageURL:       ipkg.PURL,
			DependencyOfPURL: ipkg.DependencyOfPURL,
			Checksums:        ipkg.Checksums,
			OriginType:       "intermediate",
		})
	}

	return res
}

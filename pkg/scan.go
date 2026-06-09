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

	// Type of origin of this package, can be "builder" or "intermediate".
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

// Scanner exposes methods used for scanning of buildah image builds, assigning
// image origins to SBOM packages present in a built image.
type Scanner struct {
	logger *slog.Logger
	sclient storageclient.Client
	store storage.Store
}

// Enable Scanner to use the functional options pattern for configuration
type Option func(*Scanner)

// Configure the Scanner to use the passed *slog.Logger for its logging.
func WithLogger(l *slog.Logger) Option {
	return func (s *Scanner) {
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

// Scan reads the passed containerfile stages, resolves true content origin,
// extracts relevant content from buildah storage and scans it using syft.
// Returns a PackageMetadata struct containing packages and their origin information
// for resolution by Mobster.
func (s *Scanner) Scan(
	cf containerfile.Containerfile,
) (PackageMetadata, error) {
	res := PackageMetadata{
		Packages: make([]PackageMetadataItem, 0),
	}
	s.logger.Debug("parsed containerfile stages", "stages", cf.Stages)

	digests, err := getImageDigests(s.sclient, cf.Stages)
	if err != nil {
		return PackageMetadata{}, err
	}

	pkgSources, err := getPackageSources(s.sclient, cf.Stages, digests)
	if err != nil {
		return PackageMetadata{}, err
	}
	for _, pkgSource := range pkgSources {
		stagePkgItems, err := s.scanSource(pkgSource)
		if err != nil {
			return PackageMetadata{}, fmt.Errorf("failed to scan source for stage %q: %w: %w", pkgSource.alias, err, ErrSBOMScan)
		}

		res.Packages = append(res.Packages, stagePkgItems...)
	}

	return res, nil
}

// Map all pullspecs found in the containerfile to their current digests in
// container storage.
func getImageDigests(
	storageClient storageclient.Client, stages []containerfile.Stage,
) (map[string]digest.Digest, error) {
	res := make(map[string]digest.Digest)

	for _, stage := range stages[:len(stages)-1] {
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

// getPackageSources uses the passed containerfile stages and returns a slice
// of packageSource structs, specifying which COPY-ied content originates from
// which builder stage. Uses the passed storageclient.Client to get
// OCIImageConfigs of base images to get their default workdirs for relative
// path resolution in copy destinations.
func getPackageSources(
	storageClient storageclient.Client,
	stages []containerfile.Stage,
	digests map[string]digest.Digest,
) ([]packageSource, error) {
	res := make([]packageSource, 0)
	aliasToStage := make(map[string]*containerfile.Stage)

	// use index iteration to get consistent references to stages
	// since we use the references as map keys
	for i := range stages[:len(stages)-1] {
		st := &stages[i]
		aliasToStage[st.Alias] = st
		// also map numeric index so COPY --from=<index> resolves
		// correctly even when stage aliases are present
		aliasToStage[strconv.Itoa(i)] = st
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
			return []packageSource{}, fmt.Errorf("failed to get OCI image config for %q: %w", s.Base, ErrOCIConfig)
		}

		baseToWorkdir[s.Base] = cfg.Config.Workdir
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
				traceSource(source, aliasToStage[cp.From], stageToSources, aliasToStage, baseToWorkdir)
			} else {
				external := containerfile.Stage{
					Alias:  "",
					Base:   cp.From,
					Copies: []containerfile.Copy{},
				}

				stageToSources[&external] = append(stageToSources[&external], source)
			}
		}
	}

	var err error
	pullspec := ""

	// construct builder package sources
	for i := range stages[:len(stages)-1] {
		stage := &stages[i]

		dig, exists := digests[stage.Base]
		if exists {
			pullspec, err = attachDigest(storageclient.StripTransport(stage.Base), dig)
			if err != nil {
				return nil, err
			}
		} else {
			pullspec = stage.Base
		}

		res = append(res, packageSource{
			alias:      stage.Alias,
			pullspec:   stage.Base,
			digestBase: pullspec,
			sources:    stageToSources[stage],
		})

		// the processed stage must be deleted from stageToSources so it only
		// contains "external" stages after builder stages are constructed.
		// These are then processed in the next code block below.
		delete(stageToSources, stage)
	}

	// construct external package sources
	for stage, sources := range stageToSources {
		dig, exists := digests[stage.Base]
		if exists {
			pullspec, err = attachDigest(storageclient.StripTransport(stage.Base), dig)
			if err != nil {
				return nil, err
			}
		} else {
			pullspec = stage.Base
		}

		res = append(res, packageSource{
			alias:      stage.Alias,
			pullspec:   stage.Base,
			digestBase: pullspec,
			sources:    sources,
		})
	}

	return res, nil
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

// scanSource uses the passed initialized storage.Store struct to syft scan content
// from the passed packageSource. Returns a slice of PackageMetadataItem structs specifying
// origins of packages.
func (s *Scanner) scanSource(
	pkgSource packageSource,
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
		s.logger.Debug("builder content path", "pullspec", pkgSource.pullspec, "path", builderContentPath)
		s.logger.Debug("intermediate content path", "pullspec", pkgSource.pullspec, "path", intermediateContentPath)
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

	err = s.getContent(pkgSource, builderContentPath, intermediateContentPath)
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
		pkgSource, builderPkgs, intermediatePkgs,
	), nil
}

// getPackageMetadata uses the passed packageSource and its builder and intermediate
// packages to return a slice of PackageMetadataItem structs to signify package origins.
func getPackageMetadata(
	pkgSource packageSource,
	builderPkgs []sbom.SyftPackage,
	intermediatePkgs []sbom.SyftPackage,
) []PackageMetadataItem {
	res := make([]PackageMetadataItem, 0, len(builderPkgs)+len(intermediatePkgs))

	for _, bpkg := range builderPkgs {
		res = append(res, PackageMetadataItem{
			Pullspec:         pkgSource.digestBase,
			StageAlias:       pkgSource.alias,
			PackageURL:       bpkg.PURL,
			DependencyOfPURL: bpkg.DependencyOfPURL,
			Checksums:        bpkg.Checksums,
			OriginType:       "builder",
		})
	}

	for _, ipkg := range intermediatePkgs {
		res = append(res, PackageMetadataItem{
			Pullspec:         pkgSource.digestBase,
			StageAlias:       pkgSource.alias,
			PackageURL:       ipkg.PURL,
			DependencyOfPURL: ipkg.DependencyOfPURL,
			Checksums:        ipkg.Checksums,
			OriginType:       "intermediate",
		})
	}

	return res
}

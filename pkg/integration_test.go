//go:build integration

package capo

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/google/uuid"
	"github.com/konflux-ci/capo/pkg/containerfile"
	"github.com/konflux-ci/capo/pkg/storageclient"
	"go.podman.io/storage"
	"go.podman.io/storage/pkg/reexec"
)

func TestMain(m *testing.M) {
	if reexec.Init() {
		os.Exit(0)
	}

	err := prepareBinaries()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to prepare binaries: %v\n", err)
		os.Exit(1)
	}

	os.Exit(m.Run())
}

// Walk the testdata/binaries to find go binaries to build. Builds each binary
// with "go build" and copies the built executables into testdata/image_content
// for use by integration tests.
func prepareBinaries() error {
	fmt.Println("Building testing binaries")
	binariesDir := filepath.Join("..", "testdata", "binaries")
	outputDir, err := filepath.Abs(filepath.Join("..", "testdata", "image_content"))
	if err != nil {
		return fmt.Errorf("resolving output directory: %w", err)
	}
	outputDir += "/"

	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return fmt.Errorf("creating output directory: %w", err)
	}

	entries, err := os.ReadDir(binariesDir)
	if err != nil {
		return fmt.Errorf("reading binaries directory: %w", err)
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		srcDir := filepath.Join(binariesDir, entry.Name())

		// -X main.Version=1.0.0 — sets a clean version string so we have clean PURLs for our binaries
		// -buildid= — strips the build ID, to remove the timestamp embedded in the binary metadata
		cmd := exec.Command("go", "build", "-ldflags", "-buildid= -X main.Version=1.0.0", "-o", outputDir, ".")
		cmd.Dir = srcDir
		if output, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("building %s: %s%w", entry.Name(), output, err)
		}
	}

	return nil
}

// withStore is a test-only option for using the same store as buildah.
func withStore(store storage.Store) Option {
	return func(s *Scanner) {
		s.store = store
	}
}

// buildahConfig contains values used for calling buildah during a test case
// setup
type buildahConfig struct {
	// The buildah binary to use. A custom binaryPath is required until
	// buildah 1.44.0 is generally available.
	binaryPath string
	// The environment variables in the form KEY=VALUE to pass to buildah
	// commands. Used for configuring buildah to use an isolated test container
	// storage.
	env []string
}

// testFixture contains a set of configuration and structs for executing a
// single test case
type testFixture struct {
	// Store for operations required to setup tests (e.g. building images)
	store storage.Store
	// Scanner to be used in a test
	scanner *Scanner
	// Buildah configuration to use for a test case
	bConfig buildahConfig
}

// newTestFixture gives buildah and the scanner the same private store for a case.
func newTestFixture(t *testing.T, buildahBinary string, opts ...Option) *testFixture {
	t.Helper()

	store, configPath := createTestStorage(t)
	return &testFixture{
		store:   store,
		scanner: createTestScanner(t, store, opts...),
		bConfig: buildahConfig{
			binaryPath: buildahBinary,
			env: []string{
				fmt.Sprintf("CONTAINERS_STORAGE_CONF=%s", configPath),
				fmt.Sprintf("STORAGE_DRIVER=%s", "overlay"),
			},
		},
	}
}

// createTestStorage creates a private overlay store for buildah and the scanner.
func createTestStorage(t *testing.T) (storage.Store, string) {
	t.Helper()

	// A short path avoids punctuation from test names in fuse-overlayfs mounts.
	storageDir, err := os.MkdirTemp("", "capo-store-")
	if err != nil {
		t.Fatalf("create test storage directory: %v", err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(storageDir); err != nil {
			t.Errorf("remove test storage directory: %v", err)
		}
	})
	const driver = "overlay"
	configPath := filepath.Join(storageDir, "storage.conf")
	graphRoot := filepath.Join(storageDir, "graph")
	runRoot := filepath.Join(storageDir, "run")
	conf := fmt.Sprintf("[storage]\ndriver = %q\ngraphroot = %q\nrunroot = %q\n",
		driver, graphRoot, runRoot)
	if err := os.WriteFile(configPath, []byte(conf), 0o600); err != nil {
		t.Fatalf("write test storage config: %v", err)
	}
	store, err := storage.GetStore(storage.StoreOptions{
		GraphRoot:       graphRoot,
		RunRoot:         runRoot,
		GraphDriverName: driver,
	})
	if err != nil {
		t.Fatalf("open test storage: %v", err)
	}
	t.Cleanup(func() {
		if _, err := store.Shutdown(true); err != nil {
			t.Errorf("shut down test storage: %v", err)
		}
	})
	return store, configPath
}

// createTestScanner injects the same store used by buildah into the scanner.
func createTestScanner(t *testing.T, store storage.Store, opts ...Option) *Scanner {
	t.Helper()

	scannerOpts := []Option{
		withStore(store),
		WithLogger(
			slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug})),
		),
	}
	scanner, err := NewScanner(append(scannerOpts, opts...)...)
	if err != nil {
		t.Fatalf("create test scanner: %v", err)
	}
	return scanner
}

// splitFilesystemTransport splits a filesystem transport reference into the
// transport prefix and the remaining path+reference. For example
// "oci-archive:image.tar:latest" returns ("oci-archive:", "image.tar:latest").
// Returns ("", pullspec) if the pullspec does not use a filesystem transport.
func splitFilesystemTransport(pullspec string) (transport, rest string) {
	for _, prefix := range []string{"oci-archive:", "docker-archive:", "oci:", "dir:"} {
		if after, ok := strings.CutPrefix(pullspec, prefix); ok {
			return prefix, after
		}
	}
	return "", pullspec
}

// filesystemTransportPath extracts the local file/directory path from a
// filesystem transport reference. For example "oci-archive:image.tar:latest"
// returns "image.tar".
func filesystemTransportPath(pullspec string) string {
	_, rest := splitFilesystemTransport(pullspec)
	path, _, _ := strings.Cut(rest, ":")
	return path
}

// BuildDefinition describes a container image to build for a test.
type BuildDefinition struct {
	// Tag is the image tag (e.g. "localhost/foo:latest").
	// Auto-normalized: adds "localhost/" if no registry, ":latest" if no tag.
	// Random UUID if empty.
	Tag string
	// ContainerfileContent is inline containerfile content. File paths will not work.
	ContainerfileContent string
	// ContextDirectory is the build context path relative to pkg/
	// (e.g. "../testdata/image_content").
	ContextDirectory string
	// Build contexts to pass to the build. Values representing local directory
	// paths are relative to pkg/
	BuildContexts map[string]string
}

// TestCase describes a single integration test: build images, scan, compare results.
type TestCase struct {
	// SkipTestReason, if non-empty, skips this test via t.Skip with the given reason.
	SkipTestReason string
	// TestImage is the multi-stage image to scan (built with --save-stages --stage-labels).
	TestImage BuildDefinition
	// BuilderImages are pre-built builder base images / external images referenced by TestImage.
	BuilderImages []BuildDefinition
	// ExpectedResult is the expected scan output for comparison with capo output.
	ExpectedResult PackageMetadata
	// Scanner options specific to this case, such as cataloger selection.
	ScannerOptions []Option
}

// getBuildahBinary returns the path to buildah binary to use for tests.
// Uses testdata/bin/buildah if it exists, otherwise falls back to system buildah.
func getBuildahBinary(t *testing.T) string {
	var binary string
	testdataBuildah := filepath.Join("..", "testdata", "bin", "buildah")
	if _, err := os.Stat(testdataBuildah); err == nil {
		binary, _ = filepath.Abs(testdataBuildah)
		t.Logf("Using custom buildah: %s", binary)
	} else {
		binary = "buildah"
		t.Logf("Using system buildah")
	}

	version, err := exec.Command(binary, "--version").Output()
	if err != nil {
		t.Fatalf("Could not determine buildah version: %v", err)
	}
	t.Logf("Buildah version: %s", strings.TrimSpace(string(version)))

	return binary
}

func (testCase *TestCase) build(fixture *testFixture) error {
	for _, builderImage := range testCase.BuilderImages {
		if err := builderImage.buildImage(fixture, true); err != nil {
			return err
		}
	}
	if err := testCase.TestImage.buildImage(fixture, false); err != nil {
		return err
	}
	return nil
}

func (testCase *TestCase) run(t *testing.T, fixture *testFixture) error {
	if err := testCase.build(fixture); err != nil {
		return err
	}

	cf, err := containerfile.Parse(
		strings.NewReader(testCase.TestImage.ContainerfileContent),
		containerfile.BuildOptions{
			BuildContexts: testCase.TestImage.BuildContexts,
		},
	)
	if err != nil {
		return err
	}

	result, err := fixture.scanner.Scan(cf)
	if err != nil {
		return err
	}

	// Compare packages order-independently using go-cmp:
	// - SortSlices: ensures comparison is order-independent by sorting on PackageURL
	// - EquateEmpty: treats nil and empty slices as equal
	// - FilterPath on Pullspec: strips @sha256: digests before comparing pullspecs,
	//   since actual digests vary between builds and should not cause test failures
	diff := cmp.Diff(testCase.ExpectedResult.Packages, result.Packages,
		cmpopts.SortSlices(func(a, b PackageMetadataItem) bool {
			if a.PackageURL != b.PackageURL {
				return a.PackageURL < b.PackageURL
			}
			return a.DependencyOfPURL < b.DependencyOfPURL
		}),
		cmpopts.EquateEmpty(),
		cmp.FilterPath(func(p cmp.Path) bool {
			return p.String() == "Pullspec"
		}, cmp.Comparer(func(a, b string) bool {
			return normalizePullspec(a) == normalizePullspec(b)
		})),
		cmp.FilterPath(func(p cmp.Path) bool {
			return p.String() == "PackageURL"
		}, cmp.Comparer(func(a, b string) bool {
			// we normalize go stdlib purls so mismatch between the
			// local and github versions doesn't cause issues in package
			// comparisons
			return normalizeStdlibPURL(a) == normalizeStdlibPURL(b)
		})),
	)
	if diff != "" {
		t.Errorf("package comparison mismatch (-want +got):\n%s", diff)
		return errors.New("package comparison failed")
	}
	return nil
}

// buildImage builds a container image from a containerfile using buildah.
// Skips building if the image already exists and isBuilder is true.
//
// When Tag uses a filesystem transport (e.g. "oci-archive:base.tar:latest"),
// the image is built with a temporary tag, pushed to a local file/directory
// inside the ContextDirectory using the original transport. This is needed because buildah requires
// filesystem transport paths to be relative to the build context.
func (buildDef *BuildDefinition) buildImage(fixture *testFixture, isBuilder bool) (err error) {
	tag := buildDef.Tag

	if storageclient.IsFilesystemTransport(tag) {
		return buildDef.buildFilesystemImage(fixture)
	}

	if _, err := fixture.store.Lookup(tag); err == nil && isBuilder {
		return nil
	}

	return buildDef.runBuildah(fixture.bConfig, tag, !isBuilder)
}

// buildFilesystemImage builds a temporary image from ContainerfileContent and
// pushes it to a local file/directory inside ContextDirectory using the
// transport from the Tag (e.g. "oci-archive:", "docker-archive:", "oci:", "dir:").
func (buildDef *BuildDefinition) buildFilesystemImage(fixture *testFixture) error {
	transport, rest := splitFilesystemTransport(buildDef.Tag)
	localPath := filesystemTransportPath(buildDef.Tag)
	fullPath := filepath.Join(buildDef.ContextDirectory, localPath)

	if _, err := os.Stat(fullPath); err == nil {
		return nil
	}

	tmpTag := "tmp-" + uuid.New().String()
	if err := buildDef.runBuildah(fixture.bConfig, tmpTag, false); err != nil {
		return err
	}

	pushDest := transport + fullPath
	if ref := strings.TrimPrefix(rest, localPath); ref != "" {
		pushDest += ref
	}

	cmd := exec.Command(fixture.bConfig.binaryPath, "push", tmpTag, pushDest)
	cmd.Env = append(os.Environ(), fixture.bConfig.env...)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("buildah push failed: %w", err)
	}
	return nil
}

func (buildDef *BuildDefinition) runBuildah(buildah buildahConfig, tag string, saveStages bool) (err error) {
	args := []string{
		"build",
		"-f",
		"-",
		"--tag",
		tag,
	}
	if saveStages {
		args = append(args, "--save-stages", "--stage-labels")
	}
	if buildDef.BuildContexts != nil {
		for k, v := range buildDef.BuildContexts {
			args = append(args, "--build-context", fmt.Sprintf("%s=%s", k, v))
		}
	}

	args = append(args, buildDef.ContextDirectory)

	cmd := exec.Command(buildah.binaryPath, args...)
	cmd.Stdin = strings.NewReader(buildDef.ContainerfileContent)
	cmd.Env = append(os.Environ(), buildah.env...)
	err_buf := bytes.NewBufferString("")
	cmd.Stderr = err_buf

	if err = cmd.Run(); err != nil {
		return fmt.Errorf("buildah build failed: %w: %q", err, err_buf)
	}

	return nil
}

const stdlibPURLPrefix = "pkg:golang/stdlib@"

func normalizeStdlibPURL(purl string) string {
	if strings.HasPrefix(purl, stdlibPURLPrefix) {
		return stdlibPURLPrefix
	}
	return purl
}

func normalizePullspec(pullspec string) string {
	if atIndex := strings.Index(pullspec, "@sha256:"); atIndex != -1 {
		return pullspec[:atIndex]
	}
	return pullspec
}

// buildDigestOnlyImage builds a builder image and renames it to a digest-only
// reference, simulating how buildah stores images pulled from a registry by
// digest.
// Returns the digest-only reference (e.g. "localhost/name@sha256:...").
func buildDigestOnlyImage(t *testing.T, def BuildDefinition, fixture *testFixture) string {
	t.Helper()
	if err := def.buildImage(fixture, true); err != nil {
		t.Fatalf("buildDigestOnlyImage: build failed: %v", err)
	}
	imgID, err := fixture.store.Lookup(def.Tag)
	if err != nil {
		t.Fatalf("buildDigestOnlyImage: lookup failed: %v", err)
	}
	img, err := fixture.store.Image(imgID)
	if err != nil {
		t.Fatalf("buildDigestOnlyImage: get image failed: %v", err)
	}
	// This avoids splitting on the port colon in "localhost:5000/name:tag".
	repo := def.Tag
	if idx := strings.LastIndex(repo, ":"); idx > strings.LastIndex(repo, "/") {
		repo = repo[:idx]
	}
	digestRef := repo + "@" + img.Digest.String()
	// Simulate buildah: images pulled with tag+digest are stored under digest-only name
	if err := fixture.store.SetNames(imgID, []string{digestRef}); err != nil {
		t.Fatalf("buildDigestOnlyImage: SetNames failed: %v", err)
	}
	return digestRef
}

// normalizeTag normalizes a container image tag by:
// - Adding `localhost/` prefix if the tag doesn't contain a registry URL (no `/`)
// - Adding `:latest` suffix if the tag doesn't contain a tag (no `:`)
func normalizeTag(tag string) string {
	if storageclient.IsSpecialBase(tag) {
		return tag
	}
	if tag == "" {
		tag = uuid.New().String()
	}
	normalized := tag
	if !strings.Contains(normalized, "/") {
		normalized = "localhost/" + normalized
	}
	if !strings.Contains(normalized, ":") {
		normalized = normalized + ":latest"
	}

	return normalized
}

// normalizeTestCaseTags normalizes all tags in a test case
func normalizeTestCaseTags(testCase *TestCase) {
	testCase.TestImage.Tag = normalizeTag(testCase.TestImage.Tag)
	for i := range testCase.BuilderImages {
		testCase.BuilderImages[i].Tag = normalizeTag(testCase.BuilderImages[i].Tag)
	}

	// Normalize tags in ExpectedResult (Pullspec fields)
	for i := range testCase.ExpectedResult.Packages {
		testCase.ExpectedResult.Packages[i].Pullspec = normalizeTag(testCase.ExpectedResult.Packages[i].Pullspec)
	}
}

// Builder struct used to efficiently construct expected package metadata items
// for test cases
type pkgMetaItemBuilder struct {
	inner      []PackageMetadataItem
	pullspec   string
	originType string
	stageAlias string
}

// Set the expected origin type for all inner package metadata items
func (b pkgMetaItemBuilder) ExpectedOriginType(ot string) pkgMetaItemBuilder {
	b.originType = ot
	return b
}

// Set the expected pullspec for all inner package metadata items
func (b pkgMetaItemBuilder) ExpectedPullspec(pullspec string) pkgMetaItemBuilder {
	b.pullspec = pullspec
	return b
}

// Set the expected stage alias for all inner package metadata items
func (b pkgMetaItemBuilder) ExpectedStageAlias(alias string) pkgMetaItemBuilder {
	b.stageAlias = alias
	return b
}

// Build the expected package metadata items.
func (b pkgMetaItemBuilder) Build() []PackageMetadataItem {
	items := make([]PackageMetadataItem, len(b.inner))
	copy(items, b.inner)

	for i := range items {
		items[i].Pullspec = b.pullspec
		items[i].OriginType = b.originType
		items[i].StageAlias = b.stageAlias
	}

	return items
}

var syfterBuilder = pkgMetaItemBuilder{
	inner: []PackageMetadataItem{
		{
			PackageURL:       "pkg:golang/github.com/anchore/syft@v1.32.0",
			DependencyOfPURL: "pkg:golang/syfter@v1.0.0",
		},
		{
			PackageURL:       "pkg:golang/github.com/facebookincubator/nvdtools@v0.1.5",
			DependencyOfPURL: "pkg:golang/syfter@v1.0.0",
		},
		{
			PackageURL:       "pkg:golang/stdlib@1.26.2-X%3Anodwarf5",
			DependencyOfPURL: "pkg:golang/syfter@v1.0.0",
		},
		{
			PackageURL: "pkg:golang/syfter@v1.0.0",
		},
	},
}

var texterBuilder = pkgMetaItemBuilder{
	inner: []PackageMetadataItem{
		{
			PackageURL:       "pkg:golang/golang.org/x/text@v0.18.0",
			DependencyOfPURL: "pkg:golang/texter@v1.0.0",
		},
		{
			PackageURL:       "pkg:golang/stdlib@1.26.2-X%3Anodwarf5",
			DependencyOfPURL: "pkg:golang/texter@v1.0.0",
		},
		{
			PackageURL: "pkg:golang/texter@v1.0.0",
		},
	},
}

var syncerBuilder = pkgMetaItemBuilder{
	inner: []PackageMetadataItem{
		{
			PackageURL:       "pkg:golang/golang.org/x/sync@v0.8.0",
			DependencyOfPURL: "pkg:golang/syncer@v1.0.0",
		},
		{
			PackageURL:       "pkg:golang/stdlib@1.26.2-X%3Anodwarf5",
			DependencyOfPURL: "pkg:golang/syncer@v1.0.0",
		},
		{
			PackageURL: "pkg:golang/syncer@v1.0.0",
		},
	},
}

var uuiderBuilder = pkgMetaItemBuilder{
	inner: []PackageMetadataItem{
		{
			PackageURL:       "pkg:golang/github.com/google/uuid@v1.6.0",
			DependencyOfPURL: "pkg:golang/uuider@v1.0.0",
		},
		{
			PackageURL:       "pkg:golang/stdlib@1.26.2-X%3Anodwarf5",
			DependencyOfPURL: "pkg:golang/uuider@v1.0.0",
		},
		{
			PackageURL: "pkg:golang/uuider@v1.0.0",
		},
	},
}

var expBuilder = pkgMetaItemBuilder{
	inner: []PackageMetadataItem{
		{
			PackageURL:       "pkg:golang/golang.org/x/exp@v0.0.0-20240808152545-0cdaa3abc0fa",
			DependencyOfPURL: "pkg:golang/exp@v1.0.0",
		},
		{
			PackageURL:       "pkg:golang/stdlib@1.26.2-X%3Anodwarf5",
			DependencyOfPURL: "pkg:golang/exp@v1.0.0",
		},
		{
			PackageURL: "pkg:golang/exp@v1.0.0",
		},
	},
}

// TestIntegration runs end-to-end tests: builds test images, scans them for packages,
// and compares results against expected package metadata.
func TestIntegration(t *testing.T) {
	testCases := map[string]TestCase{
		"Identification of the builder base image content - no intermediate image, single file copy": {
			TestImage: BuildDefinition{
				ContainerfileContent: `FROM localhost/capo-builder/go_builder:latest as builder
										FROM scratch
										COPY --from=builder /opt/syfter /opt/syfter`,
				ContextDirectory: "../testdata/image_content",
			},
			BuilderImages: []BuildDefinition{
				{
					Tag: "localhost/capo-builder/go_builder:latest",
					ContainerfileContent: `FROM scratch
											COPY syfter /opt/syfter`,
					ContextDirectory: "../testdata/image_content",
				},
			},
			ExpectedResult: PackageMetadata{
				Packages: syfterBuilder.
					ExpectedPullspec("localhost/capo-builder/go_builder@sha256:dummy").
					ExpectedOriginType("builder").
					ExpectedStageAlias("builder").
					Build(),
			},
		},
		"Identification of the builder and intermediate content - single file COPY from intermediate": {
			TestImage: BuildDefinition{
				Tag: "test-single-file-copy",
				ContainerfileContent: `FROM localhost/singlefile-base:latest AS builder
										COPY uuider /content/uuider

										FROM scratch
										COPY --from=builder /content/uuider /content/uuider`,
				ContextDirectory: "../testdata/image_content",
			},
			BuilderImages: []BuildDefinition{
				{
					Tag: "localhost/singlefile-base:latest",
					ContainerfileContent: `FROM scratch
											COPY go2 /untracked/base/go2`,
					ContextDirectory: "../testdata/image_content",
				},
			},
			ExpectedResult: PackageMetadata{
				Packages: uuiderBuilder.
					ExpectedPullspec("localhost/singlefile-base@sha256:dummy").
					ExpectedOriginType("intermediate").
					ExpectedStageAlias("builder").
					Build(),
			},
		},
		"Identification of the builder and intermediate content - directory copy": {
			TestImage: BuildDefinition{
				Tag: "test-builder-intermediate",
				ContainerfileContent: `FROM localhost/capo-builder/go_builder:latest AS builder
										COPY uuider /opt/app2/uuider
										COPY texter /unused/texter

										FROM scratch
										COPY --from=builder /opt/ /opt/`,
				ContextDirectory: "../testdata/image_content",
			},
			BuilderImages: []BuildDefinition{
				{
					Tag: "localhost/capo-builder/go_builder:latest",
					ContainerfileContent: `FROM scratch
											COPY syfter /opt/app1/syfter
											COPY syncer /base_unused/syncer`,
					ContextDirectory: "../testdata/image_content",
				},
			},
			ExpectedResult: PackageMetadata{
				Packages: slices.Concat(
					syfterBuilder.ExpectedPullspec("localhost/capo-builder/go_builder@sha256:dummy").
						ExpectedOriginType("builder").
						ExpectedStageAlias("builder").Build(),
					uuiderBuilder.ExpectedPullspec("localhost/capo-builder/go_builder@sha256:dummy").
						ExpectedOriginType("intermediate").
						ExpectedStageAlias("builder").Build(),
				),
			},
		},
		"Two stages with same pullspec but different intermediate content": {
			TestImage: BuildDefinition{
				Tag: "test-same-pullspec-different-content",
				ContainerfileContent: `FROM localhost/builder-base:latest AS stage1
										COPY uuider /opt/app1/uuider
										COPY texter /untracked/s1/texter

										FROM localhost/builder-base:latest AS stage2
										COPY exp /opt/app2/exp
										COPY texter /untracked/s2/texter

										FROM scratch
										COPY --from=stage1 /opt/ /opt/
										COPY --from=stage2 /opt/app2/ /opt/app2/`,
				ContextDirectory: "../testdata/image_content",
			},
			BuilderImages: []BuildDefinition{
				{
					Tag: "localhost/builder-base:latest",
					ContainerfileContent: `FROM scratch
											COPY syfter /opt/base/syfter
											COPY go2 /untracked/base/go2`,
					ContextDirectory: "../testdata/image_content",
				},
			},
			ExpectedResult: PackageMetadata{
				Packages: slices.Concat(
					syfterBuilder.ExpectedPullspec("localhost/builder-base@sha256:dummy").
						ExpectedOriginType("builder").
						ExpectedStageAlias("stage1").Build(),
					uuiderBuilder.ExpectedPullspec("localhost/builder-base@sha256:dummy").
						ExpectedOriginType("intermediate").
						ExpectedStageAlias("stage1").Build(),
					expBuilder.ExpectedPullspec("localhost/builder-base@sha256:dummy").
						ExpectedOriginType("intermediate").
						ExpectedStageAlias("stage2").Build(),
				),
			},
		},
		"Multiple sources in single COPY --from command": {
			TestImage: BuildDefinition{
				Tag: "test-multi-source-copy",
				ContainerfileContent: `FROM localhost/multi-base:latest AS builder
										COPY uuider /src1/uuider
										COPY exp /src2/exp
										COPY texter /untracked/builder/texter

										FROM scratch
										COPY --from=builder /base /src1 /src2 /dest/`,
				ContextDirectory: "../testdata/image_content",
			},
			BuilderImages: []BuildDefinition{
				{
					Tag: "localhost/multi-base:latest",
					ContainerfileContent: `FROM scratch
											COPY syfter /base/syfter
											COPY go2 /untracked/base/go2`,
					ContextDirectory: "../testdata/image_content",
				},
			},
			ExpectedResult: PackageMetadata{
				Packages: slices.Concat(
					syfterBuilder.ExpectedPullspec("localhost/multi-base@sha256:dummy").
						ExpectedOriginType("builder").
						ExpectedStageAlias("builder").Build(),
					uuiderBuilder.ExpectedPullspec("localhost/multi-base@sha256:dummy").
						ExpectedOriginType("intermediate").
						ExpectedStageAlias("builder").Build(),
					expBuilder.ExpectedPullspec("localhost/multi-base@sha256:dummy").
						ExpectedOriginType("intermediate").
						ExpectedStageAlias("builder").Build(),
				),
			},
		},
		"ARG substitution": {
			TestImage: BuildDefinition{
				Tag: "test-arg-substitution",
				ContainerfileContent: `ARG BASE_IMG=localhost/arg-base:latest
										ARG BUILDER_STAGE=builder
										ARG CONTENT_DIR

										FROM ${BASE_IMG} AS ${BUILDER_STAGE}
										ENV CONTENT_DIR=/content
										COPY uuider ${CONTENT_DIR}/app2/uuider
										COPY texter /untracked/builder/texter

										FROM scratch
										ARG BUILDER_STAGE=builder
										COPY --from=${BUILDER_STAGE} /content /content`,
				ContextDirectory: "../testdata/image_content",
			},
			BuilderImages: []BuildDefinition{
				{
					Tag: "localhost/arg-base:latest",
					ContainerfileContent: `FROM scratch
											COPY syfter /content/app1/syfter
											COPY go2 /untracked/base/go2`,
					ContextDirectory: "../testdata/image_content",
				},
			},
			ExpectedResult: PackageMetadata{
				Packages: slices.Concat(
					syfterBuilder.ExpectedPullspec("localhost/arg-base@sha256:dummy").
						ExpectedOriginType("builder").
						ExpectedStageAlias("builder").Build(),
					uuiderBuilder.ExpectedPullspec("localhost/arg-base@sha256:dummy").
						ExpectedOriginType("intermediate").
						ExpectedStageAlias("builder").Build(),
				),
			},
		},
		"Multiarch build args - TARGETARCH in builder base image tag": {
			TestImage: BuildDefinition{
				Tag: "test-multiarch-targetarch",
				ContainerfileContent: `FROM localhost/multiarch-base:${TARGETARCH} AS builder
										COPY uuider /opt/app2/uuider

										FROM scratch
										COPY --from=builder /opt/ /opt/`,
				ContextDirectory: "../testdata/image_content",
			},
			BuilderImages: []BuildDefinition{
				{
					Tag: "localhost/multiarch-base:" + runtime.GOARCH,
					ContainerfileContent: `FROM scratch
											COPY syfter /opt/app1/syfter
											COPY go2 /untracked/base/go2`,
					ContextDirectory: "../testdata/image_content",
				},
			},
			ExpectedResult: PackageMetadata{
				Packages: slices.Concat(
					syfterBuilder.ExpectedPullspec("localhost/multiarch-base@sha256:dummy").
						ExpectedOriginType("builder").
						ExpectedStageAlias("builder").Build(),
					uuiderBuilder.ExpectedPullspec("localhost/multiarch-base@sha256:dummy").
						ExpectedOriginType("intermediate").
						ExpectedStageAlias("builder").Build(),
				),
			},
		},
		"Content cascade through COPY --from between builder stages": {
			SkipTestReason: "[Priority: medium] final image is expected to contain content also from forwarder builder base image (exp package). This mght be an edgecase in capo tracing content implementation",
			TestImage: BuildDefinition{
				Tag: "test-copy-cascade-builders",
				ContainerfileContent: `FROM localhost/base1:latest AS builder
										COPY uuider /content/app3/uuider

										FROM localhost/base2:latest AS forwarder
										COPY --from=builder /content /content

										FROM scratch
										COPY --from=forwarder /content /content`,
				ContextDirectory: "../testdata/image_content",
			},
			BuilderImages: []BuildDefinition{
				{
					Tag: "localhost/base1:latest",
					ContainerfileContent: `FROM scratch
											COPY syfter /content/app1/syfter
											COPY go2 /untracked/base1/go2`,
					ContextDirectory: "../testdata/image_content",
				},
				{
					Tag: "localhost/base2:latest",
					ContainerfileContent: `FROM scratch
											COPY exp /content/app2/exp
											COPY go2 /untracked/base2/go2`,
					ContextDirectory: "../testdata/image_content",
				},
			},
			ExpectedResult: PackageMetadata{
				Packages: slices.Concat(
					syfterBuilder.ExpectedPullspec("localhost/base1@sha256:dummy").
						ExpectedOriginType("builder").
						ExpectedStageAlias("builder").Build(),
					uuiderBuilder.ExpectedPullspec("localhost/base1@sha256:dummy").
						ExpectedOriginType("intermediate").
						ExpectedStageAlias("builder").Build(),
					expBuilder.ExpectedPullspec("localhost/base2@sha256:dummy").
						ExpectedOriginType("builder").
						ExpectedStageAlias("forwarder").Build(),
				),
			},
		},
		// Verify that if a builder stage is used as a base for the final
		// image, its content is not resolved as builder nor as intermediate.
		"Builder used as final stage base - builder content excluded": {
			TestImage: BuildDefinition{
				Tag: "test-final-uses-builder-base",
				ContainerfileContent: `FROM localhost/builder1:latest AS builder
										COPY uuider /content/uuider

										FROM localhost/builder2:latest AS alias_as_base
										COPY exp /content/exp

										FROM alias_as_base
										COPY --from=builder /base1 /base1
										COPY --from=builder /content /content`,
				ContextDirectory: "../testdata/image_content",
			},
			BuilderImages: []BuildDefinition{
				{
					Tag: "localhost/builder1:latest",
					ContainerfileContent: `FROM scratch
											COPY syfter /base1/syfter`,
					ContextDirectory: "../testdata/image_content",
				},
				{
					Tag: "localhost/builder2:latest",
					ContainerfileContent: `FROM scratch
											COPY syncer /base2/syncer`,
					ContextDirectory: "../testdata/image_content",
				},
			},
			ExpectedResult: PackageMetadata{
				Packages: slices.Concat(
					syfterBuilder.ExpectedPullspec("localhost/builder1@sha256:dummy").
						ExpectedOriginType("builder").
						ExpectedStageAlias("builder").Build(),
					uuiderBuilder.ExpectedPullspec("localhost/builder1@sha256:dummy").
						ExpectedOriginType("intermediate").
						ExpectedStageAlias("builder").Build(),
				),
			},
		},
		// WARNING: This test is not specifically "correct", ideally content
		// copied from builder stage used as final base would not be reported
		// builder/intermediate at all. This test is here just to catch any
		// accidental change of behaviour.
		"Builder used as final stage base - explicit COPY from base stage traced": {
			TestImage: BuildDefinition{
				Tag: "test-final-uses-builder-base-with-copy",
				ContainerfileContent: `FROM localhost/builder1:latest AS builder
										COPY uuider /content/uuider

										FROM localhost/builder2:latest AS alias_as_base
										COPY exp /content/exp

										FROM alias_as_base
										COPY --from=builder /base1 /base1
										COPY --from=builder /content /content
										COPY --from=alias_as_base /base2 /base2
										COPY --from=alias_as_base /content /content2`,
				ContextDirectory: "../testdata/image_content",
			},
			BuilderImages: []BuildDefinition{
				{
					Tag: "localhost/builder1:latest",
					ContainerfileContent: `FROM scratch
											COPY syfter /base1/syfter`,
					ContextDirectory: "../testdata/image_content",
				},
				{
					Tag: "localhost/builder2:latest",
					ContainerfileContent: `FROM scratch
											COPY syncer /base2/syncer`,
					ContextDirectory: "../testdata/image_content",
				},
			},
			ExpectedResult: PackageMetadata{
				Packages: slices.Concat(
					syfterBuilder.ExpectedPullspec("localhost/builder1@sha256:dummy").
						ExpectedOriginType("builder").
						ExpectedStageAlias("builder").Build(),
					uuiderBuilder.ExpectedPullspec("localhost/builder1@sha256:dummy").
						ExpectedOriginType("intermediate").
						ExpectedStageAlias("builder").Build(),
					syncerBuilder.ExpectedPullspec("localhost/builder2@sha256:dummy").
						ExpectedOriginType("builder").
						ExpectedStageAlias("alias_as_base").Build(),
					expBuilder.ExpectedPullspec("localhost/builder2@sha256:dummy").
						ExpectedOriginType("intermediate").
						ExpectedStageAlias("alias_as_base").Build(),
				),
			},
		},
		"Path prefix collision - /opt should not match /optional": {
			TestImage: BuildDefinition{
				Tag: "test-path-prefix-collision",
				ContainerfileContent: `FROM localhost/prefix-base:latest AS builder
										COPY uuider /opt/uuider
										COPY exp /optional/exp

										FROM scratch
										COPY --from=builder /opt /opt`,
				ContextDirectory: "../testdata/image_content",
			},
			BuilderImages: []BuildDefinition{
				{
					Tag: "localhost/prefix-base:latest",
					ContainerfileContent: `FROM scratch
											COPY syfter /base/syfter`,
					ContextDirectory: "../testdata/image_content",
				},
			},
			ExpectedResult: PackageMetadata{
				Packages: uuiderBuilder.
					ExpectedPullspec("localhost/prefix-base@sha256:dummy").
					ExpectedOriginType("intermediate").
					ExpectedStageAlias("builder").
					Build(),
			},
		},
		"[Path normalization] Malformed and dot-dot paths in COPY --from": {
			TestImage: BuildDefinition{
				Tag: "test-path-normalization",
				ContainerfileContent: `FROM localhost/pathnorm-base:latest AS builder
										COPY uuider //opt/uuider
										COPY exp /content/exp

										FROM scratch
										COPY --from=builder etc/../opt/.//uuider /opt/uuider
										COPY --from=builder /foo/../content/ /content/`,
				ContextDirectory: "../testdata/image_content",
			},
			BuilderImages: []BuildDefinition{
				{
					Tag: "localhost/pathnorm-base:latest",
					ContainerfileContent: `FROM scratch
											COPY texter /base/texter`,
					ContextDirectory: "../testdata/image_content",
				},
			},
			ExpectedResult: PackageMetadata{
				Packages: slices.Concat(
					expBuilder.ExpectedPullspec("localhost/pathnorm-base@sha256:dummy").
						ExpectedOriginType("intermediate").
						ExpectedStageAlias("builder").Build(),
					uuiderBuilder.ExpectedPullspec("localhost/pathnorm-base@sha256:dummy").
						ExpectedOriginType("intermediate").
						ExpectedStageAlias("builder").Build(),
				),
			},
		},
		// Capo walks COPY destinations in a builder stage to find where content
		// originated. It also has to handle prefix collisions in this case
		// (/opt and /optional)
		"[trace source] Prefix collision during content tracing across stages": {
			TestImage: BuildDefinition{
				Tag: "test-trace-prefix-collision",
				ContainerfileContent: `FROM localhost/trace-prefix-provider:latest AS provider

										FROM localhost/trace-prefix-base:latest AS builder
										COPY --from=provider /opt/uuider /opt/uuider
										COPY --from=provider /optional/exp /optional/exp

										FROM scratch
										COPY --from=builder /opt/uuider /opt/uuider`,
				ContextDirectory: "../testdata/image_content",
			},
			BuilderImages: []BuildDefinition{
				{
					Tag: "localhost/trace-prefix-provider:latest",
					ContainerfileContent: `FROM scratch
											COPY uuider /opt/uuider
											COPY exp /optional/exp`,
					ContextDirectory: "../testdata/image_content",
				},
				{
					Tag: "localhost/trace-prefix-base:latest",
					ContainerfileContent: `FROM scratch
											COPY syfter /base/syfter`,
					ContextDirectory: "../testdata/image_content",
				},
			},
			ExpectedResult: PackageMetadata{
				Packages: uuiderBuilder.
					ExpectedPullspec("localhost/trace-prefix-provider@sha256:dummy").
					ExpectedOriginType("builder").
					ExpectedStageAlias("provider").
					Build(),
			},
		},
		// Two providers copy content into the same /dest/ in builder stage.
		// Provider2 overwrites provider1's file. Only exp package (provider2) should be
		// in the final image. However, capo currently cannot distinguish overlapping
		// writes — it sees both files in the intermediate layer diff and reports both.
		// Correct behavior requires Containerfile-level instruction ordering awareness.
		"Overlapping COPY destinations in builder stage": {
			SkipTestReason: "[Priority: medium] capo does not track overlapping COPY destinations - reports both providers instead of only the last one",
			TestImage: BuildDefinition{
				Tag: "test-overlapping-dest",
				ContainerfileContent: `FROM localhost/overlap-base:latest AS provider1
									   COPY uuider /src1/content

									   FROM localhost/overlap-base:latest AS provider2
									   COPY exp /src2/content

									   FROM localhost/overlap-base:latest AS builder
									   COPY --from=provider1 /src1/ /dest/
									   COPY --from=provider2 /src2/ /dest/

									   FROM scratch
									   COPY --from=builder /dest/content /dest/content`,
				ContextDirectory: "../testdata/image_content",
			},
			BuilderImages: []BuildDefinition{
				{
					Tag: "localhost/overlap-base:latest",
					ContainerfileContent: `FROM scratch
										   COPY syfter /base/syfter`,
					ContextDirectory: "../testdata/image_content",
				},
			},
			ExpectedResult: PackageMetadata{
				Packages: expBuilder.
					ExpectedPullspec("localhost/overlap-base@sha256:dummy").
					ExpectedOriginType("intermediate").
					ExpectedStageAlias("provider2").
					Build(),
			},
		},
		"Intermediate content overwrites builder base content at same path": {
			SkipTestReason: "[Priority: medium] capo scans builder base and intermediate independently - does not detect that intermediate overwrites base at same path",
			TestImage: BuildDefinition{
				Tag: "test-intermediate-overwrites-base",
				ContainerfileContent: `FROM localhost/overwrite-base:latest AS builder
									   COPY uuider /opt/app1/content

									   FROM scratch
									   COPY --from=builder /opt /opt`,
				ContextDirectory: "../testdata/image_content",
			},
			BuilderImages: []BuildDefinition{
				{
					Tag: "localhost/overwrite-base:latest",
					ContainerfileContent: `FROM scratch
										   COPY syfter /opt/app1/content`,
					ContextDirectory: "../testdata/image_content",
				},
			},
			ExpectedResult: PackageMetadata{
				Packages: uuiderBuilder.
					ExpectedPullspec("localhost/overwrite-base@sha256:dummy").
					ExpectedOriginType("intermediate").
					ExpectedStageAlias("builder").
					Build(),
			},
		},
		"[Chained stages] Grandparent, parent and child builder cascade with intermediate content": {
			TestImage: BuildDefinition{
				Tag: "test-chained-stages-cascade",
				ContainerfileContent: `FROM localhost/builder-sync:latest AS grandparent
										COPY uuider /opt/app2/uuider
										COPY texter /untracked/gp/texter

										FROM grandparent AS parent
										COPY exp /opt/app3/exp
										COPY texter /untracked/p/texter

										FROM parent AS child
										COPY syncer /opt/app4/syncer
										COPY texter /untracked/c/texter

										FROM scratch
										COPY --from=child /opt/ /opt/`,
				ContextDirectory: "../testdata/image_content",
			},
			BuilderImages: []BuildDefinition{
				{
					Tag: "localhost/builder-sync:latest",
					ContainerfileContent: `FROM scratch
											COPY syfter /opt/app1/syfter
											COPY go2 /untracked/base/go2`,
					ContextDirectory: "../testdata/image_content",
				},
			},
			ExpectedResult: PackageMetadata{
				Packages: slices.Concat(
					syfterBuilder.ExpectedPullspec("localhost/builder-sync@sha256:dummy").
						ExpectedOriginType("builder").
						ExpectedStageAlias("grandparent").Build(),
					uuiderBuilder.ExpectedPullspec("localhost/builder-sync@sha256:dummy").
						ExpectedOriginType("intermediate").
						ExpectedStageAlias("grandparent").Build(),
					expBuilder.ExpectedPullspec("localhost/builder-sync@sha256:dummy").
						ExpectedOriginType("intermediate").
						ExpectedStageAlias("parent").Build(),
					syncerBuilder.ExpectedPullspec("localhost/builder-sync@sha256:dummy").
						ExpectedOriginType("intermediate").
						ExpectedStageAlias("child").Build(),
				),
			},
		},
		"[Chained stages] Empty child chained stage (no build instructions)": {
			TestImage: BuildDefinition{
				Tag: "test-empty-chained-stage",
				ContainerfileContent: `FROM localhost/capo-builder/go_builder:latest AS parent-stage
										COPY uuider /opt/app2/uuider
										COPY texter /untracked/parent/texter

										FROM parent-stage AS empty-child

										FROM scratch
										COPY --from=empty-child /opt/ /opt/`,
				ContextDirectory: "../testdata/image_content",
			},
			BuilderImages: []BuildDefinition{
				{
					Tag: "localhost/capo-builder/go_builder:latest",
					ContainerfileContent: `FROM scratch
											COPY syfter /opt/app1/syfter
											COPY go2 /untracked/base/go2`,
					ContextDirectory: "../testdata/image_content",
				},
			},
			ExpectedResult: PackageMetadata{
				Packages: slices.Concat(
					syfterBuilder.ExpectedPullspec("localhost/capo-builder/go_builder@sha256:dummy").
						ExpectedOriginType("builder").
						ExpectedStageAlias("parent-stage").Build(),
					uuiderBuilder.ExpectedPullspec("localhost/capo-builder/go_builder@sha256:dummy").
						ExpectedOriginType("intermediate").
						ExpectedStageAlias("parent-stage").Build(),
				),
			},
		},
		"[Chained stages] Multiple empty chained stages with intermediate only in last stage": {
			TestImage: BuildDefinition{
				Tag: "test-empty-chain-cascade",
				ContainerfileContent: `FROM localhost/builder-base:latest AS first

										FROM first AS second

										FROM second AS third
										COPY uuider /opt/app/uuider
										COPY texter /untracked/third/texter

										FROM scratch
										COPY --from=third /opt/ /opt/`,
				ContextDirectory: "../testdata/image_content",
			},
			BuilderImages: []BuildDefinition{
				{
					Tag: "localhost/builder-base:latest",
					ContainerfileContent: `FROM scratch
											COPY syfter /opt/base/syfter
											COPY go2 /untracked/base/go2`,
					ContextDirectory: "../testdata/image_content",
				},
			},
			ExpectedResult: PackageMetadata{
				Packages: slices.Concat(
					syfterBuilder.ExpectedPullspec("localhost/builder-base@sha256:dummy").
						ExpectedOriginType("builder").
						ExpectedStageAlias("first").Build(),
					uuiderBuilder.ExpectedPullspec("localhost/builder-base@sha256:dummy").
						ExpectedOriginType("intermediate").
						ExpectedStageAlias("third").Build(),
				),
			},
		},
		"[Chained stages] Complex cascade: non-empty, empty, non-empty, empty, non-empty": {
			TestImage: BuildDefinition{
				Tag: "test-complex-cascade",
				ContainerfileContent: `FROM localhost/builder-base:latest AS stage1
										COPY uuider /opt/app1/uuider
										COPY texter /untracked/s1/texter

										FROM stage1 AS stage2

										FROM stage2 AS stage3
										COPY exp /opt/app2/exp
										COPY texter /untracked/s3/texter

										FROM stage3 AS stage4

										FROM stage4 AS stage5
										COPY syncer /opt/app3/syncer
										COPY texter /untracked/s5/texter

										FROM scratch
										COPY --from=stage5 /opt/ /opt/`,
				ContextDirectory: "../testdata/image_content",
			},
			BuilderImages: []BuildDefinition{
				{
					Tag: "localhost/builder-base:latest",
					ContainerfileContent: `FROM scratch
											COPY syfter /opt/base/syfter
											COPY go2 /untracked/base/go2`,
					ContextDirectory: "../testdata/image_content",
				},
			},
			ExpectedResult: PackageMetadata{
				Packages: slices.Concat(
					syfterBuilder.ExpectedPullspec("localhost/builder-base@sha256:dummy").
						ExpectedOriginType("builder").
						ExpectedStageAlias("stage1").Build(),
					uuiderBuilder.ExpectedPullspec("localhost/builder-base@sha256:dummy").
						ExpectedOriginType("intermediate").
						ExpectedStageAlias("stage1").Build(),
					expBuilder.ExpectedPullspec("localhost/builder-base@sha256:dummy").
						ExpectedOriginType("intermediate").
						ExpectedStageAlias("stage3").Build(),
					syncerBuilder.ExpectedPullspec("localhost/builder-base@sha256:dummy").
						ExpectedOriginType("intermediate").
						ExpectedStageAlias("stage5").Build(),
				),
			},
		},
		"[Chained stages] Empty chained stages copying only builder base content": {
			TestImage: BuildDefinition{
				Tag: "test-empty-chain-builder-only",
				ContainerfileContent: `FROM localhost/builder-with-content:latest AS alias

										FROM alias AS alias2

										FROM scratch
										COPY --from=alias2 /opt/content/ /opt/`,
				ContextDirectory: "../testdata/image_content",
			},
			BuilderImages: []BuildDefinition{
				{
					Tag: "localhost/builder-with-content:latest",
					ContainerfileContent: `FROM scratch
											COPY syfter /opt/content/syfter
											COPY go2 /untracked/base/go2`,
					ContextDirectory: "../testdata/image_content",
				},
			},
			ExpectedResult: PackageMetadata{
				Packages: syfterBuilder.
					ExpectedPullspec("localhost/builder-with-content@sha256:dummy").
					ExpectedOriginType("builder").
					ExpectedStageAlias("alias").
					Build(),
			},
		},
		"[Chained stages] Diamond dependency - two branches from same parent": {
			TestImage: BuildDefinition{
				Tag: "test-diamond-dependency",
				ContainerfileContent: `FROM localhost/diamond-base:latest AS shared
										COPY uuider /shared/uuider
										COPY texter /untracked/shared/texter

										FROM shared AS left
										COPY exp /left/exp
										COPY texter /untracked/left/texter

										FROM shared AS right
										COPY syncer /right/syncer
										COPY texter /untracked/right/texter

										FROM scratch
										COPY --from=left /shared /shared
										COPY --from=left /left /left
										COPY --from=right /right /right
										COPY --from=right /base /base`,
				ContextDirectory: "../testdata/image_content",
			},
			BuilderImages: []BuildDefinition{
				{
					Tag: "localhost/diamond-base:latest",
					ContainerfileContent: `FROM scratch
											COPY syfter /base/syfter
											COPY go2 /untracked/base/go2`,
					ContextDirectory: "../testdata/image_content",
				},
			},
			ExpectedResult: PackageMetadata{
				Packages: slices.Concat(
					syfterBuilder.ExpectedPullspec("localhost/diamond-base@sha256:dummy").
						ExpectedOriginType("builder").
						ExpectedStageAlias("shared").Build(),
					uuiderBuilder.ExpectedPullspec("localhost/diamond-base@sha256:dummy").
						ExpectedOriginType("intermediate").
						ExpectedStageAlias("shared").Build(),
					expBuilder.ExpectedPullspec("localhost/diamond-base@sha256:dummy").
						ExpectedOriginType("intermediate").
						ExpectedStageAlias("left").Build(),
					syncerBuilder.ExpectedPullspec("localhost/diamond-base@sha256:dummy").
						ExpectedOriginType("intermediate").
						ExpectedStageAlias("right").Build(),
				),
			},
		},
		// Stage alias "alpine" collides with real image name.
		// Verified by @BorekZnovustvoritel that both buildah and Docker resolve stage
		// alias over registry image — "FROM alpine" references the stage, not
		// docker.io/library/alpine:latest. This is a chained stage scenario.
		"[Chained stages] Stage alias with same name as image": {
			TestImage: BuildDefinition{
				Tag: "test-alias-matches-image",
				ContainerfileContent: `FROM localhost/builderwithbadalias:latest AS alpine
										COPY uuider /opt/app2/uuider

										FROM alpine AS stage2
										COPY exp /opt/app3/exp

										FROM scratch
										COPY --from=stage2 /opt/ /opt/`,
				ContextDirectory: "../testdata/image_content",
			},
			BuilderImages: []BuildDefinition{
				{
					Tag: "builderwithbadalias",
					ContainerfileContent: `FROM scratch
											COPY syfter /opt/app1/syfter`,
					ContextDirectory: "../testdata/image_content",
				},
			},
			ExpectedResult: PackageMetadata{
				Packages: slices.Concat(
					syfterBuilder.ExpectedPullspec("localhost/builderwithbadalias@sha256:dummy").
						ExpectedOriginType("builder").
						ExpectedStageAlias("alpine").Build(),
					uuiderBuilder.ExpectedPullspec("localhost/builderwithbadalias@sha256:dummy").
						ExpectedOriginType("intermediate").
						ExpectedStageAlias("alpine").Build(),
					expBuilder.ExpectedPullspec("localhost/builderwithbadalias@sha256:dummy").
						ExpectedOriginType("intermediate").
						ExpectedStageAlias("stage2").Build(),
				),
			},
		},
		"[Chained stages / external content] Content traced through intermediate builder via COPY chain with external image": {
			TestImage: BuildDefinition{
				Tag: "test-chain-with-external",
				ContainerfileContent: `FROM localhost/base-img:latest AS builder
										COPY uuider /content/app1/uuider
										COPY syncer /untracked/builder/syncer

										FROM builder AS other-builder
										COPY --from=localhost/in-chain-ext:latest /ext /ext

										FROM scratch
										COPY --from=other-builder /base /base
										COPY --from=other-builder /content /content
										COPY --from=other-builder /ext /ext`,
				ContextDirectory: "../testdata/image_content",
			},
			BuilderImages: []BuildDefinition{
				{
					Tag: "localhost/base-img:latest",
					ContainerfileContent: `FROM scratch
											COPY syfter /base/syfter
											COPY go2 /untracked/base/go2`,
					ContextDirectory: "../testdata/image_content",
				},
				{
					Tag: "localhost/in-chain-ext:latest",
					ContainerfileContent: `FROM scratch
											COPY texter /ext/texter
											COPY go2 /untracked/ext/go2`,
					ContextDirectory: "../testdata/image_content",
				},
			},
			ExpectedResult: PackageMetadata{
				Packages: slices.Concat(
					syfterBuilder.ExpectedPullspec("localhost/base-img@sha256:dummy").
						ExpectedOriginType("builder").
						ExpectedStageAlias("builder").Build(),
					uuiderBuilder.ExpectedPullspec("localhost/base-img@sha256:dummy").
						ExpectedOriginType("intermediate").
						ExpectedStageAlias("builder").Build(),
					texterBuilder.ExpectedPullspec("localhost/in-chain-ext@sha256:dummy").
						ExpectedOriginType("external").
						ExpectedStageAlias("").Build(),
				),
			},
		},
		"[External content] External COPY in final stage": {
			TestImage: BuildDefinition{
				Tag: "test-external-copy-final",
				ContainerfileContent: `FROM localhost/builder-base:latest AS builder
										COPY uuider /content/uuider
										COPY syncer /untracked/builder/syncer

										FROM scratch
										COPY --from=builder /base /base
										COPY --from=builder /content /content
										COPY --from=localhost/external:latest /ext /ext`,
				ContextDirectory: "../testdata/image_content",
			},
			BuilderImages: []BuildDefinition{
				{
					Tag: "localhost/builder-base:latest",
					ContainerfileContent: `FROM scratch
											COPY syfter /base/syfter
											COPY go2 /untracked/base/go2`,
					ContextDirectory: "../testdata/image_content",
				},
				{
					Tag: "localhost/external:latest",
					ContainerfileContent: `FROM scratch
											COPY texter /ext/texter
											COPY go2 /untracked/ext/go2`,
					ContextDirectory: "../testdata/image_content",
				},
			},
			ExpectedResult: PackageMetadata{
				Packages: slices.Concat(
					syfterBuilder.ExpectedPullspec("localhost/builder-base@sha256:dummy").
						ExpectedOriginType("builder").
						ExpectedStageAlias("builder").Build(),
					uuiderBuilder.ExpectedPullspec("localhost/builder-base@sha256:dummy").
						ExpectedOriginType("intermediate").
						ExpectedStageAlias("builder").Build(),
					texterBuilder.ExpectedPullspec("localhost/external@sha256:dummy").
						ExpectedOriginType("external").
						ExpectedStageAlias("").Build(),
				),
			},
		},
		// OriginType "external" distinguishes content from external images
		// (COPY --from=<image> or RUN --mount from=<image>) from builder base and
		// intermediate content. Required because in SBOM, intermediate content is
		// modeled as DESCENDANT_OF builder base image — external image content in a
		// builder stage has no such relationship to the builder base.
		"[External content] External COPY in builder stage - content traced through builder to final": {
			TestImage: BuildDefinition{
				Tag: "test-external-copy-in-builder",
				ContainerfileContent: `FROM localhost/builder-base:latest AS builder
										COPY --from=localhost/external:latest /ext /ext
										COPY uuider /content/uuider
										COPY syncer /untracked/builder/syncer

										FROM scratch
										COPY --from=builder /base /base
										COPY --from=builder /ext /ext
										COPY --from=builder /content /content`,
				ContextDirectory: "../testdata/image_content",
			},
			BuilderImages: []BuildDefinition{
				{
					Tag: "localhost/builder-base:latest",
					ContainerfileContent: `FROM scratch
											COPY syfter /base/syfter
											COPY go2 /untracked/base/go2`,
					ContextDirectory: "../testdata/image_content",
				},
				{
					Tag: "localhost/external:latest",
					ContainerfileContent: `FROM scratch
											COPY texter /ext/texter
											COPY go2 /untracked/ext/go2`,
					ContextDirectory: "../testdata/image_content",
				},
			},
			ExpectedResult: PackageMetadata{
				Packages: slices.Concat(
					syfterBuilder.ExpectedPullspec("localhost/builder-base@sha256:dummy").
						ExpectedOriginType("builder").
						ExpectedStageAlias("builder").Build(),
					uuiderBuilder.ExpectedPullspec("localhost/builder-base@sha256:dummy").
						ExpectedOriginType("intermediate").
						ExpectedStageAlias("builder").Build(),
					texterBuilder.ExpectedPullspec("localhost/external@sha256:dummy").
						ExpectedOriginType("external").
						ExpectedStageAlias("").Build(),
				),
			},
		},
		"[Pullspec normalization] Pullspec is missing registry and tag/digest": {
			SkipTestReason: "[Priority: medium/low] pullspec normalization not supported - store.Lookup does exact match with registry and tag/digest",
			TestImage: BuildDefinition{
				Tag: "test-simple-pullspec",
				ContainerfileContent: `FROM image AS builder
										COPY uuider /opt/app1/uuider

										FROM scratch
										COPY --from=builder /opt /opt`,
				ContextDirectory: "../testdata/image_content",
			},
			BuilderImages: []BuildDefinition{
				{
					Tag: "image",
					ContainerfileContent: `FROM scratch
											COPY syfter /opt/app2/syfter`,
					ContextDirectory: "../testdata/image_content",
				},
			},
			ExpectedResult: PackageMetadata{
				Packages: slices.Concat(
					syfterBuilder.ExpectedPullspec("localhost/image@sha256:dummy").
						ExpectedOriginType("builder").
						ExpectedStageAlias("builder").Build(),
					uuiderBuilder.ExpectedPullspec("localhost/image@sha256:dummy").
						ExpectedOriginType("intermediate").
						ExpectedStageAlias("builder").Build(),
				),
			},
		},
		"[Pullspec normalization] Pullspec missing registry and alias is identical to alias - FROM image AS image": {
			SkipTestReason: "[Priority: low] resolved, when previous test passes",
			TestImage: BuildDefinition{
				Tag: "test-identical-pullspec-alias",
				ContainerfileContent: `FROM image AS image
										COPY uuider /content/uuider

										FROM scratch
										COPY --from=image /base /base
										COPY --from=image /content /content`,
				ContextDirectory: "../testdata/image_content",
			},
			BuilderImages: []BuildDefinition{
				{
					Tag: "image",
					ContainerfileContent: `FROM scratch
											COPY syfter /base/syfter`,
					ContextDirectory: "../testdata/image_content",
				},
			},
			ExpectedResult: PackageMetadata{
				Packages: slices.Concat(
					syfterBuilder.ExpectedPullspec("localhost/image@sha256:dummy").
						ExpectedOriginType("builder").
						ExpectedStageAlias("image").Build(),
					uuiderBuilder.ExpectedPullspec("localhost/image@sha256:dummy").
						ExpectedOriginType("intermediate").
						ExpectedStageAlias("image").Build(),
				),
			},
		},
		"[Numeric index COPY --from] Stages do not have aliases - references are using numeric indices instead of aliases": {
			TestImage: BuildDefinition{
				Tag: "test-numeric-indices",
				ContainerfileContent: `FROM localhost/base1:latest
										COPY uuider /opt/app0/uuider
										COPY texter /untracked/s0/texter

										FROM localhost/base2:latest
										COPY exp /opt/app1/exp
										COPY texter /untracked/s1/texter

										FROM scratch
										COPY --from=0 /opt/ /opt/
										COPY --from=1 /opt/ /opt/`,
				ContextDirectory: "../testdata/image_content",
			},
			BuilderImages: []BuildDefinition{
				{
					Tag: "localhost/base1:latest",
					ContainerfileContent: `FROM scratch
											COPY syfter /opt/base1/syfter
											COPY go2 /untracked/base1/go2`,
					ContextDirectory: "../testdata/image_content",
				},
				{
					Tag: "localhost/base2:latest",
					ContainerfileContent: `FROM scratch
											COPY syncer /opt/base2/syncer
											COPY go2 /untracked/base2/go2`,
					ContextDirectory: "../testdata/image_content",
				},
			},
			ExpectedResult: PackageMetadata{
				Packages: slices.Concat(
					syfterBuilder.ExpectedPullspec("localhost/base1@sha256:dummy").
						ExpectedOriginType("builder").
						ExpectedStageAlias("0").Build(),
					uuiderBuilder.ExpectedPullspec("localhost/base1@sha256:dummy").
						ExpectedOriginType("intermediate").
						ExpectedStageAlias("0").Build(),
					syncerBuilder.ExpectedPullspec("localhost/base2@sha256:dummy").
						ExpectedOriginType("builder").
						ExpectedStageAlias("1").Build(),
					expBuilder.ExpectedPullspec("localhost/base2@sha256:dummy").
						ExpectedOriginType("intermediate").
						ExpectedStageAlias("1").Build(),
				),
			},
		},
		"[Numeric index COPY --from] COPY --from with numeric index in final stage (stage has alias)": {
			TestImage: BuildDefinition{
				Tag: "test-numeric-copy-from-final",
				ContainerfileContent: `FROM localhost/numfinal-base:latest AS builder
										COPY uuider /content/uuider
										COPY texter /untracked/builder/texter

										FROM scratch
										COPY --from=0 /content /content`,
				ContextDirectory: "../testdata/image_content",
			},
			BuilderImages: []BuildDefinition{
				{
					Tag: "localhost/numfinal-base:latest",
					ContainerfileContent: `FROM scratch
											COPY syfter /base/syfter
											COPY go2 /untracked/base/go2`,
					ContextDirectory: "../testdata/image_content",
				},
			},
			ExpectedResult: PackageMetadata{
				Packages: uuiderBuilder.
					ExpectedPullspec("localhost/numfinal-base@sha256:dummy").
					ExpectedOriginType("intermediate").
					ExpectedStageAlias("builder").
					Build(),
			},
		},
		"[Numeric index COPY --from] COPY --from with numeric index in builder stage (stages have alias)": {
			TestImage: BuildDefinition{
				Tag: "test-numeric-copy-from-builder",
				ContainerfileContent: `FROM localhost/numbuilder-base1:latest AS builder1
										COPY uuider /content/uuider
										COPY texter /untracked/b1/texter

										FROM localhost/numbuilder-base2:latest AS builder2
										COPY --from=0 /content /forwarded
										COPY texter /untracked/b2/texter

										FROM scratch
										COPY --from=1 /forwarded /forwarded`,
				ContextDirectory: "../testdata/image_content",
			},
			BuilderImages: []BuildDefinition{
				{
					Tag: "localhost/numbuilder-base1:latest",
					ContainerfileContent: `FROM scratch
											COPY syfter /base1/syfter
											COPY go2 /untracked/base1/go2`,
					ContextDirectory: "../testdata/image_content",
				},
				{
					Tag: "localhost/numbuilder-base2:latest",
					ContainerfileContent: `FROM scratch
											COPY exp /base2/exp
											COPY go2 /untracked/base2/go2`,
					ContextDirectory: "../testdata/image_content",
				},
			},
			ExpectedResult: PackageMetadata{
				Packages: uuiderBuilder.
					ExpectedPullspec("localhost/numbuilder-base1@sha256:dummy").
					ExpectedOriginType("intermediate").
					ExpectedStageAlias("builder1").
					Build(),
			},
		},
		"[Wildcard COPY] Builder base content": {
			TestImage: BuildDefinition{
				Tag: "test-wildcard-copy-builder-base",
				ContainerfileContent: `FROM localhost/wildcard-base:latest AS builder
										FROM scratch
										COPY --from=builder /app* /dest/`,
				ContextDirectory: "../testdata/image_content",
			},
			BuilderImages: []BuildDefinition{
				{
					Tag: "localhost/wildcard-base:latest",
					ContainerfileContent: `FROM scratch
											COPY syfter /app1/syfter
											COPY uuider /app2/uuider
											COPY exp /other/exp`,
					ContextDirectory: "../testdata/image_content",
				},
			},
			ExpectedResult: PackageMetadata{
				Packages: slices.Concat(
					syfterBuilder.ExpectedPullspec("localhost/wildcard-base@sha256:dummy").
						ExpectedOriginType("builder").
						ExpectedStageAlias("builder").Build(),
					uuiderBuilder.ExpectedPullspec("localhost/wildcard-base@sha256:dummy").
						ExpectedOriginType("builder").
						ExpectedStageAlias("builder").Build(),
				),
			},
		},
		"[Wildcard COPY] Intermediate content": {
			TestImage: BuildDefinition{
				Tag: "test-wildcard-copy-intermediate",
				ContainerfileContent: `FROM localhost/wildcard-inter-base:latest AS builder
										COPY uuider /app1/uuider
										COPY exp /app2/exp
										COPY syncer /other/syncer

										FROM scratch
										COPY --from=builder /app* /dest/`,
				ContextDirectory: "../testdata/image_content",
			},
			BuilderImages: []BuildDefinition{
				{
					Tag: "localhost/wildcard-inter-base:latest",
					ContainerfileContent: `FROM scratch
											COPY syfter /base/syfter`,
					ContextDirectory: "../testdata/image_content",
				},
			},
			ExpectedResult: PackageMetadata{
				Packages: slices.Concat(
					uuiderBuilder.ExpectedPullspec("localhost/wildcard-inter-base@sha256:dummy").
						ExpectedOriginType("intermediate").
						ExpectedStageAlias("builder").Build(),
					expBuilder.ExpectedPullspec("localhost/wildcard-inter-base@sha256:dummy").
						ExpectedOriginType("intermediate").
						ExpectedStageAlias("builder").Build(),
				),
			},
		},
		"[Wildcard COPY] Builder base and intermediate content": {
			TestImage: BuildDefinition{
				Tag: "test-wildcard-copy-both",
				ContainerfileContent: `FROM localhost/wildcard-both-base:latest AS builder
										COPY syncer /app3/syncer
										COPY texter /other/texter

										FROM scratch
										COPY --from=builder /app* /dest/`,
				ContextDirectory: "../testdata/image_content",
			},
			BuilderImages: []BuildDefinition{
				{
					Tag: "localhost/wildcard-both-base:latest",
					ContainerfileContent: `FROM scratch
											COPY syfter /app1/syfter
											COPY uuider /app2/uuider
											COPY exp /other/exp`,
					ContextDirectory: "../testdata/image_content",
				},
			},
			ExpectedResult: PackageMetadata{
				Packages: slices.Concat(
					syfterBuilder.ExpectedPullspec("localhost/wildcard-both-base@sha256:dummy").
						ExpectedOriginType("builder").
						ExpectedStageAlias("builder").Build(),
					uuiderBuilder.ExpectedPullspec("localhost/wildcard-both-base@sha256:dummy").
						ExpectedOriginType("builder").
						ExpectedStageAlias("builder").Build(),
					syncerBuilder.ExpectedPullspec("localhost/wildcard-both-base@sha256:dummy").
						ExpectedOriginType("intermediate").
						ExpectedStageAlias("builder").Build(),
				),
			},
		},
		"[FROM special] FROM scratch as builder base": {
			TestImage: BuildDefinition{
				Tag: "test-from-scratch-builder",
				ContainerfileContent: `FROM scratch AS builder
										COPY uuider /content/uuider

										FROM scratch
										COPY --from=builder /content /content`,
				ContextDirectory: "../testdata/image_content",
			},
			ExpectedResult: PackageMetadata{
				Packages: uuiderBuilder.
					ExpectedPullspec("scratch").
					ExpectedOriginType("intermediate").
					ExpectedStageAlias("builder").
					Build(),
			},
		},
		"[FROM special] FROM docker:// transport as builder base": {
			TestImage: BuildDefinition{
				Tag: "test-from-docker-transport",
				ContainerfileContent: `FROM docker://localhost/docker-transport-base:latest AS builder
										COPY uuider /content/uuider

										FROM scratch
										COPY --from=builder /content /content
										COPY --from=builder /C /content`,
				ContextDirectory: "../testdata/image_content",
			},
			BuilderImages: []BuildDefinition{
				{
					Tag: "localhost/docker-transport-base:latest",
					ContainerfileContent: `FROM scratch
											COPY go2 /base/go2
											COPY syfter /C/Users/Shadowman/Desktop/syfter`,
					ContextDirectory: "../testdata/image_content",
				},
			},
			ExpectedResult: PackageMetadata{
				Packages: slices.Concat(
					uuiderBuilder.ExpectedPullspec("localhost/docker-transport-base@sha256:dummy").
						ExpectedOriginType("intermediate").
						ExpectedStageAlias("builder").Build(),
					syfterBuilder.ExpectedPullspec("localhost/docker-transport-base@sha256:dummy").
						ExpectedOriginType("builder").
						ExpectedStageAlias("builder").Build(),
				),
			},
		},
		"[WORKDIR] WORKDIR set in intermediate image": {
			TestImage: BuildDefinition{
				Tag: "test-workdir-relative-dest",
				ContainerfileContent: `FROM localhost/workdir-base:latest AS builder
									   WORKDIR /opt/app2
									   COPY uuider uuider

									   FROM scratch
									   COPY --from=builder /opt /opt`,
				ContextDirectory: "../testdata/image_content",
			},
			BuilderImages: []BuildDefinition{
				{
					Tag: "localhost/workdir-base:latest",
					ContainerfileContent: `FROM scratch
										   COPY syfter /opt/app1/syfter`,
					ContextDirectory: "../testdata/image_content",
				},
			},
			ExpectedResult: PackageMetadata{
				Packages: slices.Concat(
					syfterBuilder.ExpectedPullspec("localhost/workdir-base@sha256:dummy").
						ExpectedOriginType("builder").
						ExpectedStageAlias("builder").Build(),
					uuiderBuilder.ExpectedPullspec("localhost/workdir-base@sha256:dummy").
						ExpectedOriginType("intermediate").
						ExpectedStageAlias("builder").Build(),
				),
			},
		},
		"[WORKDIR] WORKDIR inherited from builder base image": {
			TestImage: BuildDefinition{
				Tag: "test-workdir-inherited",
				ContainerfileContent: `FROM localhost/workdir-inherited-base:latest AS builder
									   COPY uuider app2/uuider

									   FROM scratch
									   COPY --from=builder /opt /opt`,
				ContextDirectory: "../testdata/image_content",
			},
			BuilderImages: []BuildDefinition{
				{
					Tag: "localhost/workdir-inherited-base:latest",
					ContainerfileContent: `FROM scratch
										   WORKDIR /opt
										   COPY syfter app1/syfter`,
					ContextDirectory: "../testdata/image_content",
				},
			},
			ExpectedResult: PackageMetadata{
				Packages: slices.Concat(
					syfterBuilder.ExpectedPullspec("localhost/workdir-inherited-base@sha256:dummy").
						ExpectedOriginType("builder").
						ExpectedStageAlias("builder").Build(),
					uuiderBuilder.ExpectedPullspec("localhost/workdir-inherited-base@sha256:dummy").
						ExpectedOriginType("intermediate").
						ExpectedStageAlias("builder").Build(),
				),
			},
		},
		// Check that if the builder image base image has a workdir set, a
		// relative WORKDIR command in the intermediate layers is correctly
		// joined and the origin is correctly set to "builder" and not "builder2".
		"workdir from builder image joined with relative workdir in intermediate": {
			TestImage: BuildDefinition{
				Tag: "test-relative-workdir-join",
				ContainerfileContent: `FROM localhost/workdir-inherited-base:latest AS builder
									   COPY syfter /opt/app/syfter

									   FROM localhost/workdir-inherited-base:latest AS builder2
									   WORKDIR app/
				                       COPY --from=builder /opt/app/syfter syfter

									   FROM scratch
									   COPY --from=builder2 /opt/app/syfter /syfter`,
				ContextDirectory: "../testdata/image_content",
			},
			BuilderImages: []BuildDefinition{
				{
					Tag: "localhost/workdir-inherited-base:latest",
					ContainerfileContent: `FROM scratch
										   # this copy command is here to make sure buildah
										   # creates at least one layer for the builder image
										   COPY uuider uuider
										   WORKDIR /opt`,

					ContextDirectory: "../testdata/image_content",
				},
			},
			ExpectedResult: PackageMetadata{
				Packages: syfterBuilder.
					ExpectedPullspec("localhost/workdir-inherited-base@sha256:dummy").
					ExpectedOriginType("intermediate").
					ExpectedStageAlias("builder").
					Build(),
			},
		},
		"copying from named build context does not fail": {
			TestImage: BuildDefinition{
				ContainerfileContent: `	FROM scratch
										COPY --from=named go2 go2`,
				ContextDirectory: "../testdata/image_content",
				BuildContexts: map[string]string{
					"named": "../testdata/image_content",
				},
			},
			ExpectedResult: PackageMetadata{Packages: []PackageMetadataItem{}},
		},
		// Test that the syft selection request configured in the scanner is
		// propagated to the syft scan. In this test, all go catalogers are
		// skipped; this should result in empty packages at the end.
		"syft selection request is propagated to the syft scan": {
			TestImage: BuildDefinition{
				ContainerfileContent: `FROM localhost/capo-builder/go_builder:latest as builder
										FROM scratch
										COPY --from=builder /opt/syfter /opt/syfter`,
				ContextDirectory: "../testdata/image_content",
			},
			BuilderImages: []BuildDefinition{
				{
					Tag: "localhost/capo-builder/go_builder:latest",
					ContainerfileContent: `FROM scratch
											COPY syfter /opt/syfter`,
					ContextDirectory: "../testdata/image_content",
				},
			},
			ScannerOptions: []Option{WithSelectCatalogers("-go")},
			ExpectedResult: PackageMetadata{
				Packages: []PackageMetadataItem{},
			},
		},
	}

	buildahBinary := getBuildahBinary(t)

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			if tc.SkipTestReason != "" {
				t.Skip(tc.SkipTestReason)
			}
			t.Parallel()
			fixture := newTestFixture(t, buildahBinary, tc.ScannerOptions...)
			normalizeTestCaseTags(&tc)
			if err := tc.run(t, fixture); err != nil {
				t.Fatal(err)
			}
		})
	}

}

// TestIntegrationOCIArchive verifies that content from a local archive base is
// attributed to the intermediate stage rather than to a pullable builder base.
func TestIntegrationOCIArchive(t *testing.T) {
	t.Parallel()

	const contextDir = "../testdata/image_content"
	archiveRef := "oci-archive:test-base-" + uuid.NewString() + ".ociarchive"
	fixture := newTestFixture(t, getBuildahBinary(t))
	t.Cleanup(func() {
		archivePath := filepath.Join(contextDir, filesystemTransportPath(archiveRef))
		if err := os.Remove(archivePath); err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Errorf("remove test OCI archive: %v", err)
		}
	})

	tc := TestCase{
		TestImage: BuildDefinition{
			Tag: "test-from-oci-archive-builder",
			ContainerfileContent: fmt.Sprintf(`FROM %s AS builder
										COPY uuider /content/uuider

										FROM scratch
										COPY --from=builder /content /content
										COPY --from=builder /opt /opt`, archiveRef),
			ContextDirectory: contextDir,
		},
		BuilderImages: []BuildDefinition{
			{
				Tag: archiveRef,
				ContainerfileContent: `FROM scratch
											COPY go2 /tmp/dummy
											COPY syfter /opt/syfter`,
				ContextDirectory: contextDir,
			},
		},
		ExpectedResult: PackageMetadata{
			Packages: slices.Concat(
				uuiderBuilder.ExpectedPullspec(archiveRef).
					ExpectedOriginType("intermediate").
					ExpectedStageAlias("builder").Build(),
				syfterBuilder.ExpectedPullspec(archiveRef).
					ExpectedOriginType("intermediate").
					ExpectedStageAlias("builder").Build(),
			),
		},
	}
	normalizeTestCaseTags(&tc)
	if err := tc.run(t, fixture); err != nil {
		t.Fatal(err)
	}
}

// TestIntegrationDigestLookup verifies that capo can resolve and scan builder
// images stored under digest-only names (as buildah does after pulling with a
// tag+digest ref).
func TestIntegrationDigestLookup(t *testing.T) {
	buildahBinary := getBuildahBinary(t)

	t.Run("Builder base image referenced by digest only", func(t *testing.T) {
		t.Parallel()
		fixture := newTestFixture(t, buildahBinary)
		digestRef := buildDigestOnlyImage(t, BuildDefinition{
			Tag:                  "localhost/capo-digest-test-builder:latest",
			ContainerfileContent: "FROM scratch\nCOPY syfter /opt/syfter",
			ContextDirectory:     "../testdata/image_content",
		}, fixture)

		tc := TestCase{
			TestImage: BuildDefinition{
				ContainerfileContent: fmt.Sprintf(`FROM %s as builder
FROM scratch
COPY --from=builder /opt/syfter /opt/syfter`, digestRef),
				ContextDirectory: "../testdata/image_content",
			},
			ExpectedResult: PackageMetadata{
				Packages: syfterBuilder.
					ExpectedOriginType("builder").
					ExpectedPullspec("localhost/capo-digest-test-builder@sha256:dummy").
					ExpectedStageAlias("builder").Build(),
			},
		}
		normalizeTestCaseTags(&tc)
		if err := tc.run(t, fixture); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("Builder base image referenced by tag+digest", func(t *testing.T) {
		t.Parallel()
		fixture := newTestFixture(t, buildahBinary)
		builderTag := "localhost/capo-tagdigest-test-builder:v1"
		digestRef := buildDigestOnlyImage(t, BuildDefinition{
			Tag:                  builderTag,
			ContainerfileContent: "FROM scratch\nCOPY syfter /opt/syfter",
			ContextDirectory:     "../testdata/image_content",
		}, fixture)

		// Reconstruct the tag+digest pullspec from the stored digest-only ref.
		_, digestSuffix, _ := strings.Cut(digestRef, "@")
		tagDigestRef := builderTag + "@" + digestSuffix

		tc := TestCase{
			TestImage: BuildDefinition{
				ContainerfileContent: fmt.Sprintf(`FROM %s as builder
FROM scratch
COPY --from=builder /opt/syfter /opt/syfter`, tagDigestRef),
				ContextDirectory: "../testdata/image_content",
			},
			ExpectedResult: PackageMetadata{
				Packages: syfterBuilder.
					ExpectedOriginType("builder").
					ExpectedPullspec("localhost/capo-tagdigest-test-builder@sha256:dummy").
					ExpectedStageAlias("builder").Build(),
			},
		}
		normalizeTestCaseTags(&tc)
		if err := tc.run(t, fixture); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("Builder base image referenced by tag+digest with port", func(t *testing.T) {
		t.Parallel()
		fixture := newTestFixture(t, buildahBinary)
		builderTag := "localhost:5000/capo-port-test-builder:v1"
		digestRef := buildDigestOnlyImage(t, BuildDefinition{
			Tag:                  builderTag,
			ContainerfileContent: "FROM scratch\nCOPY syfter /opt/syfter",
			ContextDirectory:     "../testdata/image_content",
		}, fixture)

		_, digestSuffix, _ := strings.Cut(digestRef, "@")
		tagDigestRef := builderTag + "@" + digestSuffix

		tc := TestCase{
			TestImage: BuildDefinition{
				ContainerfileContent: fmt.Sprintf(`FROM %s as builder
FROM scratch
COPY --from=builder /opt/syfter /opt/syfter`, tagDigestRef),
				ContextDirectory: "../testdata/image_content",
			},
			ExpectedResult: PackageMetadata{
				Packages: syfterBuilder.
					ExpectedOriginType("builder").
					ExpectedPullspec("localhost:5000/capo-port-test-builder@sha256:dummy").
					ExpectedStageAlias("builder").Build(),
			},
		}
		normalizeTestCaseTags(&tc)
		if err := tc.run(t, fixture); err != nil {
			t.Fatal(err)
		}
	})
}

type ErrorTestCase struct {
	ContainerfileContent string
	ExpectedError        error
}

func TestIntegrationScanErrors(t *testing.T) {
	testCases := map[string]ErrorTestCase{
		"Non-existent builder base image - Scan fails on pullspec resolve": {
			ContainerfileContent: `FROM nonexistent:latest as builder
								   FROM scratch
								   COPY --from=builder /file /file`,
			ExpectedError: ErrPullspecResolve,
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fixture := newTestFixture(t, "")
			cf, err := containerfile.Parse(strings.NewReader(tc.ContainerfileContent), containerfile.BuildOptions{})
			if err != nil {
				t.Fatalf("Failed to parse containerfile: %v", err)
			}

			_, err = fixture.scanner.Scan(cf)
			if !errors.Is(err, tc.ExpectedError) {
				t.Fatalf("expected %v, got: %v", tc.ExpectedError, err)
			}
		})
	}
}

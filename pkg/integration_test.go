//go:build integration

package capo

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/google/uuid"
	"github.com/konflux-ci/capo/pkg/containerfile"
	"github.com/magefile/mage/sh"
	"go.podman.io/storage"
)

type BuildDefinition struct {
	Tag                  string
	ContainerfileContent string
	ContextDirectory     string
}

type TestCase struct {
	Description     string
	LongDescription string
	SkipTestReason      string
	TestImage       BuildDefinition
	BuilderImages   []BuildDefinition
	ExpectedResult  PackageMetadata
	// SkipBuild skips image building for testing Scan() errors
	// when the referenced images are not expected to exist in storage.
	SkipBuild bool
	ExpectedError string
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

	version, err := sh.Output(binary, "--version")
	if err != nil {
		t.Logf("WARNING: could not determine buildah version: %v", err)
	} else {
		t.Logf("Buildah version: %s", version)
	}

	return binary
}

func (testCase *TestCase) checkExpectedError(t *testing.T, err error) error {
	if testCase.ExpectedError == "" {
		return err
	}
	if !strings.Contains(err.Error(), testCase.ExpectedError) {
		t.Errorf("[FAIL] expected error containing %q but got: %v", testCase.ExpectedError, err)
		return fmt.Errorf("wrong error: %w", err)
	}
	t.Logf("[PASS] Got expected error: %v", err)
	return nil
}

func (testCase *TestCase) build(store storage.Store, buildahBinary string) error {
	for _, builderImage := range testCase.BuilderImages {
		if err := builderImage.buildImage(store, buildahBinary, true); err != nil {
			return err
		}
	}
	if err := testCase.TestImage.buildImage(store, buildahBinary, false); err != nil {
		return err
	}
	return nil
}

func (testCase *TestCase) run(t *testing.T, store storage.Store, buildahBinary string) error {
	if testCase.Description != "" {
		t.Logf("=== Running test: %s ===", testCase.Description)
	}
	if testCase.LongDescription != "" {
		t.Logf("Notes:\n%s", testCase.LongDescription)
	}
	if testCase.SkipTestReason != "" {
		t.Logf("Test skipped: %s", testCase.SkipTestReason)
		return nil
	}
	if !testCase.SkipBuild {
		defer testCase.cleanUp(t, store)
		if err := testCase.build(store, buildahBinary); err != nil {
			return testCase.checkExpectedError(t, err)
		}
	}

	stages, err := containerfile.Parse(strings.NewReader(testCase.TestImage.ContainerfileContent), containerfile.BuildOptions{})
	if err != nil {
		return testCase.checkExpectedError(t, err)
	}

	result, err := Scan(stages)
	if err != nil {
		return testCase.checkExpectedError(t, err)
	}

	if testCase.ExpectedError != "" {
		t.Errorf("expected error containing %q but all steps succeeded", testCase.ExpectedError)
		return errors.New("expected error but got none")
	}

	// Compare packages order-independently using go-cmp:
	// - SortSlices: ensures comparison is order-independent by sorting on PackageURL
	// - EquateEmpty: treats nil and empty slices as equal
	// - FilterPath on Pullspec: strips @sha256: digests before comparing pullspecs,
	//   since actual digests vary between builds and should not cause test failures
	diff := cmp.Diff(testCase.ExpectedResult.Packages, result.Packages,
		cmpopts.SortSlices(func(a, b PackageMetadataItem) bool { return a.PackageURL < b.PackageURL }),
		cmpopts.EquateEmpty(),
		cmp.FilterPath(func(p cmp.Path) bool {
			return p.String() == "Pullspec"
		}, cmp.Comparer(func(a, b string) bool {
			return normalizePullspec(a) == normalizePullspec(b)
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
func (buildDef *BuildDefinition) buildImage(store storage.Store, buildahBinary string, isBuilder bool) (err error) {
	tag := buildDef.Tag

	if _, err := store.Lookup(tag); err == nil && isBuilder {
		return nil
	}

	// Create a temporary file for the Containerfile content
	tmpFile, err := os.CreateTemp("", "Containerfile-*.tmp")
	if err != nil {
		return err
	}
	defer func() {
		closeErr := errors.Join(
			tmpFile.Close(),
			os.Remove(tmpFile.Name()),
		)
		if err == nil {
			err = closeErr
		}
	}()

	// Write the Containerfile content to the temporary file
	if _, err := tmpFile.WriteString(buildDef.ContainerfileContent); err != nil {
		return err
	}

	args := []string{
		"build",
		"-f",
		tmpFile.Name(),
		"--tag",
		tag,
	}
	if !isBuilder {
		args = append(args, "--save-stages", "--stage-labels")
	}
	args = append(args, buildDef.ContextDirectory)

	return sh.RunV(buildahBinary, args...)
}

func normalizePullspec(pullspec string) string {
	if atIndex := strings.Index(pullspec, "@sha256:"); atIndex != -1 {
		return pullspec[:atIndex]
	}
	return pullspec
}

// cleanUpIntermediateLayers deletes all images without names/tags from the store.
// This technically cleans more than just intermediate layers, but it shouldn't matter.
func cleanUpIntermediateLayers(t *testing.T, store storage.Store) error {
	images, err := store.Images()
	if err != nil {
		return err
	}
	for _, image := range images {
		if len(image.Names) == 0 {
			t.Logf("Cleaning up unnamed image %s", image.ID)
			store.DeleteImage(image.ID, true)
		}
	}
	return nil
}

// cleanUpTestImage unmounts and deletes a test image by tag.
// Returns nil if the image doesn't exist.
func (testCase *TestCase) cleanUp(t *testing.T, store storage.Store) error {
	for _, builderImage := range testCase.BuilderImages {
		builderImage.cleanUp(t, store)
	}
	testCase.TestImage.cleanUp(t, store)
	cleanUpIntermediateLayers(t, store)
	return nil
}

func (buildDef *BuildDefinition) cleanUp(t *testing.T, store storage.Store) error {
	t.Logf("Cleaning up builder image %s", buildDef.Tag)
	imageID, err := store.Lookup(buildDef.Tag)
	if err != nil {
		t.Logf("Image %s not found, skipping cleanup: %v", buildDef.Tag, err)
		return nil
	}
	_, err = store.UnmountImage(imageID, true)
	if err != nil {
		t.Logf("Failed to unmount image %s: %v", buildDef.Tag, err)
	}
	_, err = store.DeleteImage(imageID, true)
	if err != nil {
		return err
	}
	return nil
}

// normalizeTag normalizes a container image tag by:
// - Adding `localhost/` prefix if the tag doesn't contain a registry URL (no `/`)
// - Adding `:latest` suffix if the tag doesn't contain a tag (no `:`)
func normalizeTag(tag string) string {
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

// TestIntegration runs end-to-end tests: builds test images, scans them for packages,
// and compares results against expected package metadata.
func TestIntegration(t *testing.T) {

	testCases := []TestCase{
		{
			Description: "Simple COPY from builder",
			TestImage: BuildDefinition{
				ContainerfileContent: `FROM localhost/capo-builder/go_builder:latest as builder
									   FROM scratch
									   COPY --from=builder /opt/go.mod /opt/go.mod
				`,
				ContextDirectory: "../testdata/image_content",
			},
			BuilderImages: []BuildDefinition{
				{
					Tag: "localhost/capo-builder/go_builder:latest",
					ContainerfileContent: `FROM scratch
										   COPY go.mod /opt/go.mod
					`,
					ContextDirectory: "../testdata/image_content",
				},
			},
			ExpectedResult: PackageMetadata{
				Packages: []PackageMetadataItem{
					{
						PackageURL: "pkg:golang/github.com/anchore/syft@v1.32.0",
						OriginType: "builder",
						Pullspec:   "localhost/capo-builder/go_builder@sha256:35623538333b2cf7e59ba286cb60bfdda7bc20cc69abdfe9484f8238f363de57",
						StageAlias: "builder",
					},
				},
			},
		},
		{
			Description: "Non-existent builder base - Scan fails on pullspec resolve",
			SkipBuild:   true,
			TestImage: BuildDefinition{
				ContainerfileContent: `FROM nonexistent:latest as builder
									   FROM scratch
									   COPY --from=builder /file /file`,
			},
			ExpectedError: "failed to resolve pullspec",
		},
	}
	// Normalize all tags in test cases
	for i := range testCases {
		normalizeTestCaseTags(&testCases[i])
	}

	store, err := SetupStore()
	if err != nil {
		t.Fatalf("Failed to setup store: %+v", err)
	}

	buildahBinary := getBuildahBinary(t)

	var passed, skipped, failed []string
	for _, testCase := range testCases {
		if testCase.SkipTestReason != "" {
			skipped = append(skipped, testCase.Description)
			t.Logf("=== Test case %s skipped. ===", testCase.Description)
			continue
		}
		err := testCase.run(t, store, buildahBinary)
		if err != nil {
			failed = append(failed, testCase.Description)
			t.Errorf("=== Test case %s failed: %+v ===", testCase.Description, err)
		} else {
			passed = append(passed, testCase.Description)
			t.Logf("=== Test case %s passed. ===", testCase.Description)
		}
	}

	t.Logf("\n=== SUMMARY: %d passed, %d skipped, %d failed ===", len(passed), len(skipped), len(failed))
	for _, name := range passed {
		t.Logf("  PASS: %s", name)
	}
	for _, name := range skipped {
		t.Logf("  SKIP: %s", name)
	}
	for _, name := range failed {
		t.Logf("  FAIL: %s", name)
	}
}

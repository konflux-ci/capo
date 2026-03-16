//go:build integration

package capo

import (
	"errors"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/google/uuid"
	"github.com/konflux-ci/capo/pkg/containerfile"
	"github.com/magefile/mage/sh"
	"go.podman.io/storage"
	"os"
	"strings"
	"testing"
)

type BuildDefinition struct {
	Tag                  string
	ContainerfileContent string
	ContextDirectory     string
}

type TestCase struct {
	TestImage      BuildDefinition
	BuilderImages  []BuildDefinition
	ExpectedResult PackageMetadata
}

func (testCase *TestCase) build(store storage.Store) error {
	for _, builderImage := range testCase.BuilderImages {
		if err := builderImage.buildImage(store, true); err != nil {
			return err
		}
	}
	if err := testCase.TestImage.buildImage(store, false); err != nil {
		return err
	}
	return nil
}

func (testCase *TestCase) run(t *testing.T, store storage.Store) error {
	if err := testCase.build(store); err != nil {
		return err
	}
	defer testCase.cleanUp(t, store)
	stages, err := containerfile.Parse(strings.NewReader(testCase.TestImage.ContainerfileContent), containerfile.BuildOptions{})
	if err != nil {
		return err
	}
	result, err := Scan(stages)
	if err != nil {
		return err
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
func (buildDef *BuildDefinition) buildImage(store storage.Store, isBuilder bool) (err error) {
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
		err = tmpFile.Close()
		if err == nil {
			err = os.Remove(tmpFile.Name())
		}
	}()

	// Write the Containerfile content to the temporary file
	if _, err := tmpFile.WriteString(buildDef.ContainerfileContent); err != nil {
		return err
	}

	// Build using buildah binary: buildah build --layers -f Containerfile --tag tag contextDir
	args := []string{
		"build",
		"-f",
		tmpFile.Name(),
		"--tag",
		tag,
	}
	if !isBuilder {
		args = append(args, "--layers")
	}
	args = append(args, buildDef.ContextDirectory)

	return sh.RunV("buildah", args...)
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
	}
	// Normalize all tags in test cases
	for i := range testCases {
		normalizeTestCaseTags(&testCases[i])
	}

	store, err := SetupStore()
	if err != nil {
		t.Fatalf("Failed to setup store: %+v", err)
	}
	for _, testCase := range testCases {
		err := testCase.run(t, store)
		if err != nil {
			t.Errorf("Test case %s failed: %+v", testCase.TestImage.Tag, err)
		}
	}
}

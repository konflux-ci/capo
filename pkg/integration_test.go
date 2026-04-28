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
	SkipTestReason  string
	TestImage       BuildDefinition
	BuilderImages   []BuildDefinition
	ExpectedResult  PackageMetadata
	// SkipBuild skips image building for testing Scan() errors
	// when the referenced images are not expected to exist in storage.
	SkipBuild     bool
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
			Description: "Identification of the builder base image content - no intermediate image, single file copy",
			TestImage: BuildDefinition{
				ContainerfileContent: `FROM localhost/capo-builder/go_builder:latest as builder
										FROM scratch
										COPY --from=builder /opt/go.mod /opt/go.mod`,
				ContextDirectory: "../testdata/image_content",
			},
			BuilderImages: []BuildDefinition{
				{
					Tag: "localhost/capo-builder/go_builder:latest",
					ContainerfileContent: `FROM scratch
											COPY go.mod /opt/go.mod`,
					ContextDirectory: "../testdata/image_content",
				},
			},
			ExpectedResult: PackageMetadata{
				Packages: []PackageMetadataItem{
					{
						PackageURL: "pkg:golang/github.com/anchore/syft@v1.32.0",
						OriginType: "builder",
						Pullspec:   "localhost/capo-builder/go_builder@sha256:dummy",
						StageAlias: "builder",
					},
				},
			},
		},
		{
			Description: "Identification of the builder and intermediate content - single file COPY from intermediate",
			TestImage: BuildDefinition{
				Tag: "test-single-file-copy",
				ContainerfileContent: `FROM localhost/singlefile-base:latest AS builder
										COPY go_uuid.mod /content/go.mod

										FROM scratch
										COPY --from=builder /content/go.mod /content/go.mod`,
				ContextDirectory: "../testdata/image_content",
			},
			BuilderImages: []BuildDefinition{
				{
					Tag: "localhost/singlefile-base:latest",
					ContainerfileContent: `FROM scratch
											COPY go2.mod /untracked/base/go.mod`,
					ContextDirectory: "../testdata/image_content",
				},
			},
			ExpectedResult: PackageMetadata{
				Packages: []PackageMetadataItem{
					{
						PackageURL: "pkg:golang/github.com/google/uuid@v1.6.0",
						OriginType: "intermediate",
						Pullspec:   "localhost/singlefile-base@sha256:dummy",
						StageAlias: "builder",
					},
				},
			},
		},
		{
			Description: "Identification of the builder and intermediate content - directory copy",
			TestImage: BuildDefinition{
				Tag: "test-builder-intermediate",
				ContainerfileContent: `FROM localhost/capo-builder/go_builder:latest AS builder
										COPY go_uuid.mod /opt/app2/go.mod
										COPY go_text.mod /unused/go.mod

										FROM scratch
										COPY --from=builder /opt/ /opt/`,
				ContextDirectory: "../testdata/image_content",
			},
			BuilderImages: []BuildDefinition{
				{
					Tag: "localhost/capo-builder/go_builder:latest",
					ContainerfileContent: `FROM scratch
											COPY go.mod /opt/app1/go.mod
											COPY go_sync.mod /base_unused/go.mod`,
					ContextDirectory: "../testdata/image_content",
				},
			},
			ExpectedResult: PackageMetadata{
				Packages: []PackageMetadataItem{
					{
						PackageURL: "pkg:golang/github.com/anchore/syft@v1.32.0",
						OriginType: "builder",
						Pullspec:   "localhost/capo-builder/go_builder@sha256:dummy",
						StageAlias: "builder",
					},
					{
						PackageURL: "pkg:golang/github.com/google/uuid@v1.6.0",
						OriginType: "intermediate",
						Pullspec:   "localhost/capo-builder/go_builder@sha256:dummy",
						StageAlias: "builder",
					},
				},
			},
		},
		{
			Description: "Non-existent builder base image - Scan fails on pullspec resolve",
			SkipBuild:   true,
			TestImage: BuildDefinition{
				ContainerfileContent: `FROM nonexistent:latest as builder
										FROM scratch
										COPY --from=builder /file /file`,
			},
			ExpectedError: "failed to resolve pullspec",
		},
		{
			Description: "Two stages with same pullspec but different intermediate content",
			TestImage: BuildDefinition{
				Tag: "test-same-pullspec-different-content",
				ContainerfileContent: `FROM localhost/builder-base:latest AS stage1
										COPY go_uuid.mod /opt/app1/go.mod
										COPY go_text.mod /untracked/s1/go.mod

										FROM localhost/builder-base:latest AS stage2
										COPY go_exp.mod /opt/app2/go.mod
										COPY go_text.mod /untracked/s2/go.mod

										FROM scratch
										COPY --from=stage1 /opt/ /opt/
										COPY --from=stage2 /opt/app2/ /opt/app2/`,
				ContextDirectory: "../testdata/image_content",
			},
			BuilderImages: []BuildDefinition{
				{
					Tag: "localhost/builder-base:latest",
					ContainerfileContent: `FROM scratch
											COPY go.mod /opt/base/go.mod
											COPY go2.mod /untracked/base/go.mod`,
					ContextDirectory: "../testdata/image_content",
				},
			},
			ExpectedResult: PackageMetadata{
				Packages: []PackageMetadataItem{
					{
						PackageURL: "pkg:golang/github.com/anchore/syft@v1.32.0",
						OriginType: "builder",
						Pullspec:   "localhost/builder-base@sha256:dummy",
						StageAlias: "stage1",
					},
					{
						PackageURL: "pkg:golang/github.com/google/uuid@v1.6.0",
						OriginType: "intermediate",
						Pullspec:   "localhost/builder-base@sha256:dummy",
						StageAlias: "stage1",
					},
					{
						PackageURL: "pkg:golang/golang.org/x/exp@v0.0.0-20240808152545-0cdaa3abc0fa",
						OriginType: "intermediate",
						Pullspec:   "localhost/builder-base@sha256:dummy",
						StageAlias: "stage2",
					},
				},
			},
		},
		{
			Description: "Multiple sources in single COPY --from command",
			TestImage: BuildDefinition{
				Tag: "test-multi-source-copy",
				ContainerfileContent: `FROM localhost/multi-base:latest AS builder
										COPY go_uuid.mod /src1/go.mod
										COPY go_exp.mod /src2/go.mod
										COPY go_text.mod /untracked/builder/go.mod

										FROM scratch
										COPY --from=builder /base /src1 /src2 /dest/`,
				ContextDirectory: "../testdata/image_content",
			},
			BuilderImages: []BuildDefinition{
				{
					Tag: "localhost/multi-base:latest",
					ContainerfileContent: `FROM scratch
											COPY go.mod /base/go.mod
											COPY go2.mod /untracked/base/go.mod`,
					ContextDirectory: "../testdata/image_content",
				},
			},
			ExpectedResult: PackageMetadata{
				Packages: []PackageMetadataItem{
					{
						PackageURL: "pkg:golang/github.com/anchore/syft@v1.32.0",
						OriginType: "builder",
						Pullspec:   "localhost/multi-base@sha256:dummy",
						StageAlias: "builder",
					},
					{
						PackageURL: "pkg:golang/github.com/google/uuid@v1.6.0",
						OriginType: "intermediate",
						Pullspec:   "localhost/multi-base@sha256:dummy",
						StageAlias: "builder",
					},
					{
						PackageURL: "pkg:golang/golang.org/x/exp@v0.0.0-20240808152545-0cdaa3abc0fa",
						OriginType: "intermediate",
						Pullspec:   "localhost/multi-base@sha256:dummy",
						StageAlias: "builder",
					},
				},
			},
		},
		{
			Description: "ARG substitution",
			TestImage: BuildDefinition{
				Tag: "test-arg-substitution",
				ContainerfileContent: `ARG BASE_IMG=localhost/arg-base:latest
										ARG BUILDER_STAGE=builder

										FROM ${BASE_IMG} AS ${BUILDER_STAGE}
										COPY go_uuid.mod /content/app2/go.mod
										COPY go_text.mod /untracked/builder/go.mod

										FROM scratch
										ARG BUILDER_STAGE=builder
										COPY --from=${BUILDER_STAGE} /content /content`,
				ContextDirectory: "../testdata/image_content",
			},
			BuilderImages: []BuildDefinition{
				{
					Tag: "localhost/arg-base:latest",
					ContainerfileContent: `FROM scratch
											COPY go.mod /content/app1/go.mod
											COPY go2.mod /untracked/base/go.mod`,
					ContextDirectory: "../testdata/image_content",
				},
			},
			ExpectedResult: PackageMetadata{
				Packages: []PackageMetadataItem{
					{
						PackageURL: "pkg:golang/github.com/anchore/syft@v1.32.0",
						OriginType: "builder",
						Pullspec:   "localhost/arg-base@sha256:dummy",
						StageAlias: "builder",
					},
					{
						PackageURL: "pkg:golang/github.com/google/uuid@v1.6.0",
						OriginType: "intermediate",
						Pullspec:   "localhost/arg-base@sha256:dummy",
						StageAlias: "builder",
					},
				},
			},
		},
		{
			Description:    "Content cascade through COPY --from between builder stages",
			SkipTestReason: "[Priority: medium] final image is expected to contain content also from forwarder builder base image (exp package). This mght be an edgecase in capo tracing content implementation",
			TestImage: BuildDefinition{
				Tag: "test-copy-cascade-builders",
				ContainerfileContent: `FROM localhost/base1:latest AS builder
										COPY go_uuid.mod /content/app3/go.mod

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
											COPY go.mod /content/app1/go.mod
											COPY go2.mod /untracked/base1/go.mod`,
					ContextDirectory: "../testdata/image_content",
				},
				{
					Tag: "localhost/base2:latest",
					ContainerfileContent: `FROM scratch
											COPY go_exp.mod /content/app2/go.mod
											COPY go2.mod /untracked/base2/go.mod`,
					ContextDirectory: "../testdata/image_content",
				},
			},
			ExpectedResult: PackageMetadata{
				Packages: []PackageMetadataItem{
					{
						PackageURL: "pkg:golang/github.com/anchore/syft@v1.32.0",
						OriginType: "builder",
						Pullspec:   "localhost/base1@sha256:dummy",
						StageAlias: "builder",
					},
					{
						PackageURL: "pkg:golang/github.com/google/uuid@v1.6.0",
						OriginType: "intermediate",
						Pullspec:   "localhost/base1@sha256:dummy",
						StageAlias: "builder",
					},
					{
						PackageURL: "pkg:golang/golang.org/x/exp@v0.0.0-20240808152545-0cdaa3abc0fa",
						OriginType: "builder",
						Pullspec:   "localhost/base2@sha256:dummy",
						StageAlias: "forwarder",
					},
				},
			},
		},
		{
			Description:    "Builder used as final stage base - builder content excluded (parent content), intermediate traced",
			SkipTestReason: "[Priority: low/medium] capo does not distinguish builder content from final base content when builder is used as FROM base",
			LongDescription: `
A builder stage can be used as the final image base (FROM alias_as_base).
When this happens, its base image content becomes the final image's parent
content — externally built, not reported by capo (responsibility of parent
content identification in mobster).
However, its intermediate content (created during THIS build) remains
intermediate and should be reported, because fixing it requires changing
this containerfile.

Expected output:
- alias_as_base BASE content (sync from builder2) → NOT in output (now final's parent)
- alias_as_base INTERMEDIATE content (exp) → IN output as intermediate
- Content traced through alias_as_base back to builder → IN output (traced to builder)
- Direct COPY --from=builder in final → IN output

This distinction (parent base vs intermediate of a stage used as final base)
should be verified in mobster as well.`,
			TestImage: BuildDefinition{
				Tag: "test-final-uses-builder-base",
				ContainerfileContent: `FROM localhost/builder1:latest AS builder
										COPY go_uuid.mod /content/go.mod
										COPY go_sync.mod /untracked/builder/go.mod

										FROM localhost/builder2:latest AS alias_as_base
										COPY go_exp.mod /content/go.mod
										COPY --from=builder /base1a /base1ainparent

										FROM alias_as_base
										COPY --from=builder /base1 /base1
										COPY --from=builder /content /content
										COPY --from=alias_as_base /base1ainparent /base1a
										COPY --from=alias_as_base /base2 /base2
										COPY --from=alias_as_base /content /content2`,
				ContextDirectory: "../testdata/image_content",
			},
			BuilderImages: []BuildDefinition{
				{
					Tag: "localhost/builder1:latest",
					ContainerfileContent: `FROM scratch
											COPY go.mod /base1/go.mod
											COPY go_text.mod /base1a/go.mod
											COPY go2.mod /untracked/b1/go.mod`,
					ContextDirectory: "../testdata/image_content",
				},
				{
					Tag: "localhost/builder2:latest",
					ContainerfileContent: `FROM scratch
											COPY go_sync.mod /base2/go.mod
											COPY go2.mod /untracked/b2/go.mod`,
					ContextDirectory: "../testdata/image_content",
				},
			},
			ExpectedResult: PackageMetadata{
				Packages: []PackageMetadataItem{
					{
						PackageURL: "pkg:golang/github.com/anchore/syft@v1.32.0",
						OriginType: "builder",
						Pullspec:   "localhost/builder1@sha256:dummy",
						StageAlias: "builder",
					},
					{
						PackageURL: "pkg:golang/golang.org/x/text@v0.18.0",
						OriginType: "builder",
						Pullspec:   "localhost/builder1@sha256:dummy",
						StageAlias: "builder",
					},
					{
						PackageURL: "pkg:golang/github.com/google/uuid@v1.6.0",
						OriginType: "intermediate",
						Pullspec:   "localhost/builder1@sha256:dummy",
						StageAlias: "builder",
					},
					{
						PackageURL: "pkg:golang/golang.org/x/exp@v0.0.0-20240808152545-0cdaa3abc0fa",
						OriginType: "intermediate",
						Pullspec:   "localhost/builder2@sha256:dummy",
						StageAlias: "alias_as_base",
					},
				},
			},
		},
		{
			Description: "Stage alias with same name as image",
			LongDescription: `Stage alias "alpine" collides with real image name. If buildah/capo
resolves "FROM alpine AS stage2" as the real alpine image instead of the stage
alias, COPY --from=stage2 would copy the entire alpine filesystem and the test
would fail with unexpected packages from the alpine base.`,
			SkipTestReason: "[Priority: low] test on real build and align capo - should alpine be resolved as alpine:latest or as chained stage?",
			TestImage: BuildDefinition{
				Tag: "test-alias-matches-image",
				ContainerfileContent: `FROM localhost/builderwithbadalias:latest AS alpine
										COPY go_uuid.mod /content/app2/go.mod

										FROM alpine AS stage2
										COPY go_exp.mod /content/app3/go.mod

										FROM scratch
										COPY --from=alpine /base /base
										COPY --from=alpine /content /content/stage1
										COPY --from=stage2 / /content/all`,
				ContextDirectory: "../testdata/image_content",
			},
			BuilderImages: []BuildDefinition{
				{
					Tag: "builderwithbadalias",
					ContainerfileContent: `FROM scratch
											COPY go.mod /content/app1/go.mod`,
					ContextDirectory: "../testdata/image_content",
				},
			},
			ExpectedResult: PackageMetadata{
				Packages: []PackageMetadataItem{
					{
						PackageURL: "pkg:golang/github.com/anchore/syft@v1.32.0",
						OriginType: "builder",
						Pullspec:   "localhost/alpine@sha256:dummy",
						StageAlias: "alpine",
					},
					{
						PackageURL: "pkg:golang/github.com/google/uuid@v1.6.0",
						OriginType: "intermediate",
						Pullspec:   "localhost/alpine@sha256:dummy",
						StageAlias: "alpine",
					},
					{
						PackageURL: "pkg:golang/golang.org/x/exp@v0.0.0-20240808152545-0cdaa3abc0fa",
						OriginType: "intermediate",
						Pullspec:   "localhost/alpine@sha256:dummy",
						StageAlias: "stage2",
					},
				},
			},
		},
		{
			Description:    "Path prefix collision - /opt should not match /optional",
			SkipTestReason: "[Priority: high] bug in includes() - false positive prefix matching: /opt matches /optional",
			TestImage: BuildDefinition{
				Tag: "test-path-prefix-collision",
				ContainerfileContent: `FROM localhost/prefix-base:latest AS builder
										COPY go_uuid.mod /opt/go.mod
										COPY go_exp.mod /optional/go.mod

										FROM scratch
										COPY --from=builder /opt /opt`,
				ContextDirectory: "../testdata/image_content",
			},
			BuilderImages: []BuildDefinition{
				{
					Tag: "localhost/prefix-base:latest",
					ContainerfileContent: `FROM scratch
											COPY go.mod /base/go.mod`,
					ContextDirectory: "../testdata/image_content",
				},
			},
			ExpectedResult: PackageMetadata{
				Packages: []PackageMetadataItem{
					{
						PackageURL: "pkg:golang/github.com/google/uuid@v1.6.0",
						OriginType: "intermediate",
						Pullspec:   "localhost/prefix-base@sha256:dummy",
						StageAlias: "builder",
					},
				},
			},
		},
		{
			Description: "Overlapping COPY destinations in builder stage",
			LongDescription: `Two providers copy go.mod into the same /dest/ in builder stage.
Provider2 overwrites provider1's file. Only exp package (provider2) should be in the
final image. However, capo currently cannot distinguish overlapping writes —
it sees both files in the intermediate layer diff and reports both. Correct
behavior requires Containerfile-level instruction ordering awareness.`,
			SkipTestReason: "[Priority: medium] capo does not track overlapping COPY destinations - reports both providers instead of only the last one",
			TestImage: BuildDefinition{
				Tag: "test-overlapping-dest",
				ContainerfileContent: `FROM localhost/overlap-base:latest AS provider1
									   COPY go_uuid.mod /src1/go.mod

									   FROM localhost/overlap-base:latest AS provider2
									   COPY go_exp.mod /src2/go.mod

									   FROM localhost/overlap-base:latest AS builder
									   COPY --from=provider1 /src1/ /dest/
									   COPY --from=provider2 /src2/ /dest/

									   FROM scratch
									   COPY --from=builder /dest/go.mod /dest/go.mod`,
				ContextDirectory: "../testdata/image_content",
			},
			BuilderImages: []BuildDefinition{
				{
					Tag: "localhost/overlap-base:latest",
					ContainerfileContent: `FROM scratch
										   COPY go.mod /base/go.mod`,
					ContextDirectory: "../testdata/image_content",
				},
			},
			ExpectedResult: PackageMetadata{
				Packages: []PackageMetadataItem{
					{
						PackageURL: "pkg:golang/golang.org/x/exp@v0.0.0-20240808152545-0cdaa3abc0fa",
						OriginType: "intermediate",
						Pullspec:   "localhost/overlap-base@sha256:dummy",
						StageAlias: "provider2",
					},
				},
			},
		},
		{
			Description:    "Intermediate content overwrites builder base content at same path",
			SkipTestReason: "[Priority: medium] capo scans builder base and intermediate independently - does not detect that intermediate overwrites base at same path",
			TestImage: BuildDefinition{
				Tag: "test-intermediate-overwrites-base",
				ContainerfileContent: `FROM localhost/overwrite-base:latest AS builder
									   COPY go_uuid.mod /opt/app1/go.mod

									   FROM scratch
									   COPY --from=builder /opt /opt`,
				ContextDirectory: "../testdata/image_content",
			},
			BuilderImages: []BuildDefinition{
				{
					Tag: "localhost/overwrite-base:latest",
					ContainerfileContent: `FROM scratch
										   COPY go.mod /opt/app1/go.mod`,
					ContextDirectory: "../testdata/image_content",
				},
			},
			ExpectedResult: PackageMetadata{
				Packages: []PackageMetadataItem{
					{
						PackageURL: "pkg:golang/github.com/google/uuid@v1.6.0",
						OriginType: "intermediate",
						Pullspec:   "localhost/overwrite-base@sha256:dummy",
						StageAlias: "builder",
					},
				},
			},
		},
		{
			Description:    "[Chained stages] Grandparent, parent and child builder cascade with intermediate content",
			SkipTestReason: "[Priority: high] chained stages not yet supported",
			TestImage: BuildDefinition{
				Tag: "test-chained-stages-cascade",
				ContainerfileContent: `FROM localhost/builder-sync:latest AS grandparent
										COPY go_uuid.mod /opt/app2/go.mod
										COPY go_text.mod /untracked/gp/go.mod

										FROM grandparent AS parent
										COPY go_exp.mod /opt/app3/go.mod
										COPY go_text.mod /untracked/p/go.mod

										FROM parent AS child
										COPY go_sync.mod /opt/app4/go.mod
										COPY go_text.mod /untracked/c/go.mod

										FROM scratch
										COPY --from=child /opt/ /opt/`,
				ContextDirectory: "../testdata/image_content",
			},
			BuilderImages: []BuildDefinition{
				{
					Tag: "localhost/builder-sync:latest",
					ContainerfileContent: `FROM scratch
											COPY go.mod /opt/app1/go.mod
											COPY go2.mod /untracked/base/go.mod`,
					ContextDirectory: "../testdata/image_content",
				},
			},
			ExpectedResult: PackageMetadata{
				Packages: []PackageMetadataItem{
					{
						PackageURL: "pkg:golang/github.com/anchore/syft@v1.32.0",
						OriginType: "builder",
						Pullspec:   "localhost/builder-sync@sha256:dummy",
						StageAlias: "grandparent",
					},
					{
						PackageURL: "pkg:golang/github.com/google/uuid@v1.6.0",
						OriginType: "intermediate",
						Pullspec:   "localhost/builder-sync@sha256:dummy",
						StageAlias: "grandparent",
					},
					{
						PackageURL: "pkg:golang/golang.org/x/exp@v0.0.0-20240808152545-0cdaa3abc0fa",
						OriginType: "intermediate",
						Pullspec:   "localhost/builder-sync@sha256:dummy",
						StageAlias: "parent",
					},
					{
						PackageURL: "pkg:golang/golang.org/x/sync@v0.8.0",
						OriginType: "intermediate",
						Pullspec:   "localhost/builder-sync@sha256:dummy",
						StageAlias: "child",
					},
				},
			},
		},
		{
			Description:    "[Chained stages] Empty child chained stage (no build instructions)",
			SkipTestReason: "[Priority: high] chained stages not yet supported",
			TestImage: BuildDefinition{
				Tag: "test-empty-chained-stage",
				ContainerfileContent: `FROM localhost/capo-builder/go_builder:latest AS parent-stage
										COPY go_uuid.mod /opt/app2/go.mod
										COPY go_text.mod /untracked/parent/go.mod

										FROM parent-stage AS empty-child

										FROM scratch
										COPY --from=empty-child /opt/ /opt/`,
				ContextDirectory: "../testdata/image_content",
			},
			BuilderImages: []BuildDefinition{
				{
					Tag: "localhost/capo-builder/go_builder:latest",
					ContainerfileContent: `FROM scratch
											COPY go.mod /opt/app1/go.mod
											COPY go2.mod /untracked/base/go.mod`,
					ContextDirectory: "../testdata/image_content",
				},
			},
			ExpectedResult: PackageMetadata{
				Packages: []PackageMetadataItem{
					{
						PackageURL: "pkg:golang/github.com/anchore/syft@v1.32.0",
						OriginType: "builder",
						Pullspec:   "localhost/capo-builder/go_builder@sha256:dummy",
						StageAlias: "parent-stage",
					},
					{
						PackageURL: "pkg:golang/github.com/google/uuid@v1.6.0",
						OriginType: "intermediate",
						Pullspec:   "localhost/capo-builder/go_builder@sha256:dummy",
						StageAlias: "parent-stage",
					},
				},
			},
		},
		{
			Description:    "[Chained stages] Multiple empty chained stages with intermediate only in last stage",
			SkipTestReason: "[Priority: high] chained stages not yet supported",
			TestImage: BuildDefinition{
				Tag: "test-empty-chain-cascade",
				ContainerfileContent: `FROM localhost/builder-base:latest AS first

										FROM first AS second

										FROM second AS third
										COPY go_uuid.mod /opt/app/go.mod
										COPY go_text.mod /untracked/third/go.mod

										FROM scratch
										COPY --from=third /opt/ /opt/`,
				ContextDirectory: "../testdata/image_content",
			},
			BuilderImages: []BuildDefinition{
				{
					Tag: "localhost/builder-base:latest",
					ContainerfileContent: `FROM scratch
											COPY go.mod /opt/base/go.mod
											COPY go2.mod /untracked/base/go.mod`,
					ContextDirectory: "../testdata/image_content",
				},
			},
			ExpectedResult: PackageMetadata{
				Packages: []PackageMetadataItem{
					{
						PackageURL: "pkg:golang/github.com/anchore/syft@v1.32.0",
						OriginType: "builder",
						Pullspec:   "localhost/builder-base@sha256:dummy",
						StageAlias: "first",
					},
					{
						PackageURL: "pkg:golang/github.com/google/uuid@v1.6.0",
						OriginType: "intermediate",
						Pullspec:   "localhost/builder-base@sha256:dummy",
						StageAlias: "third",
					},
				},
			},
		},
		{
			Description:    "[Chained stages] Complex cascade: non-empty, empty, non-empty, empty, non-empty",
			SkipTestReason: "[Priority: high] chained stages not yet supported",
			TestImage: BuildDefinition{
				Tag: "test-complex-cascade",
				ContainerfileContent: `FROM localhost/builder-base:latest AS stage1
										COPY go_uuid.mod /opt/app1/go.mod
										COPY go_text.mod /untracked/s1/go.mod

										FROM stage1 AS stage2

										FROM stage2 AS stage3
										COPY go_exp.mod /opt/app2/go.mod
										COPY go_text.mod /untracked/s3/go.mod

										FROM stage3 AS stage4

										FROM stage4 AS stage5
										COPY go_sync.mod /opt/app3/go.mod
										COPY go_text.mod /untracked/s5/go.mod

										FROM scratch
										COPY --from=stage5 /opt/ /opt/`,
				ContextDirectory: "../testdata/image_content",
			},
			BuilderImages: []BuildDefinition{
				{
					Tag: "localhost/builder-base:latest",
					ContainerfileContent: `FROM scratch
											COPY go.mod /opt/base/go.mod
											COPY go2.mod /untracked/base/go.mod`,
					ContextDirectory: "../testdata/image_content",
				},
			},
			ExpectedResult: PackageMetadata{
				Packages: []PackageMetadataItem{
					{
						PackageURL: "pkg:golang/github.com/anchore/syft@v1.32.0",
						OriginType: "builder",
						Pullspec:   "localhost/builder-base@sha256:dummy",
						StageAlias: "stage1",
					},
					{
						PackageURL: "pkg:golang/github.com/google/uuid@v1.6.0",
						OriginType: "intermediate",
						Pullspec:   "localhost/builder-base@sha256:dummy",
						StageAlias: "stage1",
					},
					{
						PackageURL: "pkg:golang/golang.org/x/exp@v0.0.0-20240808152545-0cdaa3abc0fa",
						OriginType: "intermediate",
						Pullspec:   "localhost/builder-base@sha256:dummy",
						StageAlias: "stage3",
					},
					{
						PackageURL: "pkg:golang/golang.org/x/sync@v0.8.0",
						OriginType: "intermediate",
						Pullspec:   "localhost/builder-base@sha256:dummy",
						StageAlias: "stage5",
					},
				},
			},
		},
		{
			Description:    "[Chained stages] Empty chained stages copying only builder base content",
			SkipTestReason: "[Priority: high] chained stages not yet supported",
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
											COPY go.mod /opt/content/go.mod
											COPY go2.mod /untracked/base/go.mod`,
					ContextDirectory: "../testdata/image_content",
				},
			},
			ExpectedResult: PackageMetadata{
				Packages: []PackageMetadataItem{
					{
						PackageURL: "pkg:golang/github.com/anchore/syft@v1.32.0",
						OriginType: "builder",
						Pullspec:   "localhost/builder-with-content@sha256:dummy",
						StageAlias: "alias",
					},
				},
			},
		},
		{
			Description:    "[Chained stages] Diamond dependency - two branches from same parent",
			SkipTestReason: "[Priority: high] chained stages not yet supported",
			TestImage: BuildDefinition{
				Tag: "test-diamond-dependency",
				ContainerfileContent: `FROM localhost/diamond-base:latest AS shared
										COPY go_uuid.mod /shared/go.mod
										COPY go_text.mod /untracked/shared/go.mod

										FROM shared AS left
										COPY go_exp.mod /left/go.mod
										COPY go_text.mod /untracked/left/go.mod

										FROM shared AS right
										COPY go_sync.mod /right/go.mod
										COPY go_text.mod /untracked/right/go.mod

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
											COPY go.mod /base/go.mod
											COPY go2.mod /untracked/base/go.mod`,
					ContextDirectory: "../testdata/image_content",
				},
			},
			ExpectedResult: PackageMetadata{
				Packages: []PackageMetadataItem{
					{
						PackageURL: "pkg:golang/github.com/anchore/syft@v1.32.0",
						OriginType: "builder",
						Pullspec:   "localhost/diamond-base@sha256:dummy",
						StageAlias: "right",
					},
					{
						PackageURL: "pkg:golang/github.com/google/uuid@v1.6.0",
						OriginType: "intermediate",
						Pullspec:   "localhost/diamond-base@sha256:dummy",
						StageAlias: "shared",
					},
					{
						PackageURL: "pkg:golang/golang.org/x/exp@v0.0.0-20240808152545-0cdaa3abc0fa",
						OriginType: "intermediate",
						Pullspec:   "localhost/diamond-base@sha256:dummy",
						StageAlias: "left",
					},
					{
						PackageURL: "pkg:golang/golang.org/x/sync@v0.8.0",
						OriginType: "intermediate",
						Pullspec:   "localhost/diamond-base@sha256:dummy",
						StageAlias: "right",
					},
				},
			},
		},
		{
			Description:    "[Chained stages / external content] Content traced through intermediate builder via COPY chain with external image",
			SkipTestReason: "[Priority: high] chained stages not yet supported, bug with external image in builder stage unresolved",
			TestImage: BuildDefinition{
				Tag: "test-chain-with-external",
				ContainerfileContent: `FROM localhost/base-img:latest AS builder
										COPY go_uuid.mod /content/app1/go.mod
										COPY go_sync.mod /untracked/builder/go.mod

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
											COPY go.mod /base/go.mod
											COPY go2.mod /untracked/base/go.mod`,
					ContextDirectory: "../testdata/image_content",
				},
				{
					Tag: "localhost/in-chain-ext:latest",
					ContainerfileContent: `FROM scratch
											COPY go_text.mod /ext/go.mod
											COPY go2.mod /untracked/ext/go.mod`,
					ContextDirectory: "../testdata/image_content",
				},
			},
			ExpectedResult: PackageMetadata{
				Packages: []PackageMetadataItem{
					{
						PackageURL: "pkg:golang/github.com/anchore/syft@v1.32.0",
						OriginType: "builder",
						Pullspec:   "localhost/base-img@sha256:dummy",
						StageAlias: "builder",
					},
					{
						PackageURL: "pkg:golang/github.com/google/uuid@v1.6.0",
						OriginType: "intermediate",
						Pullspec:   "localhost/base-img@sha256:dummy",
						StageAlias: "builder",
					},
					{
						PackageURL: "pkg:golang/golang.org/x/text@v0.18.0",
						OriginType: "external",
						Pullspec:   "localhost/in-chain-ext@sha256:dummy",
						StageAlias: "builder",
					},
				},
			},
		},
		{
			Description:    "[External content] External COPY in final stage",
			SkipTestReason: "[Priority: medium] origin_type 'external' not yet implemented - capo reports external content as 'builder'. Works otherwise.",
			TestImage: BuildDefinition{
				Tag: "test-external-copy-final",
				ContainerfileContent: `FROM localhost/builder-base:latest AS builder
										COPY go_uuid.mod /content/go.mod
										COPY go_sync.mod /untracked/builder/go.mod

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
											COPY go.mod /base/go.mod
											COPY go2.mod /untracked/base/go.mod`,
					ContextDirectory: "../testdata/image_content",
				},
				{
					Tag: "localhost/external:latest",
					ContainerfileContent: `FROM scratch
											COPY go_text.mod /ext/go.mod
											COPY go2.mod /untracked/ext/go.mod`,
					ContextDirectory: "../testdata/image_content",
				},
			},
			ExpectedResult: PackageMetadata{
				Packages: []PackageMetadataItem{
					{
						PackageURL: "pkg:golang/github.com/anchore/syft@v1.32.0",
						OriginType: "builder",
						Pullspec:   "localhost/builder-base@sha256:dummy",
						StageAlias: "builder",
					},
					{
						PackageURL: "pkg:golang/github.com/google/uuid@v1.6.0",
						OriginType: "intermediate",
						Pullspec:   "localhost/builder-base@sha256:dummy",
						StageAlias: "builder",
					},
					{
						PackageURL: "pkg:golang/golang.org/x/text@v0.18.0",
						OriginType: "external",
						Pullspec:   "localhost/external@sha256:dummy",
					},
				},
			},
		},
		{
			Description: "[External content] External COPY in builder stage - content traced through builder to final",
			LongDescription: `This test introduces OriginType "external" — a new type needed to distinguish
content from external images (COPY --from=<image> or RUN --mount from=<image>)
from builder base content and intermediate content.

This distinction is required because in SBOM we model intermediate content as
DESCENDANT_OF builder base image. External image placed in builder stage has no such relationship
to the builder base - it originates from a separate image. Without "external" type,
we cannot determine which builder base image a given intermediate image belongs to
when builder stage copies from external image and this content is copied to final image.`,
			SkipTestReason: "[Priority: high] bug: traceSource panic on nil stage from external COPY --from in builder",
			TestImage: BuildDefinition{
				Tag: "test-external-copy-in-builder",
				ContainerfileContent: `FROM localhost/builder-base:latest AS builder
										COPY --from=localhost/external:latest /ext /ext
										COPY go_uuid.mod /content/go.mod
										COPY go_sync.mod /untracked/builder/go.mod

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
											COPY go.mod /base/go.mod
											COPY go2.mod /untracked/base/go.mod`,
					ContextDirectory: "../testdata/image_content",
				},
				{
					Tag: "localhost/external:latest",
					ContainerfileContent: `FROM scratch
											COPY go_text.mod /ext/go.mod
											COPY go2.mod /untracked/ext/go.mod`,
					ContextDirectory: "../testdata/image_content",
				},
			},
			ExpectedResult: PackageMetadata{
				Packages: []PackageMetadataItem{
					{
						PackageURL: "pkg:golang/github.com/anchore/syft@v1.32.0",
						OriginType: "builder",
						Pullspec:   "localhost/builder-base@sha256:dummy",
						StageAlias: "builder",
					},
					{
						PackageURL: "pkg:golang/github.com/google/uuid@v1.6.0",
						OriginType: "intermediate",
						Pullspec:   "localhost/builder-base@sha256:dummy",
						StageAlias: "builder",
					},
					{
						PackageURL: "pkg:golang/golang.org/x/text@v0.18.0",
						OriginType: "external",
						Pullspec:   "localhost/external@sha256:dummy",
					},
				},
			},
		},
		{
			Description:    "[Pullspec normalization] Pullspec is missing registry and tag/digest",
			SkipTestReason: "[Priority: medium/low] pullspec normalization not supported - store.Lookup does exact match with registry and tag/digest",
			TestImage: BuildDefinition{
				Tag: "test-simple-pullspec",
				ContainerfileContent: `FROM image AS builder
										COPY go_uuid.mod /opt/app1/go.mod

										FROM scratch
										COPY --from=builder /opt /opt`,
				ContextDirectory: "../testdata/image_content",
			},
			BuilderImages: []BuildDefinition{
				{
					Tag: "image",
					ContainerfileContent: `FROM scratch
											COPY go.mod /opt/app2/go.mod`,
					ContextDirectory: "../testdata/image_content",
				},
			},
			ExpectedResult: PackageMetadata{
				Packages: []PackageMetadataItem{
					{
						PackageURL: "pkg:golang/github.com/anchore/syft@v1.32.0",
						OriginType: "builder",
						Pullspec:   "localhost/image@sha256:dummy",
						StageAlias: "builder",
					},
					{
						PackageURL: "pkg:golang/github.com/google/uuid@v1.6.0",
						OriginType: "intermediate",
						Pullspec:   "localhost/image@sha256:dummy",
						StageAlias: "builder",
					},
				},
			},
		},
		{
			Description:    "[Pullspec normalization] Pullspec missing registry and alias is identical to alias - FROM image AS image",
			SkipTestReason: "[Priority: low] resolved, when previous test passes",
			TestImage: BuildDefinition{
				Tag: "test-identical-pullspec-alias",
				ContainerfileContent: `FROM image AS image
										COPY go_uuid.mod /content/go.mod

										FROM scratch
										COPY --from=image /base /base
										COPY --from=image /content /content`,
				ContextDirectory: "../testdata/image_content",
			},
			BuilderImages: []BuildDefinition{
				{
					Tag: "image",
					ContainerfileContent: `FROM scratch
											COPY go.mod /base/go.mod`,
					ContextDirectory: "../testdata/image_content",
				},
			},
			ExpectedResult: PackageMetadata{
				Packages: []PackageMetadataItem{
					{
						PackageURL: "pkg:golang/github.com/anchore/syft@v1.32.0",
						OriginType: "builder",
						Pullspec:   "localhost/image@sha256:dummy",
						StageAlias: "image",
					},
					{
						PackageURL: "pkg:golang/github.com/google/uuid@v1.6.0",
						OriginType: "intermediate",
						Pullspec:   "localhost/image@sha256:dummy",
						StageAlias: "image",
					},
				},
			},
		},
		{
			Description: "[Numeric index COPY --from] Stages do not have aliases - references are using numeric indices instead of aliases",
			TestImage: BuildDefinition{
				Tag: "test-numeric-indices",
				ContainerfileContent: `FROM localhost/base1:latest
										COPY go_uuid.mod /opt/app0/go.mod
										COPY go_text.mod /untracked/s0/go.mod

										FROM localhost/base2:latest
										COPY go_exp.mod /opt/app1/go.mod
										COPY go_text.mod /untracked/s1/go.mod

										FROM scratch
										COPY --from=0 /opt/ /opt/
										COPY --from=1 /opt/ /opt/`,
				ContextDirectory: "../testdata/image_content",
			},
			BuilderImages: []BuildDefinition{
				{
					Tag: "localhost/base1:latest",
					ContainerfileContent: `FROM scratch
											COPY go.mod /opt/base1/go.mod
											COPY go2.mod /untracked/base1/go.mod`,
					ContextDirectory: "../testdata/image_content",
				},
				{
					Tag: "localhost/base2:latest",
					ContainerfileContent: `FROM scratch
											COPY go_sync.mod /opt/base2/go.mod
											COPY go2.mod /untracked/base2/go.mod`,
					ContextDirectory: "../testdata/image_content",
				},
			},
			ExpectedResult: PackageMetadata{
				Packages: []PackageMetadataItem{
					{
						PackageURL: "pkg:golang/github.com/anchore/syft@v1.32.0",
						OriginType: "builder",
						Pullspec:   "localhost/base1@sha256:dummy",
						StageAlias: "0",
					},
					{
						PackageURL: "pkg:golang/github.com/google/uuid@v1.6.0",
						OriginType: "intermediate",
						Pullspec:   "localhost/base1@sha256:dummy",
						StageAlias: "0",
					},
					{
						PackageURL: "pkg:golang/golang.org/x/sync@v0.8.0",
						OriginType: "builder",
						Pullspec:   "localhost/base2@sha256:dummy",
						StageAlias: "1",
					},
					{
						PackageURL: "pkg:golang/golang.org/x/exp@v0.0.0-20240808152545-0cdaa3abc0fa",
						OriginType: "intermediate",
						Pullspec:   "localhost/base2@sha256:dummy",
						StageAlias: "1",
					},
				},
			},
		},
		{
			Description:    "[Numeric index COPY --from] COPY --from with numeric index in final stage",
			SkipTestReason: "[Priority: medium/high] COPY --from=0 resolved as pullspec instead of stage index when stage has alias (fails with 'could not find resolved pullspec')",
			TestImage: BuildDefinition{
				Tag: "test-numeric-copy-from-final",
				ContainerfileContent: `FROM localhost/numfinal-base:latest AS builder
										COPY go_uuid.mod /content/go.mod
										COPY go_text.mod /untracked/builder/go.mod

										FROM scratch
										COPY --from=0 /content /content`,
				ContextDirectory: "../testdata/image_content",
			},
			BuilderImages: []BuildDefinition{
				{
					Tag: "localhost/numfinal-base:latest",
					ContainerfileContent: `FROM scratch
											COPY go.mod /base/go.mod
											COPY go2.mod /untracked/base/go.mod`,
					ContextDirectory: "../testdata/image_content",
				},
			},
			ExpectedResult: PackageMetadata{
				Packages: []PackageMetadataItem{
					{
						PackageURL: "pkg:golang/github.com/google/uuid@v1.6.0",
						OriginType: "intermediate",
						Pullspec:   "localhost/numfinal-base@sha256:dummy",
						StageAlias: "builder",
					},
				},
			},
		},
		{
			Description:    "[Numeric index COPY --from] COPY --from with numeric index in builder stage",
			SkipTestReason: "[Priority: high] COPY --from=0 in builder stage causes nil pointer panic in traceSource when stage has alias (same as in External COPY in builder stage... test)",
			TestImage: BuildDefinition{
				Tag: "test-numeric-copy-from-builder",
				ContainerfileContent: `FROM localhost/numbuilder-base1:latest AS builder1
										COPY go_uuid.mod /content/go.mod
										COPY go_text.mod /untracked/b1/go.mod

										FROM localhost/numbuilder-base2:latest AS builder2
										COPY --from=0 /content /forwarded
										COPY go_text.mod /untracked/b2/go.mod

										FROM scratch
										COPY --from=builder2 /forwarded /forwarded`,
				ContextDirectory: "../testdata/image_content",
			},
			BuilderImages: []BuildDefinition{
				{
					Tag: "localhost/numbuilder-base1:latest",
					ContainerfileContent: `FROM scratch
											COPY go.mod /base1/go.mod
											COPY go2.mod /untracked/base1/go.mod`,
					ContextDirectory: "../testdata/image_content",
				},
				{
					Tag: "localhost/numbuilder-base2:latest",
					ContainerfileContent: `FROM scratch
											COPY go_exp.mod /base2/go.mod
											COPY go2.mod /untracked/base2/go.mod`,
					ContextDirectory: "../testdata/image_content",
				},
			},
			ExpectedResult: PackageMetadata{
				Packages: []PackageMetadataItem{
					{
						PackageURL: "pkg:golang/github.com/google/uuid@v1.6.0",
						OriginType: "intermediate",
						Pullspec:   "localhost/numbuilder-base1@sha256:dummy",
						StageAlias: "builder1",
					},
				},
			},
		},
		{
			Description: "[Wildcard COPY] Builder base content",
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
											COPY go.mod /app1/go.mod
											COPY go_uuid.mod /app2/go.mod
											COPY go_exp.mod /other/go.mod`,
					ContextDirectory: "../testdata/image_content",
				},
			},
			ExpectedResult: PackageMetadata{
				Packages: []PackageMetadataItem{
					{
						PackageURL: "pkg:golang/github.com/anchore/syft@v1.32.0",
						OriginType: "builder",
						Pullspec:   "localhost/wildcard-base@sha256:dummy",
						StageAlias: "builder",
					},
					{
						PackageURL: "pkg:golang/github.com/google/uuid@v1.6.0",
						OriginType: "builder",
						Pullspec:   "localhost/wildcard-base@sha256:dummy",
						StageAlias: "builder",
					},
				},
			},
		},
		{
			Description:    "[Wildcard COPY] Intermediate content",
			SkipTestReason: "[Priority: high] includes() with wildcard /app* does not match intermediate paths like app1/go.mod",
			TestImage: BuildDefinition{
				Tag: "test-wildcard-copy-intermediate",
				ContainerfileContent: `FROM localhost/wildcard-inter-base:latest AS builder
										COPY go_uuid.mod /app1/go.mod
										COPY go_exp.mod /app2/go.mod
										COPY go_sync.mod /other/go.mod

										FROM scratch
										COPY --from=builder /app* /dest/`,
				ContextDirectory: "../testdata/image_content",
			},
			BuilderImages: []BuildDefinition{
				{
					Tag: "localhost/wildcard-inter-base:latest",
					ContainerfileContent: `FROM scratch
											COPY go.mod /base/go.mod`,
					ContextDirectory: "../testdata/image_content",
				},
			},
			ExpectedResult: PackageMetadata{
				Packages: []PackageMetadataItem{
					{
						PackageURL: "pkg:golang/github.com/google/uuid@v1.6.0",
						OriginType: "intermediate",
						Pullspec:   "localhost/wildcard-inter-base@sha256:dummy",
						StageAlias: "builder",
					},
					{
						PackageURL: "pkg:golang/golang.org/x/exp@v0.0.0-20240808152545-0cdaa3abc0fa",
						OriginType: "intermediate",
						Pullspec:   "localhost/wildcard-inter-base@sha256:dummy",
						StageAlias: "builder",
					},
				},
			},
		},
		{
			Description:    "[Wildcard COPY] Builder base and intermediate content",
			SkipTestReason: "[Priority: high] includes() with wildcard /app* does not match intermediate paths like app1/go.mod",
			TestImage: BuildDefinition{
				Tag: "test-wildcard-copy-both",
				ContainerfileContent: `FROM localhost/wildcard-both-base:latest AS builder
										COPY go_sync.mod /app3/go.mod
										COPY go_text.mod /other/go.mod

										FROM scratch
										COPY --from=builder /app* /dest/`,
				ContextDirectory: "../testdata/image_content",
			},
			BuilderImages: []BuildDefinition{
				{
					Tag: "localhost/wildcard-both-base:latest",
					ContainerfileContent: `FROM scratch
											COPY go.mod /app1/go.mod
											COPY go_uuid.mod /app2/go.mod
											COPY go_exp.mod /other/go.mod`,
					ContextDirectory: "../testdata/image_content",
				},
			},
			ExpectedResult: PackageMetadata{
				Packages: []PackageMetadataItem{
					{
						PackageURL: "pkg:golang/github.com/anchore/syft@v1.32.0",
						OriginType: "builder",
						Pullspec:   "localhost/wildcard-both-base@sha256:dummy",
						StageAlias: "builder",
					},
					{
						PackageURL: "pkg:golang/github.com/google/uuid@v1.6.0",
						OriginType: "builder",
						Pullspec:   "localhost/wildcard-both-base@sha256:dummy",
						StageAlias: "builder",
					},
					{
						PackageURL: "pkg:golang/golang.org/x/sync@v0.8.0",
						OriginType: "intermediate",
						Pullspec:   "localhost/wildcard-both-base@sha256:dummy",
						StageAlias: "builder",
					},
				},
			},
		},
		{
			Description:    "[FROM special] FROM scratch as builder base",
			SkipTestReason: "[Priority: high] scratch is not handled in resolvePullspecs",
			TestImage: BuildDefinition{
				Tag: "test-from-scratch-builder",
				ContainerfileContent: `FROM scratch AS builder
										COPY go_uuid.mod /content/go.mod

										FROM scratch
										COPY --from=builder /content /content`,
				ContextDirectory: "../testdata/image_content",
			},
			ExpectedResult: PackageMetadata{
				Packages: []PackageMetadataItem{
					{
						PackageURL: "pkg:golang/github.com/google/uuid@v1.6.0",
						OriginType: "intermediate",
						Pullspec:   "scratch@sha256:dummy",
						StageAlias: "builder",
					},
				},
			},
		},
		{
			Description:    "[FROM special] FROM oci:archive as builder base",
			SkipTestReason: "[Priority: high] oci:archive transport not handled in resolvePullspecs",
			TestImage: BuildDefinition{
				Tag: "test-from-oci-archive-builder",
				ContainerfileContent: `FROM oci:archive:/path/to/base.ociarchive AS builder
										COPY go_uuid.mod /content/go.mod

										FROM scratch
										COPY --from=builder /content /content`,
				ContextDirectory: "../testdata/image_content",
			},
			ExpectedResult: PackageMetadata{
				Packages: []PackageMetadataItem{
					{
						PackageURL: "pkg:golang/github.com/google/uuid@v1.6.0",
						OriginType: "intermediate",
						Pullspec:   "oci:archive:/path/to/base.ociarchive",
						StageAlias: "builder",
					},
				},
			},
		},
		{
			Description: "[RUN --mount] --mount from external image in builder stage",
			SkipTestReason: "[Priority: high] capo does not trace content through RUN --mount",
			TestImage: BuildDefinition{
				Tag: "test-mount-external-in-builder",
				ContainerfileContent: `FROM localhost/mount-ext-base:latest AS builder
										COPY go_exp.mod /opt/app3/go.mod
										RUN --mount=type=bind,from=localhost/mount-ext-source:latest,target=/mnt mkdir -p /opt/app2 && cp /mnt/go.mod /opt/app2/go.mod

										FROM scratch
										COPY --from=builder /opt /opt`,
				ContextDirectory: "../testdata/image_content",
			},
			BuilderImages: []BuildDefinition{
				{
					Tag: "localhost/mount-ext-base:latest",
					ContainerfileContent: `FROM docker.io/library/alpine:latest
											COPY go.mod /opt/app1/go.mod`,
					ContextDirectory: "../testdata/image_content",
				},
				{
					Tag: "localhost/mount-ext-source:latest",
					ContainerfileContent: `FROM scratch
											COPY go_uuid.mod /go.mod`,
					ContextDirectory: "../testdata/image_content",
				},
			},
			ExpectedResult: PackageMetadata{
				Packages: []PackageMetadataItem{
					{
						PackageURL: "pkg:golang/github.com/anchore/syft@v1.32.0",
						OriginType: "builder",
						Pullspec:   "localhost/mount-ext-base@sha256:dummy",
						StageAlias: "builder",
					},
					{
						PackageURL: "pkg:golang/github.com/google/uuid@v1.6.0",
						OriginType: "external",
						Pullspec:   "localhost/mount-ext-source@sha256:dummy",
						StageAlias: "builder",
					},
					{
						PackageURL: "pkg:golang/golang.org/x/exp@v0.0.0-20240808152545-0cdaa3abc0fa",
						OriginType: "intermediate",
						Pullspec:   "localhost/mount-ext-base@sha256:dummy",
						StageAlias: "builder",
					},
				},
			},
		},
		{
			Description:    "[RUN --mount] --mount from builder stage in another builder stage",
			SkipTestReason: "[Priority: high] capo does not trace content through RUN --mount",
			TestImage: BuildDefinition{
				Tag: "test-mount-builder-stage",
				ContainerfileContent: `FROM localhost/mount-stage-base:latest AS provider
										COPY go_uuid.mod /provided/go.mod

										FROM localhost/mount-stage-base2:latest AS consumer
										COPY go_exp.mod /opt/app3/go.mod
										RUN --mount=type=bind,from=provider,target=/mnt mkdir -p /opt/app2 && cp /mnt/provided/go.mod /opt/app2/go.mod

										FROM scratch
										COPY --from=consumer /opt /opt`,
				ContextDirectory: "../testdata/image_content",
			},
			BuilderImages: []BuildDefinition{
				{
					Tag: "localhost/mount-stage-base:latest",
					ContainerfileContent: `FROM scratch
											COPY go.mod /opt/app0/go.mod`,
					ContextDirectory: "../testdata/image_content",
				},
				{
					Tag: "localhost/mount-stage-base2:latest",
					ContainerfileContent: `FROM docker.io/library/alpine:latest
											COPY go_sync.mod /opt/app1/go.mod`,
					ContextDirectory: "../testdata/image_content",
				},
			},
			ExpectedResult: PackageMetadata{
				Packages: []PackageMetadataItem{
					{
						PackageURL: "pkg:golang/golang.org/x/sync@v0.8.0",
						OriginType: "builder",
						Pullspec:   "localhost/mount-stage-base2@sha256:dummy",
						StageAlias: "consumer",
					},
					{
						PackageURL: "pkg:golang/github.com/google/uuid@v1.6.0",
						OriginType: "intermediate",
						Pullspec:   "localhost/mount-stage-base@sha256:dummy",
						StageAlias: "provider",
					},
					{
						PackageURL: "pkg:golang/golang.org/x/exp@v0.0.0-20240808152545-0cdaa3abc0fa",
						OriginType: "intermediate",
						Pullspec:   "localhost/mount-stage-base2@sha256:dummy",
						StageAlias: "consumer",
					},
				},
			},
		},
		{
			Description:    "[RUN --mount] --mount from external image in final stage",
			SkipTestReason: "[Priority: high] capo does not trace content through RUN --mount",
			TestImage: BuildDefinition{
				Tag: "test-mount-external-final",
				ContainerfileContent: `FROM localhost/mount-ext-final-base:latest AS builder
										COPY go_exp.mod /opt/app3/go.mod

										FROM docker.io/library/alpine:latest
										RUN --mount=type=bind,from=localhost/mount-ext-final-source:latest,target=/mnt mkdir -p /opt/app2 && cp /mnt/go.mod /opt/app2/go.mod
										COPY --from=builder /opt /opt`,
				ContextDirectory: "../testdata/image_content",
			},
			BuilderImages: []BuildDefinition{
				{
					Tag: "localhost/mount-ext-final-base:latest",
					ContainerfileContent: `FROM scratch
											COPY go.mod /opt/app1/go.mod`,
					ContextDirectory: "../testdata/image_content",
				},
				{
					Tag: "localhost/mount-ext-final-source:latest",
					ContainerfileContent: `FROM scratch
											COPY go_uuid.mod /go.mod`,
					ContextDirectory: "../testdata/image_content",
				},
			},
			ExpectedResult: PackageMetadata{
				Packages: []PackageMetadataItem{
					{
						PackageURL: "pkg:golang/github.com/anchore/syft@v1.32.0",
						OriginType: "builder",
						Pullspec:   "localhost/mount-ext-final-base@sha256:dummy",
						StageAlias: "builder",
					},
					{
						PackageURL: "pkg:golang/golang.org/x/exp@v0.0.0-20240808152545-0cdaa3abc0fa",
						OriginType: "intermediate",
						Pullspec:   "localhost/mount-ext-final-base@sha256:dummy",
						StageAlias: "builder",
					},
					{
						PackageURL: "pkg:golang/github.com/google/uuid@v1.6.0",
						OriginType: "external",
						Pullspec:   "localhost/mount-ext-final-source@sha256:dummy",
					},
				},
			},
		},
		{
			Description:    "[RUN --mount] --mount from builder stage in final stage",
			SkipTestReason: "[Priority: high] capo does not trace content through RUN --mount",
			TestImage: BuildDefinition{
				Tag: "test-mount-builder-final",
				ContainerfileContent: `FROM localhost/mount-builder-final-base:latest AS builder
										COPY go_exp.mod /opt/app2/go.mod

										FROM docker.io/library/alpine:latest
										RUN --mount=type=bind,from=builder,target=/mnt mkdir -p /opt/app1 && cp /mnt/opt/app1/go.mod /opt/app1/go.mod
										RUN --mount=type=bind,from=builder,target=/mnt mkdir -p /opt/app2 && cp /mnt/opt/app2/go.mod /opt/app2/go.mod`,
				ContextDirectory: "../testdata/image_content",
			},
			BuilderImages: []BuildDefinition{
				{
					Tag: "localhost/mount-builder-final-base:latest",
					ContainerfileContent: `FROM scratch
											COPY go.mod /opt/app1/go.mod`,
					ContextDirectory: "../testdata/image_content",
				},
			},
			ExpectedResult: PackageMetadata{
				Packages: []PackageMetadataItem{
					{
						PackageURL: "pkg:golang/github.com/anchore/syft@v1.32.0",
						OriginType: "builder",
						Pullspec:   "localhost/mount-builder-final-base@sha256:dummy",
						StageAlias: "builder",
					},
					{
						PackageURL: "pkg:golang/golang.org/x/exp@v0.0.0-20240808152545-0cdaa3abc0fa",
						OriginType: "intermediate",
						Pullspec:   "localhost/mount-builder-final-base@sha256:dummy",
						StageAlias: "builder",
					},
				},
			},
		},
		{
			Description: "[WORKDIR] WORKDIR set in intermediate image",
			TestImage: BuildDefinition{
				Tag: "test-workdir-relative-dest",
				ContainerfileContent: `FROM localhost/workdir-base:latest AS builder
									   WORKDIR /opt/app2
									   COPY go_uuid.mod go.mod

									   FROM scratch
									   COPY --from=builder /opt /opt`,
				ContextDirectory: "../testdata/image_content",
			},
			BuilderImages: []BuildDefinition{
				{
					Tag: "localhost/workdir-base:latest",
					ContainerfileContent: `FROM scratch
										   COPY go.mod /opt/app1/go.mod`,
					ContextDirectory: "../testdata/image_content",
				},
			},
			ExpectedResult: PackageMetadata{
				Packages: []PackageMetadataItem{
					{
						PackageURL: "pkg:golang/github.com/anchore/syft@v1.32.0",
						OriginType: "builder",
						Pullspec:   "localhost/workdir-base@sha256:dummy",
						StageAlias: "builder",
					},
					{
						PackageURL: "pkg:golang/github.com/google/uuid@v1.6.0",
						OriginType: "intermediate",
						Pullspec:   "localhost/workdir-base@sha256:dummy",
						StageAlias: "builder",
					},
				},
			},
		},
		{
			Description: "[WORKDIR] WORKDIR inherited from builder base image",
			TestImage: BuildDefinition{
				Tag: "test-workdir-inherited",
				ContainerfileContent: `FROM localhost/workdir-inherited-base:latest AS builder
									   COPY go_uuid.mod app2/go.mod

									   FROM scratch
									   COPY --from=builder /opt /opt`,
				ContextDirectory: "../testdata/image_content",
			},
			BuilderImages: []BuildDefinition{
				{
					Tag: "localhost/workdir-inherited-base:latest",
					ContainerfileContent: `FROM scratch
										   WORKDIR /opt
										   COPY go.mod app1/go.mod`,
					ContextDirectory: "../testdata/image_content",
				},
			},
			ExpectedResult: PackageMetadata{
				Packages: []PackageMetadataItem{
					{
						PackageURL: "pkg:golang/github.com/anchore/syft@v1.32.0",
						OriginType: "builder",
						Pullspec:   "localhost/workdir-inherited-base@sha256:dummy",
						StageAlias: "builder",
					},
					{
						PackageURL: "pkg:golang/github.com/google/uuid@v1.6.0",
						OriginType: "intermediate",
						Pullspec:   "localhost/workdir-inherited-base@sha256:dummy",
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

	buildahBinary := getBuildahBinary(t)

	type skippedTest struct {
		description string
		reason      string
	}
	var passed, failed []string
	var skipped []skippedTest
	for _, testCase := range testCases {
		if testCase.SkipTestReason != "" {
			skipped = append(skipped, skippedTest{testCase.Description, testCase.SkipTestReason})
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
	for _, s := range skipped {
		t.Logf("  SKIP: %s | %s", s.description, s.reason)
	}
	for _, name := range failed {
		t.Logf("  FAIL: %s", name)
	}
}

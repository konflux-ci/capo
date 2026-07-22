//go:build unit

package probe

import (
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/opencontainers/go-digest"

	"github.com/konflux-ci/capo/internal/testutils"
	"github.com/konflux-ci/capo/pkg/storageclient"
)

func TestProbe(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		containerfile string
		opts          ProbeOpts
		digests       map[string]digest.Digest
		expected      BuildMetadata
	}{
		"simple": {
			containerfile: `FROM quay.io/rhel:9 as builder
							WORKDIR /app
							COPY . .
							FROM quay.io/fedora:42
							COPY --from=builder /app /app`,
			opts: ProbeOpts{
				Tag:    "quay.io/image:latest",
				Target: "",
				Args:   make(map[string]string),
				SkipUnusedStages: true,
			},
			digests: map[string]digest.Digest{
				"quay.io/rhel:9":       "rheldigest",
				"quay.io/fedora:42":    "fedoradigest",
				"quay.io/image:latest": "imagedigest",
			},
			expected: BuildMetadata{
				Image: Image{
					Pullspec: "quay.io/image:latest",
					Digest:   "imagedigest",
				},
				BaseImages: []Image{
					{
						Pullspec: "quay.io/rhel:9",
						Digest:   "rheldigest",
					},
					{
						Pullspec: "quay.io/fedora:42",
						Digest:   "fedoradigest",
					},
				},
				ExtraImages: []Image{},
			},
		},
		"extra image from COPY --from": {
			containerfile: `FROM quay.io/rhel:9
							COPY --from=quay.io/tools:1 /bin/tool /usr/bin/tool`,
			opts: ProbeOpts{
				Tag:    "quay.io/image:latest",
				Target: "",
				Args:   make(map[string]string),
				SkipUnusedStages: true,
			},
			digests: map[string]digest.Digest{
				"quay.io/rhel:9":       "rheldigest",
				"quay.io/tools:1":      "toolsdigest",
				"quay.io/image:latest": "imagedigest",
			},
			expected: BuildMetadata{
				Image: Image{
					Pullspec: "quay.io/image:latest",
					Digest:   "imagedigest",
				},
				BaseImages: []Image{
					{Pullspec: "quay.io/rhel:9", Digest: "rheldigest"},
				},
				ExtraImages: []Image{
					{Pullspec: "quay.io/tools:1", Digest: "toolsdigest"},
				},
			},
		},
		"COPY --from builder stage is not an extra image": {
			containerfile: `FROM quay.io/rhel:9 as builder
							WORKDIR /app
							COPY . .
							FROM quay.io/fedora:42
							COPY --from=builder /app /app`,
			opts: ProbeOpts{
				Tag:    "quay.io/image:latest",
				Target: "",
				Args:   make(map[string]string),
				SkipUnusedStages: true,
			},
			digests: map[string]digest.Digest{
				"quay.io/rhel:9":       "rheldigest",
				"quay.io/fedora:42":    "fedoradigest",
				"quay.io/image:latest": "imagedigest",
			},
			expected: BuildMetadata{
				Image: Image{
					Pullspec: "quay.io/image:latest",
					Digest:   "imagedigest",
				},
				BaseImages: []Image{
					{Pullspec: "quay.io/rhel:9", Digest: "rheldigest"},
					{Pullspec: "quay.io/fedora:42", Digest: "fedoradigest"},
				},
				ExtraImages: []Image{},
			},
		},
		"duplicate base and extra images are deduplicated": {
			containerfile: `FROM quay.io/rhel:9 as builder
							COPY --from=quay.io/tools:1 /bin/tool1 /usr/bin/tool1
							COPY --from=quay.io/tools:1 /bin/tool2 /usr/bin/tool2
							COPY --from=quay.io/utils:2 /bin/util /usr/bin/util
							FROM quay.io/rhel:9
							COPY --from=quay.io/tools:1 /bin/tool /usr/bin/tool
							COPY --from=quay.io/utils:2 /bin/util /usr/bin/util`,
			opts: ProbeOpts{
				Tag:    "quay.io/image:latest",
				Target: "",
				Args:   make(map[string]string),
				SkipUnusedStages: true,
			},
			digests: map[string]digest.Digest{
				"quay.io/rhel:9":       "rheldigest",
				"quay.io/tools:1":      "toolsdigest",
				"quay.io/utils:2":      "utilsdigest",
				"quay.io/image:latest": "imagedigest",
			},
			expected: BuildMetadata{
				Image: Image{
					Pullspec: "quay.io/image:latest",
					Digest:   "imagedigest",
				},
				BaseImages: []Image{
					{Pullspec: "quay.io/rhel:9", Digest: "rheldigest"},
				},
				ExtraImages: []Image{
					{Pullspec: "quay.io/tools:1", Digest: "toolsdigest"},
					{Pullspec: "quay.io/utils:2", Digest: "utilsdigest"},
				},
			},
		},
		"scratch base is excluded from base images": {
			containerfile: `FROM quay.io/rhel:9 as builder
							FROM scratch
							COPY --from=builder /app /app`,
			opts: ProbeOpts{
				Tag:    "quay.io/image:latest",
				Target: "",
				Args:   make(map[string]string),
				SkipUnusedStages: true,
			},
			digests: map[string]digest.Digest{
				"quay.io/rhel:9":       "rheldigest",
				"quay.io/image:latest": "imagedigest",
			},
			expected: BuildMetadata{
				Image: Image{
					Pullspec: "quay.io/image:latest",
					Digest:   "imagedigest",
				},
				BaseImages: []Image{
					{Pullspec: "quay.io/rhel:9", Digest: "rheldigest"},
				},
				ExtraImages: []Image{},
			},
		},
		"oci-archive base is excluded from base images": {
			containerfile: `FROM quay.io/rhel:9 as builder
							FROM oci-archive:/path/to/archive
							COPY --from=builder /app /app`,
			opts: ProbeOpts{
				Tag:    "quay.io/image:latest",
				Target: "",
				Args:   make(map[string]string),
				SkipUnusedStages: true,
			},
			digests: map[string]digest.Digest{
				"quay.io/rhel:9":       "rheldigest",
				"quay.io/image:latest": "imagedigest",
			},
			expected: BuildMetadata{
				Image: Image{
					Pullspec: "quay.io/image:latest",
					Digest:   "imagedigest",
				},
				BaseImages: []Image{
					{Pullspec: "quay.io/rhel:9", Digest: "rheldigest"},
				},
				ExtraImages: []Image{},
			},
		},
		"extra image from RUN --mount=from": {
			containerfile: `FROM quay.io/rhel:9
							RUN --mount=type=bind,from=quay.io/tools:1,src=/bin/tool,dst=/tmp/tool /tmp/tool --version`,
			opts: ProbeOpts{
				Tag:    "quay.io/image:latest",
				Target: "",
				Args:   make(map[string]string),
				SkipUnusedStages: true,
			},
			digests: map[string]digest.Digest{
				"quay.io/rhel:9":       "rheldigest",
				"quay.io/tools:1":      "toolsdigest",
				"quay.io/image:latest": "imagedigest",
			},
			expected: BuildMetadata{
				Image: Image{
					Pullspec: "quay.io/image:latest",
					Digest:   "imagedigest",
				},
				BaseImages: []Image{
					{Pullspec: "quay.io/rhel:9", Digest: "rheldigest"},
				},
				ExtraImages: []Image{
					{Pullspec: "quay.io/tools:1", Digest: "toolsdigest"},
				},
			},
		},
		"RUN --mount=from builder stage is not an extra image": {
			containerfile: `FROM quay.io/rhel:9 AS builder
							FROM quay.io/fedora:42
							RUN --mount=type=bind,from=builder,src=/app,dst=/app ls /app`,
			opts: ProbeOpts{
				Tag:    "quay.io/image:latest",
				Target: "",
				Args:   make(map[string]string),
				SkipUnusedStages: true,
			},
			digests: map[string]digest.Digest{
				"quay.io/rhel:9":       "rheldigest",
				"quay.io/fedora:42":    "fedoradigest",
				"quay.io/image:latest": "imagedigest",
			},
			expected: BuildMetadata{
				Image: Image{
					Pullspec: "quay.io/image:latest",
					Digest:   "imagedigest",
				},
				BaseImages: []Image{
					{Pullspec: "quay.io/rhel:9", Digest: "rheldigest"},
					{Pullspec: "quay.io/fedora:42", Digest: "fedoradigest"},
				},
				ExtraImages: []Image{},
			},
		},
		"COPY --from numeric stage index is builder image": {
			containerfile: `FROM quay.io/rhel:9
							WORKDIR /app
							COPY . .
							FROM quay.io/fedora:42
							COPY --from=0 /app /app`,
			opts: ProbeOpts{
				Tag:    "quay.io/image:latest",
				Target: "",
				Args:   make(map[string]string),
				SkipUnusedStages: true,
			},
			digests: map[string]digest.Digest{
				"quay.io/rhel:9":       "rheldigest",
				"quay.io/fedora:42":    "fedoradigest",
				"quay.io/image:latest": "imagedigest",
			},
			expected: BuildMetadata{
				Image: Image{
					Pullspec: "quay.io/image:latest",
					Digest:   "imagedigest",
				},
				BaseImages: []Image{
					{Pullspec: "quay.io/rhel:9", Digest: "rheldigest"},
					{Pullspec: "quay.io/fedora:42", Digest: "fedoradigest"},
				},
				ExtraImages: []Image{},
			},
		},
		// buildprobe accepts duplicate aliases to match konflux-build-cli, where
		// findMatchingStages returns all stages with the same name (no error):
		// https://github.com/konflux-ci/konflux-build-cli/blob/1d6a8e2/pkg/commands/build.go#L2298
		"duplicate stage names reports both base images": {
			containerfile: `FROM quay.io/rhel:9 AS builder
							COPY . .
							FROM quay.io/fedora:42 AS builder
							COPY . .
							FROM scratch
							COPY --from=builder /app /app`,
			opts: ProbeOpts{
				Tag:    "quay.io/image:latest",
				Target: "",
				Args:   make(map[string]string),
				SkipUnusedStages: true,
			},
			digests: map[string]digest.Digest{
				"quay.io/rhel:9":       "rheldigest",
				"quay.io/fedora:42":    "fedoradigest",
				"quay.io/image:latest": "imagedigest",
			},
			expected: BuildMetadata{
				Image: Image{
					Pullspec: "quay.io/image:latest",
					Digest:   "imagedigest",
				},
				BaseImages: []Image{
					{Pullspec: "quay.io/rhel:9", Digest: "rheldigest"},
					{Pullspec: "quay.io/fedora:42", Digest: "fedoradigest"},
				},
				ExtraImages: []Image{},
			},
		},
		// BFS must follow BaseRef (alias) to traverse chained stages,
		// otherwise the parent stage is unreachable and its extra images are lost
		"chained stage parent is reachable through chain": {
			containerfile: `FROM quay.io/rhel:9 AS builder
							COPY --from=quay.io/tools:1 /bin/tool /usr/bin/tool
							FROM builder AS child
							FROM scratch
							COPY --from=child /app /app`,
			opts: ProbeOpts{
				Tag:    "quay.io/image:latest",
				Target: "",
				Args:   make(map[string]string),
				SkipUnusedStages: true,
			},
			digests: map[string]digest.Digest{
				"quay.io/rhel:9":       "rheldigest",
				"quay.io/tools:1":      "toolsdigest",
				"quay.io/image:latest": "imagedigest",
			},
			expected: BuildMetadata{
				Image: Image{
					Pullspec: "quay.io/image:latest",
					Digest:   "imagedigest",
				},
				BaseImages: []Image{
					{Pullspec: "quay.io/rhel:9", Digest: "rheldigest"},
				},
				ExtraImages: []Image{
					{Pullspec: "quay.io/tools:1", Digest: "toolsdigest"},
				},
			},
		},
		"unreachable stage is excluded when target is set": {
			containerfile: `FROM quay.io/alpine:3 AS unreachable
							RUN echo hello
							FROM quay.io/rhel:9 AS builder
							COPY . .
							FROM quay.io/fedora:42
							COPY --from=builder /app /app`,
			opts: ProbeOpts{
				Tag:    "quay.io/image:latest",
				Target: "",
				Args:   make(map[string]string),
				SkipUnusedStages: true,
			},
			digests: map[string]digest.Digest{
				"quay.io/alpine:3":     "alpinedigest",
				"quay.io/rhel:9":       "rheldigest",
				"quay.io/fedora:42":    "fedoradigest",
				"quay.io/image:latest": "imagedigest",
			},
			expected: BuildMetadata{
				Image: Image{
					Pullspec: "quay.io/image:latest",
					Digest:   "imagedigest",
				},
				BaseImages: []Image{
					{Pullspec: "quay.io/rhel:9", Digest: "rheldigest"},
					{Pullspec: "quay.io/fedora:42", Digest: "fedoradigest"},
				},
				ExtraImages: []Image{},
			},
		},
		"duplicate stage names with target matches first stage": {
			containerfile: `FROM quay.io/rhel:9 AS builder
							COPY . .
							FROM quay.io/fedora:42 AS builder
							COPY . .
							FROM scratch
							COPY --from=builder /app /app`,
			opts: ProbeOpts{
				Tag:    "quay.io/image:latest",
				Target: "builder",
				Args:   make(map[string]string),
				SkipUnusedStages: true,
			},
			digests: map[string]digest.Digest{
				"quay.io/rhel:9":       "rheldigest",
				"quay.io/image:latest": "imagedigest",
			},
			expected: BuildMetadata{
				Image: Image{
					Pullspec: "quay.io/image:latest",
					Digest:   "imagedigest",
				},
				BaseImages: []Image{
					{Pullspec: "quay.io/rhel:9", Digest: "rheldigest"},
				},
				ExtraImages: []Image{},
			},
		},
		"skip unused stages = false": {
			containerfile: `FROM quay.io/rhel:9 AS rhelbuilder
							COPY . .
							FROM quay.io/fedora:42 AS fedorabuilder
							COPY . .
							FROM scratch`,
			opts: ProbeOpts{
				Tag:    "quay.io/image:latest",
				Target: "",
				Args:   make(map[string]string),
				SkipUnusedStages: false,
			},
			digests: map[string]digest.Digest{
				"quay.io/rhel:9":       "rheldigest",
				"quay.io/fedora:42":       "feddigest",
				"quay.io/image:latest": "imagedigest",
			},
			expected: BuildMetadata{
				Image: Image{
					Pullspec: "quay.io/image:latest",
					Digest:   "imagedigest",
				},
				BaseImages: []Image{
					{Pullspec: "quay.io/rhel:9", Digest: "rheldigest"},
					{Pullspec: "quay.io/fedora:42", Digest: "feddigest"},
				},
				ExtraImages: []Image{},
			},
		},
		"skip unused stages = true": {
			containerfile: `FROM quay.io/rhel:9 AS rhelbuilder
							COPY . .
							FROM quay.io/fedora:42 AS fedorabuilder
							COPY . .
							FROM scratch`,
			opts: ProbeOpts{
				Tag:    "quay.io/image:latest",
				Target: "",
				Args:   make(map[string]string),
				SkipUnusedStages: true,
			},
			digests: map[string]digest.Digest{
				"quay.io/image:latest": "imagedigest",
			},
			expected: BuildMetadata{
				Image: Image{
					Pullspec: "quay.io/image:latest",
					Digest:   "imagedigest",
				},
				BaseImages: []Image{},
				ExtraImages: []Image{},
			},
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			client := testutils.NewTStorageClient(
				test.digests, make(map[string]storageclient.OCIImageConfig),
			)

			test.opts.Containerfile = strings.NewReader(test.containerfile)

			actual, err := Probe(test.opts, client)
			if err != nil {
				t.Fatalf("Probe returned unexpected error: %v", err)
			}

			if diff := cmp.Diff(test.expected, actual); diff != "" {
				t.Errorf("Probe() result mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

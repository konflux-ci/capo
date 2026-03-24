//go:build unit

package probe

import (
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

type TestStore struct {
	digests map[string]string
}

func (store TestStore) ResolveDigest(pullspec string) (string, error) {
	return store.digests[pullspec], nil
}

func TestProbe(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		containerfile string
		opts          ProbeOpts
		digests       map[string]string
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
			},
			digests: map[string]string{
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
			},
			digests: map[string]string{
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
			},
			digests: map[string]string{
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
			},
			digests: map[string]string{
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
			},
			digests: map[string]string{
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
		"oci:archive base is excluded from base images": {
			containerfile: `FROM quay.io/rhel:9 as builder
							FROM oci:archive:/path/to/archive
							COPY --from=builder /app /app`,
			opts: ProbeOpts{
				Tag:    "quay.io/image:latest",
				Target: "",
				Args:   make(map[string]string),
			},
			digests: map[string]string{
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
			},
			digests: map[string]string{
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
			},
			digests: map[string]string{
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
			},
			digests: map[string]string{
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
		// The following test exists to match inconsistent behaviour in buildah:
		// https://github.com/containers/buildah/issues/6731
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
			},
			digests: map[string]string{
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
			},
			digests: map[string]string{
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
			},
			digests: map[string]string{
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
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			repo := TestStore{
				digests: test.digests,
			}

			test.opts.Containerfile = strings.NewReader(test.containerfile)

			actual, err := Probe(test.opts, repo)
			if err != nil {
				t.Fatalf("Probe returned unexpected error: %v", err)
			}

			if diff := cmp.Diff(test.expected, actual); diff != "" {
				t.Errorf("Probe() result mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

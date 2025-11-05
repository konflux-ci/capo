package capo

import (
	"slices"
	"strings"
	"testing"
)

func TestParseContainerfile(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		containerfile   string
		buildOptions    BuildOptions
		expectedSources map[string][]string
	}{
		"only external copy in final": {
			containerfile: `FROM scratch
							COPY --from=docker.io/library/fedora:latest /usr/bin/oras /usr/bin/oras`,
			expectedSources: map[string][]string{
				"docker.io/library/fedora:latest": {"/usr/bin/oras"},
			},
		},
		"arg evaluation": {
			containerfile: `ARG FEDORA_REPO="docker.io/library/fedora"
							ARG ALPINE_IMAGE="docker.io/library/alpine:latest"
							FROM ${ALPINE_IMAGE} as builder
							FROM scratch
							COPY --from=${FEDORA_REPO}:${FEDORA_TAG} /usr/bin/oras /usr/bin/oras
							COPY --from=builder /usr/bin/binary /usr/bin/binary`,
			buildOptions: BuildOptions{
				Args: map[string]string{
					"FEDORA_TAG": "latest",
				},
			},
			expectedSources: map[string][]string{
				"docker.io/library/fedora:latest": {"/usr/bin/oras"},
				"docker.io/library/alpine:latest": {"/usr/bin/binary"},
			},
		},
		"build target": {
			containerfile: `FROM docker.io/library/fedora:latest AS builder
							COPY --from=docker.io/library/alpine:latest /usr/bin/binary /usr/bin/binary
							FROM scratch
							COPY --from=docker.io/library/fedora:latest /usr/bin/oras /usr/bin/oras`,
			buildOptions: BuildOptions{
				Target: "builder",
			},
			expectedSources: map[string][]string{
				"docker.io/library/alpine:latest": {"/usr/bin/binary"},
			},
		},
		"copies in final stage only": {
			containerfile: `FROM docker.io/library/fedora:latest AS builder1
							FROM docker.io/alpine/helm:latest AS builder2
							FROM scratch
							COPY --from=builder1 /usr/bin/oras /usr/bin/oras
							COPY --from=builder2 /usr/bin/helm /usr/bin/helm`,
			expectedSources: map[string][]string{
				"docker.io/library/fedora:latest": {"/usr/bin/oras"},
				"docker.io/alpine/helm:latest":    {"/usr/bin/helm"},
			},
		},
		"recursive multi-stage file copy": {
			containerfile: `FROM docker.io/library/fedora:latest AS builder1
							FROM docker.io/alpine/helm:latest AS builder2
							COPY --from=builder1 /usr/bin/oras /usr/bin/oras
							FROM scratch
							COPY --from=builder2 /usr/bin/oras /usr/bin/oras`,
			expectedSources: map[string][]string{
				"docker.io/library/fedora:latest": {"/usr/bin/oras"},
			},
		},
		"recursive multi-stage file copy - mixed sources": {
			containerfile: `FROM docker.io/library/fedora:latest AS builder1
							FROM docker.io/alpine/helm:latest AS builder2
							COPY --from=builder1 /usr/bin/oras /usr/bin/oras
							FROM scratch
							COPY --from=builder2 /usr/bin/oras /usr/bin/helm /app/`,
			expectedSources: map[string][]string{
				"docker.io/library/fedora:latest": {"/usr/bin/oras"},
				"docker.io/alpine/helm:latest":    {"/usr/bin/helm"},
			},
		},
		"multi-stage directory copy": {
			containerfile: `FROM docker.io/library/fedora:latest AS builder1
							FROM docker.io/alpine/helm:latest AS builder2
							COPY --from=builder1 /usr/bin/oras /app/oras
							COPY --from=builder1 /bin/* /app/
							FROM scratch
							COPY --from=builder2 /app/ /app/`,
			expectedSources: map[string][]string{
				"docker.io/library/fedora:latest": {"/usr/bin/oras", "/bin/*"},
				"docker.io/alpine/helm:latest":    {"/app/"},
			},
		},
		"ignore non-copied content": {
			containerfile: `FROM docker.io/library/fedora:latest AS builder1
							FROM docker.io/alpine/helm:latest AS builder2
							COPY --from=builder1 /usr/bin/wget /usr/bin/wget
							FROM scratch
							COPY --from=builder2 /app/ /app/`,
			expectedSources: map[string][]string{
				"docker.io/alpine/helm:latest": {"/app/"},
			},
		},
		"complex multi-stage with multiple final copies": {
			containerfile: `FROM docker.io/library/fedora:latest AS builder1
							FROM docker.io/alpine/helm:latest AS builder2
							COPY --from=builder1 /usr/bin/kubectl /tools/kubectl
							FROM scratch
							COPY --from=builder1 /lib/libc.so /lib/libc.so
							COPY --from=builder2 /tools/ /usr/bin/
							COPY --from=builder2 /usr/bin/helm /usr/bin/helm`,
			expectedSources: map[string][]string{
				"docker.io/library/fedora:latest": {"/lib/libc.so", "/usr/bin/kubectl"},
				"docker.io/alpine/helm:latest":    {"/tools/", "/usr/bin/helm"},
			},
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			reader := strings.NewReader(test.containerfile)
			stages, err := ParseContainerfile(reader, test.buildOptions)
			if err != nil {
				t.Fatalf("ParseContainerfile failed: %v", err)
			}

			// Create a map from pullspec to sources for easier comparison
			actualSources := make(map[string][]string)
			for _, stage := range stages {
				pullspec := stage.Pullspec()
				sources := stage.Sources()
				if len(sources) > 0 {
					actualSources[pullspec] = sources
				}
			}

			// Verify expected pullspecs and sources
			for expectedPullspec, expectedSrcs := range test.expectedSources {
				actualSrcs, exists := actualSources[expectedPullspec]
				if !exists {
					t.Errorf("Expected pullspec %s not found in actual sources", expectedPullspec)
					continue
				}

				if !slices.Equal(actualSrcs, expectedSrcs) {
					t.Errorf("Unexpected sources for pullspec %s. Have %+v, expected %+v",
						expectedPullspec, actualSrcs, expectedSrcs)
				}
			}

			// Verify no unexpected pullspecs
			for actualPullspec := range actualSources {
				if _, exists := test.expectedSources[actualPullspec]; !exists {
					t.Errorf("Unexpected pullspec %s found in actual sources with sources %+v",
						actualPullspec, actualSources[actualPullspec])
				}
			}
		})
	}
}

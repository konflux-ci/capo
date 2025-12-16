//go:build unit

package containerfile

import (
	"errors"
	"fmt"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestParseBuiltinArgs(t *testing.T) {
	t.Parallel()
	containerfile := `FROM docker.io/library/alpine:${TARGETARCH} as builder
						FROM scratch
						COPY --from=builder /usr/bin/binary /usr/bin/binary`

	expectedPullspec := fmt.Sprintf("docker.io/library/alpine:%s", runtime.GOARCH)

	expected := []Stage{
		{
			Alias:    "builder",
			Pullspec: expectedPullspec,
			Copies:   []Copy{},
		},
		{
			Alias:    FinalStage,
			Pullspec: "",
			Copies: []Copy{
				{
					From:        "builder",
					Sources:     []string{"/usr/bin/binary"},
					Destination: "/usr/bin/binary",
				},
			},
		},
	}

	reader := strings.NewReader(containerfile)
	actual, err := Parse(reader, BuildOptions{})

	if err != nil {
		t.Fatalf("Parsing failed: %v", err)
	}

	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("Actual parsed stages %+v don't match expected %+v", actual, expected)
	}
}

// Test that parsing containerfiles with relative paths fails when attempting
// to use ambiguous relative paths.
func TestParseInvalidRelativePaths(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		containerfile string
	}{
		"relative WORKDIR": {
			containerfile: `FROM scratch
							WORKDIR app/`,
		},
		"relative COPY destination without WORKDIR": {
			containerfile: `FROM docker.io/library/helm:latest AS builder
							FROM scratch
							COPY --from=builder /usr/bin/helm helm`,
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			reader := strings.NewReader(test.containerfile)
			_, err := Parse(reader, BuildOptions{})
			if !errors.Is(err, ErrAmbiguousRelativePath) {
				t.Fatalf("Parse didn't return WorkdirError when expected, actual: %v", err)
			}
		})
	}
}

func TestParse(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		containerfile string
		buildOptions  BuildOptions
		expected      []Stage
	}{
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
			expected: []Stage{
				{
					Alias:    "builder",
					Pullspec: "docker.io/library/alpine:latest",
					Copies:   []Copy{},
				},
				{
					Alias:    FinalStage,
					Pullspec: "",
					Copies: []Copy{
						{
							From:        "docker.io/library/fedora:latest",
							Sources:     []string{"/usr/bin/oras"},
							Destination: "/usr/bin/oras",
							Type:        CopyTypeExternal,
						},
						{
							From:        "builder",
							Sources:     []string{"/usr/bin/binary"},
							Destination: "/usr/bin/binary",
							Type:        CopyTypeBuilder,
						},
					},
				},
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
			expected: []Stage{
				{
					Alias:    FinalStage,
					Pullspec: "",
					Copies: []Copy{
						{
							From:        "docker.io/library/alpine:latest",
							Sources:     []string{"/usr/bin/binary"},
							Destination: "/usr/bin/binary",
							Type:        CopyTypeExternal,
						},
					},
				},
			},
		},
		"copies in final stage only": {
			containerfile: `FROM docker.io/library/fedora:latest AS builder1
							FROM docker.io/alpine/helm:latest AS builder2
							FROM scratch
							COPY --from=builder1 /usr/bin/oras /usr/bin/oras
							COPY --from=builder2 /usr/bin/helm /usr/bin/helm`,
			expected: []Stage{
				{
					Alias:    "builder1",
					Pullspec: "docker.io/library/fedora:latest",
					Copies:   []Copy{},
				},
				{
					Alias:    "builder2",
					Pullspec: "docker.io/alpine/helm:latest",
					Copies:   []Copy{},
				},
				{
					Alias:    FinalStage,
					Pullspec: "",
					Copies: []Copy{
						{
							From:        "builder1",
							Sources:     []string{"/usr/bin/oras"},
							Destination: "/usr/bin/oras",
							Type:        CopyTypeBuilder,
						},
						{
							From:        "builder2",
							Sources:     []string{"/usr/bin/helm"},
							Destination: "/usr/bin/helm",
							Type:        CopyTypeBuilder,
						},
					},
				},
			},
		},
		"recursive multi-stage file copy": {
			containerfile: `FROM docker.io/library/fedora:latest AS builder1
							FROM docker.io/alpine/helm:latest AS builder2
							COPY --from=builder1 /usr/bin/oras /usr/bin/oras
							FROM scratch
							COPY --from=builder2 /usr/bin/oras /usr/bin/oras`,
			expected: []Stage{
				{
					Alias:    "builder1",
					Pullspec: "docker.io/library/fedora:latest",
					Copies:   []Copy{},
				},
				{
					Alias:    "builder2",
					Pullspec: "docker.io/alpine/helm:latest",
					Copies: []Copy{
						{
							From:        "builder1",
							Sources:     []string{"/usr/bin/oras"},
							Destination: "/usr/bin/oras",
							Type:        CopyTypeBuilder,
						},
					},
				},
				{
					Alias:    FinalStage,
					Pullspec: "",
					Copies: []Copy{
						{
							From:        "builder2",
							Sources:     []string{"/usr/bin/oras"},
							Destination: "/usr/bin/oras",
							Type:        CopyTypeBuilder,
						},
					},
				},
			},
		},
		"relative path resolution": {
			containerfile: `FROM docker.io/alpine/helm:latest AS builder
							FROM scratch
							WORKDIR /usr/bin/
							COPY --from=builder /usr/bin/rustc rustcompiler
							COPY --from=builder /usr/bin/mono ../app/
							COPY --from=builder /usr/bin/go ./go
							COPY --from=builder /usr/bin/helm app/
							COPY --from=builder /usr/bin/syft ..
							COPY --from=builder /usr/bin/capo ../..
							COPY --from=builder /usr/bin/oras .`,
			expected: []Stage{
				{
					Alias:    "builder",
					Pullspec: "docker.io/alpine/helm:latest",
					Copies:   []Copy{},
				},
				{
					Alias:    FinalStage,
					Pullspec: "",
					Copies: []Copy{
						{
							From:        "builder",
							Sources:     []string{"/usr/bin/rustc"},
							Destination: "/usr/bin/rustcompiler",
							Type:        CopyTypeBuilder,
						},
						{
							From:        "builder",
							Sources:     []string{"/usr/bin/mono"},
							Destination: "/usr/app/",
							Type:        CopyTypeBuilder,
						},
						{
							From:        "builder",
							Sources:     []string{"/usr/bin/go"},
							Destination: "/usr/bin/go",
							Type:        CopyTypeBuilder,
						},
						{
							From:        "builder",
							Sources:     []string{"/usr/bin/helm"},
							Destination: "/usr/bin/app/",
							Type:        CopyTypeBuilder,
						},
						{
							From:        "builder",
							Sources:     []string{"/usr/bin/syft"},
							Destination: "/usr/",
							Type:        CopyTypeBuilder,
						},
						{
							From:        "builder",
							Sources:     []string{"/usr/bin/capo"},
							Destination: "/",
							Type:        CopyTypeBuilder,
						},
						{
							From:        "builder",
							Sources:     []string{"/usr/bin/oras"},
							Destination: "/usr/bin/",
							Type:        CopyTypeBuilder,
						},
					},
				},
			},
		},
		"recursive multi-stage file copy - mixed sources": {
			containerfile: `FROM docker.io/library/fedora:latest AS builder1
							FROM docker.io/alpine/helm:latest AS builder2
							COPY --from=builder1 /usr/bin/oras /usr/bin/oras
							FROM scratch
							COPY --from=builder2 /usr/bin/oras /usr/bin/helm /app/`,
			expected: []Stage{
				{
					Alias:    "builder1",
					Pullspec: "docker.io/library/fedora:latest",
					Copies:   []Copy{},
				},
				{
					Alias:    "builder2",
					Pullspec: "docker.io/alpine/helm:latest",
					Copies: []Copy{
						{
							From:        "builder1",
							Sources:     []string{"/usr/bin/oras"},
							Destination: "/usr/bin/oras",
							Type:        CopyTypeBuilder,
						},
					},
				},
				{
					Alias:    FinalStage,
					Pullspec: "",
					Copies: []Copy{
						{
							From:        "builder2",
							Sources:     []string{"/usr/bin/oras", "/usr/bin/helm"},
							Destination: "/app/",
							Type:        CopyTypeBuilder,
						},
					},
				},
			},
		},
		"multi-stage directory copy": {
			containerfile: `FROM docker.io/library/fedora:latest AS builder1
							FROM docker.io/alpine/helm:latest AS builder2
							COPY --from=builder1 /usr/bin/oras /app/oras
							COPY --from=builder1 /bin/* /app/
							FROM scratch
							COPY --from=builder2 /app/ /app/`,
			expected: []Stage{
				{
					Alias:    "builder1",
					Pullspec: "docker.io/library/fedora:latest",
					Copies:   []Copy{},
				},
				{
					Alias:    "builder2",
					Pullspec: "docker.io/alpine/helm:latest",
					Copies: []Copy{
						{
							From:        "builder1",
							Sources:     []string{"/usr/bin/oras"},
							Destination: "/app/oras",
							Type:        CopyTypeBuilder,
						},
						{
							From:        "builder1",
							Sources:     []string{"/bin/*"},
							Destination: "/app/",
							Type:        CopyTypeBuilder,
						},
					},
				},
				{
					Alias:    FinalStage,
					Pullspec: "",
					Copies: []Copy{
						{
							From:        "builder2",
							Sources:     []string{"/app/"},
							Destination: "/app/",
							Type:        CopyTypeBuilder,
						},
					},
				},
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
			expected: []Stage{
				{
					Alias:    "builder1",
					Pullspec: "docker.io/library/fedora:latest",
					Copies:   []Copy{},
				},
				{
					Alias:    "builder2",
					Pullspec: "docker.io/alpine/helm:latest",
					Copies: []Copy{
						{
							From:        "builder1",
							Sources:     []string{"/usr/bin/kubectl"},
							Destination: "/tools/kubectl",
							Type:        CopyTypeBuilder,
						},
					},
				},
				{
					Alias:    FinalStage,
					Pullspec: "",
					Copies: []Copy{
						{
							From:        "builder1",
							Sources:     []string{"/lib/libc.so"},
							Destination: "/lib/libc.so",
							Type:        CopyTypeBuilder,
						},
						{
							From:        "builder2",
							Sources:     []string{"/tools/"},
							Destination: "/usr/bin/",
							Type:        CopyTypeBuilder,
						},
						{
							From:        "builder2",
							Sources:     []string{"/usr/bin/helm"},
							Destination: "/usr/bin/helm",
							Type:        CopyTypeBuilder,
						},
					},
				},
			},
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			reader := strings.NewReader(test.containerfile)
			actual, err := Parse(reader, test.buildOptions)
			if err != nil {
				t.Fatalf("Parsing failed: %v", err)
			}

			if !reflect.DeepEqual(actual, test.expected) {
				t.Fatalf("Actual parsed stages %+v don't match expected %+v", actual, test.expected)
			}
		})
	}
}

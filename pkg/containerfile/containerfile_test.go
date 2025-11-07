package containerfile

import (
	"reflect"
	"strings"
	"testing"
)

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
						},
						{
							From:        "builder",
							Sources:     []string{"/usr/bin/binary"},
							Destination: "/usr/bin/binary",
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
						},
						{
							From:        "builder2",
							Sources:     []string{"/usr/bin/helm"},
							Destination: "/usr/bin/helm",
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
						},
						{
							From:        "builder1",
							Sources:     []string{"/bin/*"},
							Destination: "/app/",
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
						},
						{
							From:        "builder2",
							Sources:     []string{"/tools/"},
							Destination: "/usr/bin/",
						},
						{
							From:        "builder2",
							Sources:     []string{"/usr/bin/helm"},
							Destination: "/usr/bin/helm",
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

//go:build unit

package containerfile

import (
	"fmt"
	"runtime"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
)

func TestParseBuiltinArgs(t *testing.T) {
	t.Parallel()
	containerfile := `FROM docker.io/library/alpine:${TARGETARCH} as builder
						FROM scratch
						COPY --from=builder /usr/bin/binary /usr/bin/binary`

	expectedPullspec := fmt.Sprintf("docker.io/library/alpine:%s", runtime.GOARCH)

	expected := []Stage{
		{
			Alias:   "builder",
			Base:    expectedPullspec,
			BaseRef: expectedPullspec,
			Index:   0,
			Copies:  []Copy{},
			Mounts:  []Mount{},
		},
		{
			Alias:   FinalStage,
			Base:    "scratch",
			BaseRef: "scratch",
			Index:   -1,
			Copies: []Copy{
				{
					From:        "builder",
					Sources:     []string{"/usr/bin/binary"},
					Destination: "/usr/bin/binary",
				},
			},
			Mounts: []Mount{},
		},
	}

	reader := strings.NewReader(containerfile)
	actual, err := Parse(reader, BuildOptions{})

	if err != nil {
		t.Fatalf("Parsing failed: %v", err)
	}

	if diff := cmp.Diff(expected, actual, cmpopts.EquateEmpty()); diff != "" {
		t.Errorf("Parse() result mismatch (-want +got):\n%s", diff)
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
					Alias:   "builder",
					Base:    "docker.io/library/alpine:latest",
					BaseRef: "docker.io/library/alpine:latest",
					Index:   0,
					Copies:  []Copy{},
					Mounts:  []Mount{},
				},
				{
					Alias:   FinalStage,
					Base:    "scratch",
					BaseRef: "scratch",
					Index:   -1,
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
						},
					},
					Mounts: []Mount{},
				},
			}},
		"alias arg evaluation": {
			containerfile: `ARG zero=0
							ARG one=1
							FROM docker.io/library/fedora as builder0
							FROM docker.io/library/alpine:latest as builder1
							FROM builder${one} as somethingelse
							COPY --from=builder${zero} /usr/bin/oras /usr/bin/oras
							RUN --mount=from=builder${one},type=bind,src=/usr/bin/binary,dst=/usr/bin/binary ls /usr/bin/binary
							FROM scratch`,
			expected: []Stage{
				{
					Alias:   "builder0",
					Base:    "docker.io/library/fedora",
					BaseRef: "docker.io/library/fedora",
					Index:   0,
					Copies:  []Copy{},
					Mounts:  []Mount{},
				},
				{
					Alias:   "builder1",
					Base:    "docker.io/library/alpine:latest",
					BaseRef: "docker.io/library/alpine:latest",
					Index:   1,
					Copies:  []Copy{},
					Mounts:  []Mount{},
				},
				{
					Alias:   "somethingelse",
					Base:    "docker.io/library/alpine:latest",
					BaseRef: "builder1",
					Index:   2,
					Copies: []Copy{
						{
							From:        "builder0",
							Sources:     []string{"/usr/bin/oras"},
							Destination: "/usr/bin/oras",
							Type:        CopyTypeBuilder,
						},
					},
					Mounts: []Mount{
						{
							FromRaw:   "builder1",
							MountType: MountTypeBind,
						},
					},
				},
				{Alias: FinalStage, Base: "scratch", BaseRef: "scratch", Index: -1, Copies: []Copy{}, Mounts: []Mount{}},
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
					Alias:   FinalStage,
					Base:    "docker.io/library/fedora:latest",
					BaseRef: "docker.io/library/fedora:latest",
					Index:   -1,
					Copies: []Copy{
						{
							From:        "docker.io/library/alpine:latest",
							Sources:     []string{"/usr/bin/binary"},
							Destination: "/usr/bin/binary",
							Type:        CopyTypeExternal,
						},
					},
					Mounts: []Mount{},
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
					Alias:   "builder1",
					Base:    "docker.io/library/fedora:latest",
					BaseRef: "docker.io/library/fedora:latest",
					Index:   0,
					Copies:  []Copy{},
					Mounts:  []Mount{},
				},
				{
					Alias:   "builder2",
					Base:    "docker.io/alpine/helm:latest",
					BaseRef: "docker.io/alpine/helm:latest",
					Index:   1,
					Copies:  []Copy{},
					Mounts:  []Mount{},
				},
				{
					Alias:   FinalStage,
					Base:    "scratch",
					BaseRef: "scratch",
					Index:   -1,
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
					Mounts: []Mount{},
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
					Alias:   "builder1",
					Base:    "docker.io/library/fedora:latest",
					BaseRef: "docker.io/library/fedora:latest",
					Index:   0,
					Copies:  []Copy{},
					Mounts:  []Mount{},
				},
				{
					Alias:   "builder2",
					Base:    "docker.io/alpine/helm:latest",
					BaseRef: "docker.io/alpine/helm:latest",
					Index:   1,
					Copies: []Copy{
						{
							From:        "builder1",
							Sources:     []string{"/usr/bin/oras"},
							Destination: "/usr/bin/oras",
							Type:        CopyTypeBuilder,
						},
					},
					Mounts: []Mount{},
				},
				{
					Alias:   FinalStage,
					Base:    "scratch",
					BaseRef: "scratch",
					Index:   -1,
					Copies: []Copy{
						{
							From:        "builder2",
							Sources:     []string{"/usr/bin/oras"},
							Destination: "/usr/bin/oras",
							Type:        CopyTypeBuilder,
						},
					},
					Mounts: []Mount{},
				},
			},
		},
		"workdir switching - only relative paths": {
			containerfile: `FROM docker.io/alpine/helm:latest AS builder
							FROM scratch
							WORKDIR usr/
							COPY --from=builder /usr/bin/go ./go
							WORKDIR bin/
							COPY --from=builder /usr/bin/capo ../..`,
			expected: []Stage{
				{
					Alias:   "builder",
					Base:    "docker.io/alpine/helm:latest",
					BaseRef: "docker.io/alpine/helm:latest",
					Index:   0,
					Copies:  []Copy{},
					Mounts:  []Mount{},
				},
				{
					Alias:   FinalStage,
					Base:    "scratch",
					BaseRef: "scratch",
					Index:   -1,
					Copies: []Copy{
						{
							From:        "builder",
							Sources:     []string{"/usr/bin/go"},
							Destination: "./go",
							Type:        CopyTypeBuilder,
							Workdir:     "usr",
						},
						{
							From:        "builder",
							Sources:     []string{"/usr/bin/capo"},
							Destination: "../..",
							Type:        CopyTypeBuilder,
							Workdir:     "usr/bin",
						},
					},
					Mounts: []Mount{},
				},
			},
		},
		"relative paths with workdir switching": {
			containerfile: `FROM docker.io/alpine/helm:latest AS builder
							FROM scratch
							COPY --from=builder /usr/bin/rustc rustcompiler
							COPY --from=builder /usr/bin/mono ../app/
							WORKDIR /bin/
							COPY --from=builder /usr/bin/go ./go
							COPY --from=builder /usr/bin/helm app/
							COPY --from=builder /usr/bin/syft ..
							WORKDIR /usr/bin/
							COPY --from=builder /usr/bin/capo ../..
							COPY --from=builder /usr/bin/oras .
							WORKDIR app/
							COPY --from=builder /usr/bin/mage .`,
			expected: []Stage{
				{
					Alias:   "builder",
					Base:    "docker.io/alpine/helm:latest",
					BaseRef: "docker.io/alpine/helm:latest",
					Index:   0,
					Copies:  []Copy{},
					Mounts:  []Mount{},
				},
				{
					Alias:   FinalStage,
					Base:    "scratch",
					BaseRef: "scratch",
					Index:   -1,
					Copies: []Copy{
						{
							From:        "builder",
							Sources:     []string{"/usr/bin/rustc"},
							Destination: "rustcompiler",
							Type:        CopyTypeBuilder,
							Workdir:     "",
						},
						{
							From:        "builder",
							Sources:     []string{"/usr/bin/mono"},
							Destination: "../app/",
							Type:        CopyTypeBuilder,
							Workdir:     "",
						},
						{
							From:        "builder",
							Sources:     []string{"/usr/bin/go"},
							Destination: "./go",
							Type:        CopyTypeBuilder,
							Workdir:     "/bin/",
						},
						{
							From:        "builder",
							Sources:     []string{"/usr/bin/helm"},
							Destination: "app/",
							Type:        CopyTypeBuilder,
							Workdir:     "/bin/",
						},
						{
							From:        "builder",
							Sources:     []string{"/usr/bin/syft"},
							Destination: "..",
							Type:        CopyTypeBuilder,
							Workdir:     "/bin/",
						},
						{
							From:        "builder",
							Sources:     []string{"/usr/bin/capo"},
							Destination: "../..",
							Type:        CopyTypeBuilder,
							Workdir:     "/usr/bin/",
						},
						{
							From:        "builder",
							Sources:     []string{"/usr/bin/oras"},
							Destination: ".",
							Type:        CopyTypeBuilder,
							Workdir:     "/usr/bin/",
						},
						{
							From:        "builder",
							Sources:     []string{"/usr/bin/mage"},
							Destination: ".",
							Type:        CopyTypeBuilder,
							Workdir:     "/usr/bin/app",
						},
					},
					Mounts: []Mount{},
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
					Alias:   "builder1",
					Base:    "docker.io/library/fedora:latest",
					BaseRef: "docker.io/library/fedora:latest",
					Index:   0,
					Copies:  []Copy{},
					Mounts:  []Mount{},
				},
				{
					Alias:   "builder2",
					Base:    "docker.io/alpine/helm:latest",
					BaseRef: "docker.io/alpine/helm:latest",
					Index:   1,
					Copies: []Copy{
						{
							From:        "builder1",
							Sources:     []string{"/usr/bin/oras"},
							Destination: "/usr/bin/oras",
							Type:        CopyTypeBuilder,
						},
					},
					Mounts: []Mount{},
				},
				{
					Alias:   FinalStage,
					Base:    "scratch",
					BaseRef: "scratch",
					Index:   -1,
					Copies: []Copy{
						{
							From:        "builder2",
							Sources:     []string{"/usr/bin/oras", "/usr/bin/helm"},
							Destination: "/app/",
							Type:        CopyTypeBuilder,
						},
					},
					Mounts: []Mount{},
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
					Alias:   "builder1",
					Base:    "docker.io/library/fedora:latest",
					BaseRef: "docker.io/library/fedora:latest",
					Index:   0,
					Copies:  []Copy{},
					Mounts:  []Mount{},
				},
				{
					Alias:   "builder2",
					Base:    "docker.io/alpine/helm:latest",
					BaseRef: "docker.io/alpine/helm:latest",
					Index:   1,
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
					Mounts: []Mount{},
				},
				{
					Alias:   FinalStage,
					Base:    "scratch",
					BaseRef: "scratch",
					Index:   -1,
					Copies: []Copy{
						{
							From:        "builder2",
							Sources:     []string{"/app/"},
							Destination: "/app/",
							Type:        CopyTypeBuilder,
						},
					},
					Mounts: []Mount{},
				},
			},
		},
		"COPY --from numeric stage index": {
			containerfile: `FROM quay.io/rhel:9
							FROM scratch
							COPY --from=0 /usr/bin/binary /usr/bin/binary`,
			expected: []Stage{
				{
					Alias:   "0",
					Base:    "quay.io/rhel:9",
					BaseRef: "quay.io/rhel:9",
					Index:   0,
					Copies:  []Copy{},
					Mounts:  []Mount{},
				},
				{
					Alias:   FinalStage,
					Base:    "scratch",
					BaseRef: "scratch",
					Index:   -1,
					Copies: []Copy{
						{
							From:        "0",
							Sources:     []string{"/usr/bin/binary"},
							Destination: "/usr/bin/binary",
							Type:        CopyTypeBuilder,
						},
					},
					Mounts: []Mount{},
				},
			},
		},
		"COPY --from numeric index with named stage": {
			containerfile: `FROM quay.io/rhel:9 AS builder
							FROM scratch
							COPY --from=0 /usr/bin/binary /usr/bin/binary`,
			expected: []Stage{
				{
					Alias:   "builder",
					Base:    "quay.io/rhel:9",
					BaseRef: "quay.io/rhel:9",
					Index:   0,
					Copies:  []Copy{},
					Mounts:  []Mount{},
				},
				{
					Alias:   FinalStage,
					Base:    "scratch",
					BaseRef: "scratch",
					Index:   -1,
					Copies: []Copy{
						{
							From:        "0",
							Sources:     []string{"/usr/bin/binary"},
							Destination: "/usr/bin/binary",
							Type:        CopyTypeBuilder,
						},
					},
					Mounts: []Mount{},
				},
			},
		},
		"COPY --from numeric index out of bounds is external image": {
			containerfile: `FROM quay.io/rhel:9
							COPY --from=5 /usr/bin/binary /usr/bin/binary`,
			expected: []Stage{
				{
					Alias:   FinalStage,
					Base:    "quay.io/rhel:9",
					BaseRef: "quay.io/rhel:9",
					Index:   -1,
					Copies: []Copy{
						{
							From:        "5",
							Sources:     []string{"/usr/bin/binary"},
							Destination: "/usr/bin/binary",
							Type:        CopyTypeExternal,
						},
					},
					Mounts: []Mount{},
				},
			},
		},
		"RUN --mount=from external image": {
			containerfile: `FROM quay.io/rhel:9
							RUN --mount=type=bind,from=quay.io/tools:1,src=/bin/tool,dst=/tmp/tool /tmp/tool --version`,
			expected: []Stage{
				{
					Alias:   FinalStage,
					Base:    "quay.io/rhel:9",
					BaseRef: "quay.io/rhel:9",
					Index:   -1,
					Copies:  []Copy{},
					Mounts: []Mount{
						{FromRaw: "quay.io/tools:1", Pullspec: "quay.io/tools:1"},
					},
				},
			},
		},
		"RUN --mount=from builder stage": {
			containerfile: `FROM quay.io/rhel:9 AS builder
							FROM scratch
							RUN --mount=type=bind,from=builder,src=/app,dst=/app ls /app`,
			expected: []Stage{
				{
					Alias:   "builder",
					Base:    "quay.io/rhel:9",
					BaseRef: "quay.io/rhel:9",
					Index:   0,
					Copies:  []Copy{},
					Mounts:  []Mount{},
				},
				{
					Alias:   FinalStage,
					Base:    "scratch",
					BaseRef: "scratch",
					Index:   -1,
					Copies:  []Copy{},
					Mounts: []Mount{
						{FromRaw: "builder"},
					},
				},
			},
		},
		"RUN --mount=from numeric stage index": {
			containerfile: `FROM quay.io/rhel:9 AS builder
							FROM scratch
							RUN --mount=type=bind,from=0,src=/app,dst=/app ls /app`,
			expected: []Stage{
				{
					Alias:   "builder",
					Base:    "quay.io/rhel:9",
					BaseRef: "quay.io/rhel:9",
					Index:   0,
					Copies:  []Copy{},
					Mounts:  []Mount{},
				},
				{
					Alias:   FinalStage,
					Base:    "scratch",
					BaseRef: "scratch",
					Index:   -1,
					Copies:  []Copy{},
					Mounts: []Mount{
						{FromRaw: "0"},
					},
				},
			},
		},
		"duplicate stage names": {
			containerfile: `FROM quay.io/rhel:9 AS builder
							FROM quay.io/fedora:42 AS builder
							FROM scratch
							COPY --from=builder /app /app`,
			expected: []Stage{
				{Alias: "builder", Base: "quay.io/rhel:9", BaseRef: "quay.io/rhel:9", Index: 0, Copies: []Copy{}, Mounts: []Mount{}},
				{Alias: "builder", Base: "quay.io/fedora:42", BaseRef: "quay.io/fedora:42", Index: 1, Copies: []Copy{}, Mounts: []Mount{}},
				{Alias: FinalStage, Base: "scratch", BaseRef: "scratch", Index: -1, Copies: []Copy{
					{From: "builder", Sources: []string{"/app"}, Destination: "/app", Type: CopyTypeBuilder},
				}, Mounts: []Mount{}},
			},
		},
		"COPY source paths are normalized to absolute clean paths": {
			containerfile: `FROM docker.io/library/alpine:latest AS builder
							FROM scratch
							COPY --from=builder relative/auntie/jane /dest1
							COPY --from=builder /foo//bar /dest2
							COPY --from=builder /foo/baz/../bar /dest3`,
			expected: []Stage{
				{
					Alias:   "builder",
					Base:    "docker.io/library/alpine:latest",
					BaseRef: "docker.io/library/alpine:latest",
					Index:   0,
					Copies:  []Copy{},
					Mounts:  []Mount{},
				},
				{
					Alias:   FinalStage,
					Base:    "scratch",
					BaseRef: "scratch",
					Index:   -1,
					Copies: []Copy{
						{
							From:        "builder",
							Sources:     []string{"/relative/auntie/jane"},
							Destination: "/dest1",
							Type:        CopyTypeBuilder,
						},
						{
							From:        "builder",
							Sources:     []string{"/foo/bar"},
							Destination: "/dest2",
							Type:        CopyTypeBuilder,
						},
						{
							From:        "builder",
							Sources:     []string{"/foo/bar"},
							Destination: "/dest3",
							Type:        CopyTypeBuilder,
						},
					},
					Mounts: []Mount{},
				},
			},
		},
		"single label": {
			containerfile: `FROM quay.io/rhel:9
							LABEL version=1.0`,
			expected: []Stage{
				{
					Alias:  FinalStage,
					Base:   "quay.io/rhel:9",
					Copies: []Copy{},
					Mounts: []Mount{},
					Labels: map[string]string{"version": "1.0"},
				},
			},
		},
		"multiple labels on one line": {
			containerfile: `FROM quay.io/rhel:9
							LABEL version=1.0 vendor="Red Hat"`,
			expected: []Stage{
				{
					Alias:  FinalStage,
					Base:   "quay.io/rhel:9",
					Copies: []Copy{},
					Mounts: []Mount{},
					Labels: map[string]string{"version": "1.0", "vendor": "Red Hat"},
				},
			},
		},
		"multiple LABEL instructions merge": {
			containerfile: `FROM quay.io/rhel:9
							LABEL version=1.0
							LABEL vendor="Red Hat"`,
			expected: []Stage{
				{
					Alias:  FinalStage,
					Base:   "quay.io/rhel:9",
					Copies: []Copy{},
					Mounts: []Mount{},
					Labels: map[string]string{"version": "1.0", "vendor": "Red Hat"},
				},
			},
		},
		"label with ARG substitution": {
			containerfile: `ARG VER=2.0
							FROM quay.io/rhel:9
							LABEL version=$VER`,
			buildOptions: BuildOptions{},
			expected: []Stage{
				{
					Alias:  FinalStage,
					Base:   "quay.io/rhel:9",
					Copies: []Copy{},
					Mounts: []Mount{},
					Labels: map[string]string{"version": "2.0"},
				},
			},
		},
		"later label overrides earlier": {
			containerfile: `FROM quay.io/rhel:9
							LABEL version=1.0
							LABEL version=2.0`,
			expected: []Stage{
				{
					Alias:  FinalStage,
					Base:   "quay.io/rhel:9",
					Copies: []Copy{},
					Mounts: []Mount{},
					Labels: map[string]string{"version": "2.0"},
				},
			},
		},
		"labels are per-stage": {
			containerfile: `FROM quay.io/rhel:9 AS builder
							LABEL stage=builder
							FROM scratch
							LABEL stage=final`,
			expected: []Stage{
				{
					Alias:  "builder",
					Base:   "quay.io/rhel:9",
					Copies: []Copy{},
					Mounts: []Mount{},
					Labels: map[string]string{"stage": "builder"},
				},
				{
					Alias:  FinalStage,
					Base:   "scratch",
					Copies: []Copy{},
					Mounts: []Mount{},
					Labels: map[string]string{"stage": "final"},
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
					Alias:   "builder1",
					Base:    "docker.io/library/fedora:latest",
					BaseRef: "docker.io/library/fedora:latest",
					Index:   0,
					Copies:  []Copy{},
					Mounts:  []Mount{},
				},
				{
					Alias:   "builder2",
					Base:    "docker.io/alpine/helm:latest",
					BaseRef: "docker.io/alpine/helm:latest",
					Index:   1,
					Copies: []Copy{
						{
							From:        "builder1",
							Sources:     []string{"/usr/bin/kubectl"},
							Destination: "/tools/kubectl",
							Type:        CopyTypeBuilder,
						},
					},
					Mounts: []Mount{},
				},
				{
					Alias:   FinalStage,
					Base:    "scratch",
					BaseRef: "scratch",
					Index:   -1,
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
					Mounts: []Mount{},
				},
			},
		},
		"chained stages resolve base through chain": {
			containerfile: `FROM docker.io/library/fedora:latest AS parent
							FROM parent AS child
							FROM child AS grandchild
							FROM scratch
							COPY --from=grandchild /app /app`,
			expected: []Stage{
				{
					Alias:   "parent",
					Base:    "docker.io/library/fedora:latest",
					BaseRef: "docker.io/library/fedora:latest",
					Index:   0,
					Copies:  []Copy{},
					Mounts:  []Mount{},
				},
				{
					Alias:   "child",
					Base:    "docker.io/library/fedora:latest",
					BaseRef: "parent",
					Index:   1,
					Copies:  []Copy{},
					Mounts:  []Mount{},
				},
				{
					Alias:   "grandchild",
					Base:    "docker.io/library/fedora:latest",
					BaseRef: "child",
					Index:   2,
					Copies:  []Copy{},
					Mounts:  []Mount{},
				},
				{
					Alias:   FinalStage,
					Base:    "scratch",
					BaseRef: "scratch",
					Index:   -1,
					Copies: []Copy{
						{From: "grandchild", Sources: []string{"/app"}, Destination: "/app", Type: CopyTypeBuilder},
					},
					Mounts: []Mount{},
				},
			},
		},
		"run with mount is parsed correctly": {
			containerfile: `FROM quay.io/rhel:9 AS builder
							FROM scratch
							RUN --mount=type=bind,from=builder,src=/app,dst=/app ls /app
							RUN --mount=type=cache,from=quay.io/builder,src=/cache,dst=/cache ls /cache`,
			expected: []Stage{
				{Alias: "builder", Base: "quay.io/rhel:9", BaseRef: "quay.io/rhel:9", Index: 0, Copies: []Copy{}, Mounts: []Mount{}},
				{Alias: FinalStage, Base: "scratch", BaseRef: "scratch", Index: -1, Copies: []Copy{}, Mounts: []Mount{
					{
						FromRaw:   "builder",
						MountType: MountTypeBind,
					},
					{
						FromRaw:   "quay.io/builder",
						Pullspec:  "quay.io/builder",
						MountType: MountTypeCache,
					},
				}},
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

			if diff := cmp.Diff(test.expected, actual, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("Parse() result mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

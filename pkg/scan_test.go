//go:build unit

package capo

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/opencontainers/go-digest"

	"github.com/konflux-ci/capo/pkg/containerfile"
	"github.com/konflux-ci/capo/pkg/storageclient"

	"github.com/konflux-ci/capo/internal/testutils"
)

func testDigest(seed string) digest.Digest {
	repeated := strings.Repeat(seed, 64/len(seed)+1)
	return digest.Digest("sha256:" + repeated[:64])
}

func configWithWorkdir(workdir string) storageclient.OCIImageConfig {
	return storageclient.OCIImageConfig{
		Config: struct {
			Labels  map[string]string `json:"Labels"`
			Workdir string            `json:"WorkingDir"`
		}{
			Workdir: workdir,
		},
	}
}

func TestGetPackageSources(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		cf                containerfile.Containerfile
		digests           map[string]digest.Digest
		configs           map[string]storageclient.OCIImageConfig
		expectedRoots []packageSource
	}{
		"only external copy in final": {
			cf: containerfile.Containerfile{Stages: []containerfile.Stage{
				{
					Alias:   containerfile.FinalStage,
					Base:    "scratch",
					BaseRef: "scratch",
					Index:   -1,
					Copies: []containerfile.Copy{
						{
							From:        "docker.io/library/fedora:latest",
							Sources:     []string{"/usr/bin/oras"},
							Destination: "/usr/bin/oras",
							Type:        containerfile.CopyTypeExternal,
						},
					},
				},
			}},
			digests: map[string]digest.Digest{
				"docker.io/library/fedora:latest": testDigest("abc123"),
			},
			configs:       map[string]storageclient.OCIImageConfig{},
			expectedRoots: []packageSource{
				{
					pullspec:   "docker.io/library/fedora:latest",
					digestBase: "docker.io/library/fedora@" + string(testDigest("abc123")),
					sources:    []string{"/usr/bin/oras"},
					external:   true,
				},
			},
		},
		"copies in final stage only": {
			cf: containerfile.Containerfile{Stages: []containerfile.Stage{
				{
					Alias:   "builder1",
					Base:    "docker.io/library/fedora:latest",
					BaseRef: "docker.io/library/fedora:latest",
					Index:   0,
					Copies:  []containerfile.Copy{},
				},
				{
					Alias:   "builder2",
					Base:    "docker.io/alpine/helm:latest",
					BaseRef: "docker.io/alpine/helm:latest",
					Index:   1,
					Copies:  []containerfile.Copy{},
				},
				{
					Alias:   containerfile.FinalStage,
					Base:    "scratch",
					BaseRef: "scratch",
					Index:   -1,
					Copies: []containerfile.Copy{
						{
							From:        "builder1",
							Sources:     []string{"/usr/bin/oras"},
							Destination: "/usr/bin/oras",
							Type:        containerfile.CopyTypeBuilder,
						},
						{
							From:        "builder2",
							Sources:     []string{"/usr/bin/helm"},
							Destination: "/usr/bin/helm",
							Type:        containerfile.CopyTypeBuilder,
						},
					},
				},
			}},
			digests: map[string]digest.Digest{
				"docker.io/library/fedora:latest": testDigest("def456"),
				"docker.io/alpine/helm:latest":    testDigest("ca0789"),
			},
			configs: map[string]storageclient.OCIImageConfig{
				"docker.io/library/fedora:latest": configWithWorkdir("/"),
				"docker.io/alpine/helm:latest":    configWithWorkdir("/"),
			},
			expectedRoots: []packageSource{
				{
					index:      0,
					alias:      "builder1",
					pullspec:   "docker.io/library/fedora:latest",
					digestBase: "docker.io/library/fedora@" + string(testDigest("def456")),
					sources:    []string{"/usr/bin/oras"},
				},
				{
					index:      1,
					alias:      "builder2",
					pullspec:   "docker.io/alpine/helm:latest",
					digestBase: "docker.io/alpine/helm@" + string(testDigest("ca0789")),
					sources:    []string{"/usr/bin/helm"},
				},
			},
		},
		"recursive multi-stage file copy": {
			cf: containerfile.Containerfile{Stages: []containerfile.Stage{
				{
					Alias:   "builder1",
					Base:    "docker.io/library/fedora:latest",
					BaseRef: "docker.io/library/fedora:latest",
					Index:   0,
					Copies:  []containerfile.Copy{},
				},
				{
					Alias:   "builder2",
					Base:    "docker.io/alpine/helm:latest",
					BaseRef: "docker.io/alpine/helm:latest",
					Index:   1,
					Copies: []containerfile.Copy{
						{
							From:        "builder1",
							Sources:     []string{"/usr/bin/oras"},
							Destination: "/usr/bin/oras",
							Type:        containerfile.CopyTypeBuilder,
						},
					},
				},
				{
					Alias:   containerfile.FinalStage,
					Base:    "scratch",
					BaseRef: "scratch",
					Index:   -1,
					Copies: []containerfile.Copy{
						{
							From:        "builder2",
							Sources:     []string{"/usr/bin/oras"},
							Destination: "/usr/bin/oras",
							Type:        containerfile.CopyTypeBuilder,
						},
					},
				},
			}},
			digests: map[string]digest.Digest{
				"docker.io/library/fedora:latest": testDigest("da0012"),
				"docker.io/alpine/helm:latest":    testDigest("ea0345"),
			},
			configs: map[string]storageclient.OCIImageConfig{
				"docker.io/library/fedora:latest": configWithWorkdir("/"),
				"docker.io/alpine/helm:latest":    configWithWorkdir("/"),
			},
			expectedRoots: []packageSource{
				{
					index:      0,
					alias:      "builder1",
					pullspec:   "docker.io/library/fedora:latest",
					digestBase: "docker.io/library/fedora@" + string(testDigest("da0012")),
					sources:    []string{"/usr/bin/oras"},
				},
				{
					index:      1,
					alias:      "builder2",
					pullspec:   "docker.io/alpine/helm:latest",
					digestBase: "docker.io/alpine/helm@" + string(testDigest("ea0345")),
					sources:    []string{},
				},
			},
		},
		"recursive multi-stage file copy - mixed sources": {
			cf: containerfile.Containerfile{Stages: []containerfile.Stage{
				{
					Alias:   "builder1",
					Base:    "docker.io/library/fedora:latest",
					BaseRef: "docker.io/library/fedora:latest",
					Index:   0,
					Copies:  []containerfile.Copy{},
				},
				{
					Alias:   "builder2",
					Base:    "docker.io/alpine/helm:latest",
					BaseRef: "docker.io/alpine/helm:latest",
					Index:   1,
					Copies: []containerfile.Copy{
						{
							From:        "builder1",
							Sources:     []string{"/usr/bin/oras"},
							Destination: "/usr/bin/oras",
							Type:        containerfile.CopyTypeBuilder,
						},
					},
				},
				{
					Alias:   containerfile.FinalStage,
					Base:    "scratch",
					BaseRef: "scratch",
					Index:   -1,
					Copies: []containerfile.Copy{
						{
							From:        "builder2",
							Sources:     []string{"/usr/bin/oras", "/usr/bin/helm"},
							Destination: "/app/",
							Type:        containerfile.CopyTypeBuilder,
						},
					},
				},
			}},
			digests: map[string]digest.Digest{
				"docker.io/library/fedora:latest": testDigest("fa0678"),
				"docker.io/alpine/helm:latest":    testDigest("5a0901"),
			},
			configs: map[string]storageclient.OCIImageConfig{
				"docker.io/library/fedora:latest": configWithWorkdir("/"),
				"docker.io/alpine/helm:latest":    configWithWorkdir("/"),
			},
			expectedRoots: []packageSource{
				{
					index:      0,
					alias:      "builder1",
					pullspec:   "docker.io/library/fedora:latest",
					digestBase: "docker.io/library/fedora@" + string(testDigest("fa0678")),
					sources:    []string{"/usr/bin/oras"},
				},
				{
					index:      1,
					alias:      "builder2",
					pullspec:   "docker.io/alpine/helm:latest",
					digestBase: "docker.io/alpine/helm@" + string(testDigest("5a0901")),
					sources:    []string{"/usr/bin/helm"},
				},
			},
		},
		"multi-stage directory copy": {
			cf: containerfile.Containerfile{Stages: []containerfile.Stage{
				{
					Alias:   "builder1",
					Base:    "docker.io/library/fedora:latest",
					BaseRef: "docker.io/library/fedora:latest",
					Index:   0,
					Copies:  []containerfile.Copy{},
				},
				{
					Alias:   "builder2",
					Base:    "docker.io/alpine/helm:latest",
					BaseRef: "docker.io/alpine/helm:latest",
					Index:   1,
					Copies: []containerfile.Copy{
						{
							From:        "builder1",
							Sources:     []string{"/usr/bin/oras"},
							Destination: "/app/oras",
							Type:        containerfile.CopyTypeBuilder,
						},
						{
							From:        "builder1",
							Sources:     []string{"/bin/*"},
							Destination: "/app/",
							Type:        containerfile.CopyTypeBuilder,
						},
					},
				},
				{
					Alias:   containerfile.FinalStage,
					Base:    "scratch",
					BaseRef: "scratch",
					Index:   -1,
					Copies: []containerfile.Copy{
						{
							From:        "builder2",
							Sources:     []string{"/app/"},
							Destination: "/app/",
							Type:        containerfile.CopyTypeBuilder,
						},
					},
				},
			}},
			digests: map[string]digest.Digest{
				"docker.io/library/fedora:latest": testDigest("ba0234"),
				"docker.io/alpine/helm:latest":    testDigest("0a0567"),
			},
			configs: map[string]storageclient.OCIImageConfig{
				"docker.io/library/fedora:latest": configWithWorkdir("/"),
				"docker.io/alpine/helm:latest":    configWithWorkdir("/"),
			},
			expectedRoots: []packageSource{
				{
					index:      0,
					alias:      "builder1",
					pullspec:   "docker.io/library/fedora:latest",
					digestBase: "docker.io/library/fedora@" + string(testDigest("ba0234")),
					sources:    []string{"/usr/bin/oras", "/bin/*"},
				},
				{
					index:      1,
					alias:      "builder2",
					pullspec:   "docker.io/alpine/helm:latest",
					digestBase: "docker.io/alpine/helm@" + string(testDigest("0a0567")),
					sources:    []string{"/app/"},
				},
			},
		},
		"ignore non-copied content": {
			cf: containerfile.Containerfile{Stages: []containerfile.Stage{
				{
					Alias:   "builder1",
					Base:    "docker.io/library/fedora:latest",
					BaseRef: "docker.io/library/fedora:latest",
					Index:   0,
					Copies:  []containerfile.Copy{},
				},
				{
					Alias:   "builder2",
					Base:    "docker.io/alpine/helm:latest",
					BaseRef: "docker.io/alpine/helm:latest",
					Index:   1,
					Copies: []containerfile.Copy{
						{
							From:        "builder1",
							Sources:     []string{"/usr/bin/wget"},
							Destination: "/usr/bin/wget",
							Type:        containerfile.CopyTypeBuilder,
						},
					},
				},
				{
					Alias:   containerfile.FinalStage,
					Base:    "scratch",
					BaseRef: "scratch",
					Index:   -1,
					Copies: []containerfile.Copy{
						{
							From:        "builder2",
							Sources:     []string{"/app/"},
							Destination: "/app/",
							Type:        containerfile.CopyTypeBuilder,
						},
					},
				},
			}},
			digests: map[string]digest.Digest{
				"docker.io/library/fedora:latest": testDigest("bcd890"),
				"docker.io/alpine/helm:latest":    testDigest("ef0123"),
			},
			configs: map[string]storageclient.OCIImageConfig{
				"docker.io/library/fedora:latest": configWithWorkdir("/"),
				"docker.io/alpine/helm:latest":    configWithWorkdir("/"),
			},
			expectedRoots: []packageSource{
				{
					index:      0,
					alias:      "builder1",
					pullspec:   "docker.io/library/fedora:latest",
					digestBase: "docker.io/library/fedora@" + string(testDigest("bcd890")),
					sources:    []string{},
				},
				{
					index:      1,
					alias:      "builder2",
					pullspec:   "docker.io/alpine/helm:latest",
					digestBase: "docker.io/alpine/helm@" + string(testDigest("ef0123")),
					sources:    []string{"/app/"},
				},
			},
		},
		"complex multi-stage with multiple final copies": {
			cf: containerfile.Containerfile{Stages: []containerfile.Stage{
				{
					Alias:   "builder1",
					Base:    "docker.io/library/fedora:latest",
					BaseRef: "docker.io/library/fedora:latest",
					Index:   0,
					Copies:  []containerfile.Copy{},
				},
				{
					Alias:   "builder2",
					Base:    "docker.io/alpine/helm:latest",
					BaseRef: "docker.io/alpine/helm:latest",
					Index:   1,
					Copies: []containerfile.Copy{
						{
							From:        "builder1",
							Sources:     []string{"/usr/bin/kubectl"},
							Destination: "/tools/kubectl",
							Type:        containerfile.CopyTypeBuilder,
						},
					},
				},
				{
					Alias:   containerfile.FinalStage,
					Base:    "scratch",
					BaseRef: "scratch",
					Index:   -1,
					Copies: []containerfile.Copy{
						{
							From:        "builder1",
							Sources:     []string{"/lib/libc.so"},
							Destination: "/lib/libc.so",
							Type:        containerfile.CopyTypeBuilder,
						},
						{
							From:        "builder2",
							Sources:     []string{"/tools/"},
							Destination: "/usr/bin/",
							Type:        containerfile.CopyTypeBuilder,
						},
						{
							From:        "builder2",
							Sources:     []string{"/usr/bin/helm"},
							Destination: "/usr/bin/helm",
							Type:        containerfile.CopyTypeBuilder,
						},
					},
				},
			}},
			digests: map[string]digest.Digest{
				"docker.io/library/fedora:latest": testDigest("a1f456"),
				"docker.io/alpine/helm:latest":    testDigest("b2f789"),
			},
			configs: map[string]storageclient.OCIImageConfig{
				"docker.io/library/fedora:latest": configWithWorkdir("/"),
				"docker.io/alpine/helm:latest":    configWithWorkdir("/"),
			},
			expectedRoots: []packageSource{
				{
					index:      0,
					alias:      "builder1",
					pullspec:   "docker.io/library/fedora:latest",
					digestBase: "docker.io/library/fedora@" + string(testDigest("a1f456")),
					sources:    []string{"/lib/libc.so", "/usr/bin/kubectl"},
				},
				{
					index:      1,
					alias:      "builder2",
					pullspec:   "docker.io/alpine/helm:latest",
					digestBase: "docker.io/alpine/helm@" + string(testDigest("b2f789")),
					sources:    []string{"/tools/", "/usr/bin/helm"},
				},
			},
		},
		"wildcard copy in final stage": {
			cf: containerfile.Containerfile{Stages: []containerfile.Stage{
				{
					Alias:   "builder",
					Base:    "docker.io/library/fedora:latest",
					BaseRef: "docker.io/library/fedora:latest",
					Index:   0,
					Copies:  []containerfile.Copy{},
				},
				{
					Alias:   containerfile.FinalStage,
					Base:    "scratch",
					BaseRef: "scratch",
					Index:   -1,
					Copies: []containerfile.Copy{
						{
							From:        "builder",
							Sources:     []string{"/lib/*.so"},
							Destination: "/lib/",
						},
					},
				},
			}},
			digests: map[string]digest.Digest{
				"docker.io/library/fedora:latest": testDigest("a1f456"),
			},
			configs: map[string]storageclient.OCIImageConfig{
				"docker.io/library/fedora:latest": configWithWorkdir("/"),
			},
			expectedRoots: []packageSource{
				{
					index:      0,
					alias:      "builder",
					pullspec:   "docker.io/library/fedora:latest",
					digestBase: "docker.io/library/fedora@" + string(testDigest("a1f456")),
					sources:    []string{"/lib/*.so"},
				},
			},
		},
		"wildcard traced through multiple stages": {
			cf: containerfile.Containerfile{Stages: []containerfile.Stage{
				{
					Alias:   "builder1",
					Base:    "docker.io/library/fedora:latest",
					BaseRef: "docker.io/library/fedora:latest",
					Index:   0,
					Copies:  []containerfile.Copy{},
				},
				{
					Alias:   "builder2",
					Base:    "docker.io/alpine/helm:latest",
					BaseRef: "docker.io/alpine/helm:latest",
					Index:   1,
					Copies: []containerfile.Copy{
						{
							From:        "builder1",
							Sources:     []string{"/usr/lib/*.so"},
							Destination: "/libs/",
						},
					},
				},
				{
					Alias:   containerfile.FinalStage,
					Base:    "scratch",
					BaseRef: "scratch",
					Index:   -1,
					Copies: []containerfile.Copy{
						{
							From:        "builder2",
							Sources:     []string{"/libs/*.so"},
							Destination: "/lib/",
						},
					},
				},
			}},
			digests: map[string]digest.Digest{
				"docker.io/library/fedora:latest": testDigest("a1f456"),
				"docker.io/alpine/helm:latest":    testDigest("abcdef"),
			},
			configs: map[string]storageclient.OCIImageConfig{
				"docker.io/library/fedora:latest": configWithWorkdir("/"),
				"docker.io/alpine/helm:latest":    configWithWorkdir("/"),
			},
			expectedRoots: []packageSource{
				{
					index:      0,
					alias:      "builder1",
					pullspec:   "docker.io/library/fedora:latest",
					digestBase: "docker.io/library/fedora@" + string(testDigest("a1f456")),
					sources:    []string{"/usr/lib/*.so"},
				},
				{
					index:      1,
					alias:      "builder2",
					pullspec:   "docker.io/alpine/helm:latest",
					digestBase: "docker.io/alpine/helm@" + string(testDigest("abcdef")),
					sources:    []string{"/libs/*.so"},
				},
			},
		},
		"mixed wildcards and regular files": {
			cf: containerfile.Containerfile{Stages: []containerfile.Stage{
				{
					Alias:   "builder",
					Base:    "docker.io/library/fedora:latest",
					BaseRef: "docker.io/library/fedora:latest",
					Index:   0,
					Copies:  []containerfile.Copy{},
				},
				{
					Alias:   containerfile.FinalStage,
					Base:    "scratch",
					BaseRef: "scratch",
					Index:   -1,
					Copies: []containerfile.Copy{
						{
							From:        "builder",
							Sources:     []string{"/usr/bin/helm", "/lib/*.so", "/etc/config.txt"},
							Destination: "/app/",
						},
					},
				},
			}},
			digests: map[string]digest.Digest{
				"docker.io/library/fedora:latest": testDigest("a1f456"),
			},
			configs: map[string]storageclient.OCIImageConfig{
				"docker.io/library/fedora:latest": configWithWorkdir("/"),
			},
			expectedRoots: []packageSource{
				{
					index:      0,
					alias:      "builder",
					pullspec:   "docker.io/library/fedora:latest",
					digestBase: "docker.io/library/fedora@" + string(testDigest("a1f456")),
					sources:    []string{"/usr/bin/helm", "/lib/*.so", "/etc/config.txt"},
				},
			},
		},
		"relative destination resolution": {
			cf: containerfile.Containerfile{Stages: []containerfile.Stage{
				{
					Alias:   "builder1",
					Base:    "docker.io/library/fedora:latest",
					BaseRef: "docker.io/library/fedora:latest",
					Index:   0,
					Copies:  []containerfile.Copy{},
				},
				{
					Alias:   "builder2",
					Base:    "docker.io/library/node:latest",
					BaseRef: "docker.io/library/node:latest",
					Index:   1,
					Copies: []containerfile.Copy{
						// explicit workdir takes precedence over base image workdir (/var/data)
						{
							From:        "builder1",
							Sources:     []string{"/usr/bin/app"},
							Destination: "bin/app",
							Type:        containerfile.CopyTypeBuilder,
							Workdir:     "/usr/local",
						},
						// base image workdir resolves relative destination
						{
							From:        "builder1",
							Sources:     []string{"/usr/bin/tool"},
							Destination: "tool",
							Type:        containerfile.CopyTypeBuilder,
						},
					},
				},
				{
					Alias:   containerfile.FinalStage,
					Base:    "scratch",
					BaseRef: "scratch",
					Index:   -1,
					Copies: []containerfile.Copy{
						{
							From:        "builder2",
							Sources:     []string{"/usr/local/bin/app"},
							Destination: "/usr/local/bin/app",
							Type:        containerfile.CopyTypeBuilder,
						},
						{
							From:        "builder2",
							Sources:     []string{"/var/data/tool"},
							Destination: "/var/data/tool",
							Type:        containerfile.CopyTypeBuilder,
						},
					},
				},
			}},
			digests: map[string]digest.Digest{
				"docker.io/library/fedora:latest": testDigest("aaa111"),
				"docker.io/library/node:latest":   testDigest("bbb222"),
			},
			configs: map[string]storageclient.OCIImageConfig{
				"docker.io/library/fedora:latest": configWithWorkdir("/"),
				"docker.io/library/node:latest":   configWithWorkdir("/var/data"),
			},
			expectedRoots: []packageSource{
				{
					index:      0,
					alias:      "builder1",
					pullspec:   "docker.io/library/fedora:latest",
					digestBase: "docker.io/library/fedora@" + string(testDigest("aaa111")),
					sources:    []string{"/usr/bin/app", "/usr/bin/tool"},
				},
				{
					index:      1,
					alias:      "builder2",
					pullspec:   "docker.io/library/node:latest",
					digestBase: "docker.io/library/node@" + string(testDigest("bbb222")),
					sources:    []string{},
				},
			},
		},
		"mixed destinations with workdir variants": {
			cf: containerfile.Containerfile{Stages: []containerfile.Stage{
				{
					Alias:   "builder1",
					Base:    "docker.io/library/fedora:latest",
					BaseRef: "docker.io/library/fedora:latest",
					Index:   0,
					Copies:  []containerfile.Copy{},
				},
				{
					Alias:   "builder2",
					Base:    "docker.io/library/alpine:latest",
					BaseRef: "docker.io/library/alpine:latest",
					Index:   1,
					Copies: []containerfile.Copy{
						// absolute destination (no workdir resolution)
						{
							From:        "builder1",
							Sources:     []string{"/usr/bin/cli"},
							Destination: "/usr/local/bin/cli",
							Type:        containerfile.CopyTypeBuilder,
						},
						// parent directory navigation
						{
							From:        "builder1",
							Sources:     []string{"/etc/app.conf"},
							Destination: "../config/app.conf",
							Type:        containerfile.CopyTypeBuilder,
							Workdir:     "/app/subdir",
						},
						// dot destination resolves to workdir
						{
							From:        "builder1",
							Sources:     []string{"/src/app"},
							Destination: ".",
							Type:        containerfile.CopyTypeBuilder,
							Workdir:     "/opt",
						},
						// workdir switching within stage
						{
							From:        "builder1",
							Sources:     []string{"/etc/defaults.conf"},
							Destination: "config.yaml",
							Type:        containerfile.CopyTypeBuilder,
							Workdir:     "/etc/myapp",
						},
					},
				},
				{
					Alias:   containerfile.FinalStage,
					Base:    "scratch",
					BaseRef: "scratch",
					Index:   -1,
					Copies: []containerfile.Copy{
						{
							From:        "builder2",
							Sources:     []string{"/usr/local/bin/cli"},
							Destination: "/usr/local/bin/cli",
							Type:        containerfile.CopyTypeBuilder,
						},
						{
							From:        "builder2",
							Sources:     []string{"/app/config/app.conf"},
							Destination: "/app/config/app.conf",
							Type:        containerfile.CopyTypeBuilder,
						},
						{
							From:        "builder2",
							Sources:     []string{"/opt/app"},
							Destination: "/opt/app",
							Type:        containerfile.CopyTypeBuilder,
						},
						{
							From:        "builder2",
							Sources:     []string{"/etc/myapp/config.yaml"},
							Destination: "/etc/myapp/config.yaml",
							Type:        containerfile.CopyTypeBuilder,
						},
					},
				},
			}},
			digests: map[string]digest.Digest{
				"docker.io/library/fedora:latest": testDigest("ccc333"),
				"docker.io/library/alpine:latest": testDigest("ddd444"),
			},
			configs: map[string]storageclient.OCIImageConfig{
				"docker.io/library/fedora:latest": configWithWorkdir("/"),
				"docker.io/library/alpine:latest": configWithWorkdir("/"),
			},
			expectedRoots: []packageSource{
				{
					index:      0,
					alias:      "builder1",
					pullspec:   "docker.io/library/fedora:latest",
					digestBase: "docker.io/library/fedora@" + string(testDigest("ccc333")),
					sources:    []string{"/usr/bin/cli", "/etc/app.conf", "/src/app", "/etc/defaults.conf"},
				},
				{
					index:      1,
					alias:      "builder2",
					pullspec:   "docker.io/library/alpine:latest",
					digestBase: "docker.io/library/alpine@" + string(testDigest("ddd444")),
					sources:    []string{},
				},
			},
		},
		"multi-stage tracing with workdir in intermediate stage": {
			cf: containerfile.Containerfile{Stages: []containerfile.Stage{
				{
					Alias:   "builder1",
					Base:    "docker.io/library/fedora:latest",
					BaseRef: "docker.io/library/fedora:latest",
					Index:   0,
					Copies:  []containerfile.Copy{},
				},
				{
					Alias:   "builder2",
					Base:    "docker.io/library/alpine:latest",
					BaseRef: "docker.io/library/alpine:latest",
					Index:   1,
					Copies: []containerfile.Copy{
						{
							From:        "builder1",
							Sources:     []string{"/usr/bin/app"},
							Destination: "app",
							Type:        containerfile.CopyTypeBuilder,
							Workdir:     "/opt",
						},
					},
				},
				{
					Alias:   "builder3",
					Base:    "docker.io/library/ubuntu:latest",
					BaseRef: "docker.io/library/ubuntu:latest",
					Index:   2,
					Copies: []containerfile.Copy{
						{
							From:        "builder2",
							Sources:     []string{"/opt/app"},
							Destination: "/opt/app",
							Type:        containerfile.CopyTypeBuilder,
						},
					},
				},
				{
					Alias:   containerfile.FinalStage,
					Base:    "scratch",
					BaseRef: "scratch",
					Index:   -1,
					Copies: []containerfile.Copy{
						{
							From:        "builder3",
							Sources:     []string{"/opt/app"},
							Destination: "/opt/app",
							Type:        containerfile.CopyTypeBuilder,
						},
					},
				},
			}},
			digests: map[string]digest.Digest{
				"docker.io/library/fedora:latest": testDigest("111999"),
				"docker.io/library/alpine:latest": testDigest("222000"),
				"docker.io/library/ubuntu:latest": testDigest("333111"),
			},
			configs: map[string]storageclient.OCIImageConfig{
				"docker.io/library/fedora:latest": configWithWorkdir("/"),
				"docker.io/library/alpine:latest": configWithWorkdir("/"),
				"docker.io/library/ubuntu:latest": configWithWorkdir("/"),
			},
			expectedRoots: []packageSource{
				{
					index:      0,
					alias:      "builder1",
					pullspec:   "docker.io/library/fedora:latest",
					digestBase: "docker.io/library/fedora@" + string(testDigest("111999")),
					sources:    []string{"/usr/bin/app"},
				},
				{
					index:      1,
					alias:      "builder2",
					pullspec:   "docker.io/library/alpine:latest",
					digestBase: "docker.io/library/alpine@" + string(testDigest("222000")),
					sources:    []string{},
				},
				{
					index:      2,
					alias:      "builder3",
					pullspec:   "docker.io/library/ubuntu:latest",
					digestBase: "docker.io/library/ubuntu@" + string(testDigest("333111")),
					sources:    []string{},
				},
			},
		},
		"different stages with different base image workdirs": {
			cf: containerfile.Containerfile{Stages: []containerfile.Stage{
				{
					Alias:   "go-builder",
					Base:    "docker.io/library/golang:latest",
					BaseRef: "docker.io/library/golang:latest",
					Index:   0,
					Copies:  []containerfile.Copy{},
				},
				{
					Alias:   "node-builder",
					Base:    "docker.io/library/node:latest",
					BaseRef: "docker.io/library/node:latest",
					Index:   1,
					Copies:  []containerfile.Copy{},
				},
				{
					Alias:   "go-runner",
					Base:    "registry.example.com/go-runtime:v1",
					BaseRef: "registry.example.com/go-runtime:v1",
					Index:   2,
					Copies: []containerfile.Copy{
						{
							From:        "go-builder",
							Sources:     []string{"/go/bin/server"},
							Destination: "server",
							Type:        containerfile.CopyTypeBuilder,
						},
					},
				},
				{
					Alias:   "node-runner",
					Base:    "registry.example.com/nginx:v1",
					BaseRef: "registry.example.com/nginx:v1",
					Index:   3,
					Copies: []containerfile.Copy{
						{
							From:        "node-builder",
							Sources:     []string{"/home/node/dist/bundle.js"},
							Destination: "bundle.js",
							Type:        containerfile.CopyTypeBuilder,
						},
					},
				},
				{
					Alias:   containerfile.FinalStage,
					Base:    "scratch",
					BaseRef: "scratch",
					Index:   -1,
					Copies: []containerfile.Copy{
						{
							From:        "go-runner",
							Sources:     []string{"/app/server"},
							Destination: "/app/server",
							Type:        containerfile.CopyTypeBuilder,
						},
						{
							From:        "node-runner",
							Sources:     []string{"/usr/share/nginx/html/bundle.js"},
							Destination: "/usr/share/nginx/html/bundle.js",
							Type:        containerfile.CopyTypeBuilder,
						},
					},
				},
			}},
			digests: map[string]digest.Digest{
				"docker.io/library/golang:latest":    testDigest("444222"),
				"docker.io/library/node:latest":      testDigest("555333"),
				"registry.example.com/go-runtime:v1": testDigest("666444"),
				"registry.example.com/nginx:v1":      testDigest("777555"),
			},
			configs: map[string]storageclient.OCIImageConfig{
				"docker.io/library/golang:latest":    configWithWorkdir("/go"),
				"docker.io/library/node:latest":      configWithWorkdir("/home/node"),
				"registry.example.com/go-runtime:v1": configWithWorkdir("/app"),
				"registry.example.com/nginx:v1":      configWithWorkdir("/usr/share/nginx/html"),
			},
			expectedRoots: []packageSource{
				{
					index:      0,
					alias:      "go-builder",
					pullspec:   "docker.io/library/golang:latest",
					digestBase: "docker.io/library/golang@" + string(testDigest("444222")),
					sources:    []string{"/go/bin/server"},
				},
				{
					index:      1,
					alias:      "node-builder",
					pullspec:   "docker.io/library/node:latest",
					digestBase: "docker.io/library/node@" + string(testDigest("555333")),
					sources:    []string{"/home/node/dist/bundle.js"},
				},
				{
					index:      2,
					alias:      "go-runner",
					pullspec:   "registry.example.com/go-runtime:v1",
					digestBase: "registry.example.com/go-runtime@" + string(testDigest("666444")),
					sources:    []string{},
				},
				{
					index:      3,
					alias:      "node-runner",
					pullspec:   "registry.example.com/nginx:v1",
					digestBase: "registry.example.com/nginx@" + string(testDigest("777555")),
					sources:    []string{},
				},
			},
		},
		"numeric index COPY --from in final stage with aliased stages": {
			cf: containerfile.Containerfile{Stages: []containerfile.Stage{
				{
					Alias:   "builder",
					Base:    "docker.io/library/fedora:latest",
					BaseRef: "docker.io/library/fedora:latest",
					Index:   0,
					Copies:  []containerfile.Copy{},
				},
				{
					Alias:   containerfile.FinalStage,
					Base:    "scratch",
					BaseRef: "scratch",
					Index:   -1,
					Copies: []containerfile.Copy{
						{
							From:        "0",
							Sources:     []string{"/usr/bin/app"},
							Destination: "/usr/bin/app",
							Type:        containerfile.CopyTypeBuilder,
						},
					},
				},
			}},
			digests: map[string]digest.Digest{
				"docker.io/library/fedora:latest": testDigest("aaa111"),
			},
			configs: map[string]storageclient.OCIImageConfig{
				"docker.io/library/fedora:latest": configWithWorkdir("/"),
			},
			expectedRoots: []packageSource{
				{
					index:      0,
					alias:      "builder",
					pullspec:   "docker.io/library/fedora:latest",
					digestBase: "docker.io/library/fedora@" + string(testDigest("aaa111")),
					sources:    []string{"/usr/bin/app"},
				},
			},
		},
		"numeric index COPY --from in builder stage with aliased stages": {
			cf: containerfile.Containerfile{Stages: []containerfile.Stage{
				{
					Alias:   "builder1",
					Base:    "docker.io/library/fedora:latest",
					BaseRef: "docker.io/library/fedora:latest",
					Index:   0,
					Copies:  []containerfile.Copy{},
				},
				{
					Alias:   "builder2",
					Base:    "docker.io/alpine/helm:latest",
					BaseRef: "docker.io/alpine/helm:latest",
					Index:   1,
					Copies: []containerfile.Copy{
						{
							From:        "0",
							Sources:     []string{"/content"},
							Destination: "/forwarded",
							Type:        containerfile.CopyTypeBuilder,
						},
					},
				},
				{
					Alias:   containerfile.FinalStage,
					Base:    "scratch",
					BaseRef: "scratch",
					Index:   -1,
					Copies: []containerfile.Copy{
						{
							From:        "builder2",
							Sources:     []string{"/forwarded"},
							Destination: "/forwarded",
							Type:        containerfile.CopyTypeBuilder,
						},
					},
				},
			}},
			digests: map[string]digest.Digest{
				"docker.io/library/fedora:latest": testDigest("bbb222"),
				"docker.io/alpine/helm:latest":    testDigest("ccc333"),
			},
			configs: map[string]storageclient.OCIImageConfig{
				"docker.io/library/fedora:latest": configWithWorkdir("/"),
				"docker.io/alpine/helm:latest":    configWithWorkdir("/"),
			},
			expectedRoots: []packageSource{
				{
					index:      0,
					alias:      "builder1",
					pullspec:   "docker.io/library/fedora:latest",
					digestBase: "docker.io/library/fedora@" + string(testDigest("bbb222")),
					sources:    []string{"/content"},
				},
				{
					index:      1,
					alias:      "builder2",
					pullspec:   "docker.io/alpine/helm:latest",
					digestBase: "docker.io/alpine/helm@" + string(testDigest("ccc333")),
					sources:    []string{},
				},
			},
		},
		"chained stages - parent and child cascade": {
			// FROM fedora AS parent    (non-chained, index 0)
			// FROM parent AS child     (chained, index 1, BaseRef=parent)
			// FROM scratch             (final, copies from child)
			cf: containerfile.Containerfile{Stages: []containerfile.Stage{
				{
					Alias:   "parent",
					Base:    "docker.io/library/fedora:latest",
					BaseRef: "docker.io/library/fedora:latest",
					Index:   0,
					Copies:  []containerfile.Copy{},
				},
				{
					Alias:   "child",
					Base:    "docker.io/library/fedora:latest",
					BaseRef: "parent",
					Index:   1,
					Copies:  []containerfile.Copy{},
				},
				{
					Alias:   containerfile.FinalStage,
					Base:    "scratch",
					BaseRef: "scratch",
					Index:   -1,
					Copies: []containerfile.Copy{
						{
							From:        "child",
							Sources:     []string{"/app/bin"},
							Destination: "/app/bin",
							Type:        containerfile.CopyTypeBuilder,
						},
					},
				},
			}},
			digests: map[string]digest.Digest{
				"docker.io/library/fedora:latest": testDigest("aa1111"),
			},
			configs: map[string]storageclient.OCIImageConfig{
				"docker.io/library/fedora:latest": configWithWorkdir("/"),
			},
			expectedRoots: []packageSource{
				{
					index:      0,
					alias:      "parent",
					pullspec:   "docker.io/library/fedora:latest",
					digestBase: "docker.io/library/fedora@" + string(testDigest("aa1111")),
					sources:    []string{"/app/bin"},
					descendants: []*packageSourceDescendant{
						{
							index:   1,
							alias:   "child",
							sources: []string{"/app/bin"},
						},
					},
				},
			},
		},
		"chained stages - empty child skipped": {
			// FROM fedora AS parent        (non-chained, index 0)
			// FROM parent AS empty-child   (chained, index 1, BaseRef=parent, no intermediate expected)
			// FROM scratch                 (final, copies from empty-child)
			cf: containerfile.Containerfile{Stages: []containerfile.Stage{
				{
					Alias:   "parent",
					Base:    "docker.io/library/fedora:latest",
					BaseRef: "docker.io/library/fedora:latest",
					Index:   0,
					Copies:  []containerfile.Copy{},
				},
				{
					Alias:   "empty-child",
					Base:    "docker.io/library/fedora:latest",
					BaseRef: "parent",
					Index:   1,
					Copies:  []containerfile.Copy{},
				},
				{
					Alias:   containerfile.FinalStage,
					Base:    "scratch",
					BaseRef: "scratch",
					Index:   -1,
					Copies: []containerfile.Copy{
						{
							From:        "empty-child",
							Sources:     []string{"/usr/bin/tool"},
							Destination: "/usr/bin/tool",
							Type:        containerfile.CopyTypeBuilder,
						},
					},
				},
			}},
			digests: map[string]digest.Digest{
				"docker.io/library/fedora:latest": testDigest("bb2222"),
			},
			configs: map[string]storageclient.OCIImageConfig{
				"docker.io/library/fedora:latest": configWithWorkdir("/"),
			},
			expectedRoots: []packageSource{
				{
					index:      0,
					alias:      "parent",
					pullspec:   "docker.io/library/fedora:latest",
					digestBase: "docker.io/library/fedora@" + string(testDigest("bb2222")),
					sources:    []string{"/usr/bin/tool"},
					descendants: []*packageSourceDescendant{
						{
							index:   1,
							alias:   "empty-child",
							sources: []string{"/usr/bin/tool"},
						},
					},
				},
			},
		},
		"chained stages - diamond dependency": {
			// FROM fedora AS shared   (non-chained, index 0)
			// FROM shared AS left     (chained, index 1, BaseRef=shared)
			// FROM shared AS right    (chained, index 2, BaseRef=shared)
			// FROM scratch            (final, copies from both left and right)
			cf: containerfile.Containerfile{Stages: []containerfile.Stage{
				{
					Alias:   "shared",
					Base:    "docker.io/library/fedora:latest",
					BaseRef: "docker.io/library/fedora:latest",
					Index:   0,
					Copies:  []containerfile.Copy{},
				},
				{
					Alias:   "left",
					Base:    "docker.io/library/fedora:latest",
					BaseRef: "shared",
					Index:   1,
					Copies:  []containerfile.Copy{},
				},
				{
					Alias:   "right",
					Base:    "docker.io/library/fedora:latest",
					BaseRef: "shared",
					Index:   2,
					Copies:  []containerfile.Copy{},
				},
				{
					Alias:   containerfile.FinalStage,
					Base:    "scratch",
					BaseRef: "scratch",
					Index:   -1,
					Copies: []containerfile.Copy{
						{
							From:        "left",
							Sources:     []string{"/left/bin"},
							Destination: "/left/bin",
							Type:        containerfile.CopyTypeBuilder,
						},
						{
							From:        "right",
							Sources:     []string{"/right/bin"},
							Destination: "/right/bin",
							Type:        containerfile.CopyTypeBuilder,
						},
					},
				},
			}},
			digests: map[string]digest.Digest{
				"docker.io/library/fedora:latest": testDigest("cc3333"),
			},
			configs: map[string]storageclient.OCIImageConfig{
				"docker.io/library/fedora:latest": configWithWorkdir("/"),
			},
			expectedRoots: []packageSource{
				{
					index:      0,
					alias:      "shared",
					pullspec:   "docker.io/library/fedora:latest",
					digestBase: "docker.io/library/fedora@" + string(testDigest("cc3333")),
					sources:    []string{"/left/bin", "/right/bin"},
					descendants: []*packageSourceDescendant{
						{
							index:   1,
							alias:   "left",
							sources: []string{"/left/bin"},
						},
						{
							index:   2,
							alias:   "right",
							sources: []string{"/right/bin"},
						},
					},
				},
			},
		},
		"external COPY --from in builder stage": {
			cf: containerfile.Containerfile{Stages: []containerfile.Stage{
				{
					Alias:   "builder",
					Base:    "docker.io/library/fedora:latest",
					BaseRef: "docker.io/library/fedora:latest",
					Index:   0,
					Copies: []containerfile.Copy{
						{
							From:        "docker.io/library/external:latest",
							Sources:     []string{"/ext/bin"},
							Destination: "/ext/bin",
							Type:        containerfile.CopyTypeExternal,
						},
					},
				},
				{
					Alias:   containerfile.FinalStage,
					Base:    "scratch",
					BaseRef: "scratch",
					Index:   -1,
					Copies: []containerfile.Copy{
						{
							From:        "builder",
							Sources:     []string{"/app/", "/ext/bin"},
							Destination: "/",
							Type:        containerfile.CopyTypeBuilder,
						},
					},
				},
			}},
			digests: map[string]digest.Digest{
				"docker.io/library/fedora:latest":   testDigest("eee111"),
				"docker.io/library/external:latest": testDigest("fff222"),
			},
			configs: map[string]storageclient.OCIImageConfig{
				"docker.io/library/fedora:latest": configWithWorkdir("/"),
			},
			expectedRoots: []packageSource{
				{
					index:      0,
					alias:      "builder",
					pullspec:   "docker.io/library/fedora:latest",
					digestBase: "docker.io/library/fedora@" + string(testDigest("eee111")),
					sources:    []string{"/app/"},
				},
				{
					pullspec:   "docker.io/library/external:latest",
					digestBase: "docker.io/library/external@" + string(testDigest("fff222")),
					sources:    []string{"/ext/bin"},
					external:   true,
				},
			},
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			client := testutils.NewTStorageClient(
				test.digests, test.configs,
			)

			roots, err := getPackageSources(client, test.cf, test.digests)
			if err != nil {
				t.Fatalf("getPackageSources returned error: %v", err)
			}

			diff := cmp.Diff(
				test.expectedRoots, roots,
				cmp.AllowUnexported(packageSource{}, packageSourceDescendant{}),
				cmpopts.SortSlices(func(a, b packageSource) bool {
					if a.external != b.external {
						return !a.external
					}
					return a.index < b.index
				}),
				cmpopts.EquateEmpty(),
			)
			if diff != "" {
				t.Errorf("getPackageSources() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestResolveRelativeDestination(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		cp          containerfile.Copy
		baseWorkdir string
		expected    string
	}{
		"no workdir": {
			cp:          containerfile.Copy{Destination: "app/"},
			baseWorkdir: "/root",
			expected:    "/root/app",
		},
		"absolute workdir": {
			cp:          containerfile.Copy{Workdir: "/usr/src", Destination: "dist/"},
			baseWorkdir: "/ignored",
			expected:    "/usr/src/dist",
		},
		"relative workdir": {
			cp:          containerfile.Copy{Workdir: "subdir", Destination: "files/"},
			baseWorkdir: "/base",
			expected:    "/base/subdir/files",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got := resolveRelativeDestination(test.cp, test.baseWorkdir)
			if got != test.expected {
				t.Errorf("resolveRelativeDestination() = %q, want %q", got, test.expected)
			}
		})
	}
}

func TestGetPackageSourcesError(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		cf          containerfile.Containerfile
		digests     map[string]digest.Digest
		configs     map[string]storageclient.OCIImageConfig
		expectedErr error
	}{
		"GetImageConfig error": {
			cf: containerfile.Containerfile{Stages: []containerfile.Stage{
				{
					Alias:   "builder",
					Base:    "docker.io/library/fedora:latest",
					BaseRef: "docker.io/library/fedora:latest",
					Index:   0,
					Copies:  []containerfile.Copy{},
				},
				{
					Alias:   containerfile.FinalStage,
					Base:    "docker.io/library/ubi9:latest",
					BaseRef: "docker.io/library/ubi9:latest",
					Index:   -1,
					Copies:  []containerfile.Copy{},
				},
			}},
			configs:     map[string]storageclient.OCIImageConfig{},
			expectedErr: ErrOCIConfig,
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			client := testutils.NewTStorageClient(
				test.digests, test.configs,
			)

			_, err := getPackageSources(client, test.cf, test.digests)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !errors.Is(err, test.expectedErr) {
				t.Fatalf("expected error wrapping %v, got: %v", test.expectedErr, err)
			}
		})
	}
}

func TestScanPreflightErrors(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		cf         containerfile.Containerfile
		expectErrs []error
		rejectErrs []error
	}{
		"builder alias used as final base": {
			cf: containerfile.Containerfile{Stages: []containerfile.Stage{
				{
					Alias:   "builder",
					Base:    "docker.io/library/golang:1.22",
					BaseRef: "docker.io/library/golang:1.22",
					Index:   0,
				},
				{
					Alias:   containerfile.FinalStage,
					Base:    "builder",
					BaseRef: "builder",
					Index:   -1,
				},
			}},
			expectErrs: []error{ErrUnsupportedFeature, ErrBuilderIsFinalBase},
			rejectErrs: []error{ErrDuplicateAlias, ErrMountTypeBind},
		},
		"builder referenced by numeric index as final base": {
			cf: containerfile.Containerfile{Stages: []containerfile.Stage{
				{
					Alias:   "builder",
					Base:    "docker.io/library/golang:1.22",
					BaseRef: "docker.io/library/golang:1.22",
					Index:   0,
				},
				{
					Alias:   containerfile.FinalStage,
					Base:    "0",
					BaseRef: "0",
					Index:   -1,
				},
			}},
			expectErrs: []error{ErrUnsupportedFeature, ErrBuilderIsFinalBase},
			rejectErrs: []error{ErrDuplicateAlias, ErrMountTypeBind},
		},
		"duplicate stage alias": {
			cf: containerfile.Containerfile{Stages: []containerfile.Stage{
				{
					Alias:   "builder",
					Base:    "docker.io/library/golang:1.22",
					BaseRef: "docker.io/library/golang:1.22",
					Index:   0,
				},
				{
					Alias:   "builder",
					Base:    "docker.io/library/node:20",
					BaseRef: "docker.io/library/node:20",
					Index:   1,
				},
				{
					Alias:   containerfile.FinalStage,
					Base:    "scratch",
					BaseRef: "scratch",
					Index:   -1,
				},
			}},
			expectErrs: []error{ErrUnsupportedFeature, ErrDuplicateAlias},
			rejectErrs: []error{ErrBuilderIsFinalBase, ErrMountTypeBind},
		},
		"bind mount with from": {
			cf: containerfile.Containerfile{Stages: []containerfile.Stage{
				{
					Alias:   "builder",
					Base:    "docker.io/library/golang:1.22",
					BaseRef: "docker.io/library/golang:1.22",
					Index:   0,
					Mounts: []containerfile.Mount{
						{MountType: containerfile.MountTypeBind, FromRaw: "docker.io/library/some-image:latest"},
					},
				},
				{
					Alias:   containerfile.FinalStage,
					Base:    "scratch",
					BaseRef: "scratch",
					Index:   -1,
				},
			}},
			expectErrs: []error{ErrUnsupportedFeature, ErrMountTypeBind},
			rejectErrs: []error{ErrBuilderIsFinalBase, ErrDuplicateAlias},
		},
		"multiple preflight errors": {
			cf: containerfile.Containerfile{Stages: []containerfile.Stage{
				{
					Alias:   "builder",
					Base:    "docker.io/library/golang:1.22",
					BaseRef: "docker.io/library/golang:1.22",
					Index:   0,
				},
				{
					Alias:   "builder",
					Base:    "docker.io/library/node:20",
					BaseRef: "docker.io/library/node:20",
					Index:   1,
				},
				{
					Alias:   containerfile.FinalStage,
					Base:    "builder",
					BaseRef: "builder",
					Index:   -1,
				},
			}},
			expectErrs: []error{ErrUnsupportedFeature, ErrBuilderIsFinalBase, ErrDuplicateAlias},
			rejectErrs: []error{ErrMountTypeBind},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			s := &Scanner{}
			_, err := s.Scan(tc.cf)

			for _, expected := range tc.expectErrs {
				if !errors.Is(err, expected) {
					t.Errorf("expected error wrapping %v, got: %v", expected, err)
				}
			}

			for _, rejected := range tc.rejectErrs {
				if errors.Is(err, rejected) {
					t.Errorf("unexpected error %v in: %v", rejected, err)
				}
			}
		})
	}
}

//go:build unit

package capo

import (
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/opencontainers/go-digest"

	"github.com/konflux-ci/capo/pkg/containerfile"
	"github.com/konflux-ci/capo/pkg/storageclient"

	"github.com/konflux-ci/capo/internal/testutils"
)

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
		stages            []containerfile.Stage
		resolvedPullspecs map[string]string
		configs           map[string]storageclient.OCIImageConfig
		expected          []packageSource
		expectedMountOnly []packageSource
	}{
		"only external copy in final": {
			stages: []containerfile.Stage{
				{
					Alias: containerfile.FinalStage,
					Base:  "scratch",
					Copies: []containerfile.Copy{
						{
							From:        "docker.io/library/fedora:latest",
							Sources:     []string{"/usr/bin/oras"},
							Destination: "/usr/bin/oras",
							Type:        containerfile.CopyTypeExternal,
						},
					},
				},
			},
			resolvedPullspecs: map[string]string{
				"docker.io/library/fedora:latest": "docker.io/library/fedora@sha256:abc123",
			},
			configs: map[string]storageclient.OCIImageConfig{
				"docker.io/library/fedora:latest": configWithWorkdir("/"),
			},
			expected: []packageSource{
				{
					alias:      "",
					pullspec:   "docker.io/library/fedora:latest",
					digestBase: "docker.io/library/fedora@sha256:abc123",
					sources:    []string{"/usr/bin/oras"},
				},
			},
		},
		"copies in final stage only": {
			stages: []containerfile.Stage{
				{
					Alias:  "builder1",
					Base:   "docker.io/library/fedora:latest",
					Copies: []containerfile.Copy{},
				},
				{
					Alias:  "builder2",
					Base:   "docker.io/alpine/helm:latest",
					Copies: []containerfile.Copy{},
				},
				{
					Alias: containerfile.FinalStage,
					Base:  "scratch",
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
			},
			resolvedPullspecs: map[string]string{
				"docker.io/library/fedora:latest": "docker.io/library/fedora@sha256:def456",
				"docker.io/alpine/helm:latest":    "docker.io/alpine/helm@sha256:ghi789",
			},
			configs: map[string]storageclient.OCIImageConfig{
				"docker.io/library/fedora:latest": configWithWorkdir("/"),
				"docker.io/alpine/helm:latest":    configWithWorkdir("/"),
			},
			expected: []packageSource{
				{
					alias:      "builder1",
					pullspec:   "docker.io/library/fedora:latest",
					digestBase: "docker.io/library/fedora@sha256:def456",
					sources:    []string{"/usr/bin/oras"},
				},
				{
					alias:      "builder2",
					pullspec:   "docker.io/alpine/helm:latest",
					digestBase: "docker.io/alpine/helm@sha256:ghi789",
					sources:    []string{"/usr/bin/helm"},
				},
			},
		},
		"recursive multi-stage file copy": {
			stages: []containerfile.Stage{
				{
					Alias:  "builder1",
					Base:   "docker.io/library/fedora:latest",
					Copies: []containerfile.Copy{},
				},
				{
					Alias: "builder2",
					Base:  "docker.io/alpine/helm:latest",
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
					Alias: containerfile.FinalStage,
					Base:  "scratch",
					Copies: []containerfile.Copy{
						{
							From:        "builder2",
							Sources:     []string{"/usr/bin/oras"},
							Destination: "/usr/bin/oras",
							Type:        containerfile.CopyTypeBuilder,
						},
					},
				},
			},
			resolvedPullspecs: map[string]string{
				"docker.io/library/fedora:latest": "docker.io/library/fedora@sha256:jkl012",
				"docker.io/alpine/helm:latest":    "docker.io/alpine/helm@sha256:mno345",
			},
			configs: map[string]storageclient.OCIImageConfig{
				"docker.io/library/fedora:latest": configWithWorkdir("/"),
				"docker.io/alpine/helm:latest":    configWithWorkdir("/"),
			},
			expected: []packageSource{
				{
					alias:      "builder1",
					pullspec:   "docker.io/library/fedora:latest",
					digestBase: "docker.io/library/fedora@sha256:jkl012",
					sources:    []string{"/usr/bin/oras"},
				},
				{
					alias:      "builder2",
					pullspec:   "docker.io/alpine/helm:latest",
					digestBase: "docker.io/alpine/helm@sha256:mno345",
					sources:    []string{},
				},
			},
		},
		"recursive multi-stage file copy - mixed sources": {
			stages: []containerfile.Stage{
				{
					Alias:  "builder1",
					Base:   "docker.io/library/fedora:latest",
					Copies: []containerfile.Copy{},
				},
				{
					Alias: "builder2",
					Base:  "docker.io/alpine/helm:latest",
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
					Alias: containerfile.FinalStage,
					Base:  "scratch",
					Copies: []containerfile.Copy{
						{
							From:        "builder2",
							Sources:     []string{"/usr/bin/oras", "/usr/bin/helm"},
							Destination: "/app/",
							Type:        containerfile.CopyTypeBuilder,
						},
					},
				},
			},
			resolvedPullspecs: map[string]string{
				"docker.io/library/fedora:latest": "docker.io/library/fedora@sha256:pqr678",
				"docker.io/alpine/helm:latest":    "docker.io/alpine/helm@sha256:stu901",
			},
			configs: map[string]storageclient.OCIImageConfig{
				"docker.io/library/fedora:latest": configWithWorkdir("/"),
				"docker.io/alpine/helm:latest":    configWithWorkdir("/"),
			},
			expected: []packageSource{
				{
					alias:      "builder1",
					pullspec:   "docker.io/library/fedora:latest",
					digestBase: "docker.io/library/fedora@sha256:pqr678",
					sources:    []string{"/usr/bin/oras"},
				},
				{
					alias:      "builder2",
					pullspec:   "docker.io/alpine/helm:latest",
					digestBase: "docker.io/alpine/helm@sha256:stu901",
					sources:    []string{"/usr/bin/helm"},
				},
			},
		},
		"multi-stage directory copy": {
			stages: []containerfile.Stage{
				{
					Alias:  "builder1",
					Base:   "docker.io/library/fedora:latest",
					Copies: []containerfile.Copy{},
				},
				{
					Alias: "builder2",
					Base:  "docker.io/alpine/helm:latest",
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
					Alias: containerfile.FinalStage,
					Base:  "scratch",
					Copies: []containerfile.Copy{
						{
							From:        "builder2",
							Sources:     []string{"/app/"},
							Destination: "/app/",
							Type:        containerfile.CopyTypeBuilder,
						},
					},
				},
			},
			resolvedPullspecs: map[string]string{
				"docker.io/library/fedora:latest": "docker.io/library/fedora@sha256:vwx234",
				"docker.io/alpine/helm:latest":    "docker.io/alpine/helm@sha256:yza567",
			},
			configs: map[string]storageclient.OCIImageConfig{
				"docker.io/library/fedora:latest": configWithWorkdir("/"),
				"docker.io/alpine/helm:latest":    configWithWorkdir("/"),
			},
			expected: []packageSource{
				{
					alias:      "builder1",
					pullspec:   "docker.io/library/fedora:latest",
					digestBase: "docker.io/library/fedora@sha256:vwx234",
					sources:    []string{"/usr/bin/oras", "/bin/*"},
				},
				{
					alias:      "builder2",
					pullspec:   "docker.io/alpine/helm:latest",
					digestBase: "docker.io/alpine/helm@sha256:yza567",
					sources:    []string{"/app/"},
				},
			},
		},
		"ignore non-copied content": {
			stages: []containerfile.Stage{
				{
					Alias:  "builder1",
					Base:   "docker.io/library/fedora:latest",
					Copies: []containerfile.Copy{},
				},
				{
					Alias: "builder2",
					Base:  "docker.io/alpine/helm:latest",
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
					Alias: containerfile.FinalStage,
					Base:  "scratch",
					Copies: []containerfile.Copy{
						{
							From:        "builder2",
							Sources:     []string{"/app/"},
							Destination: "/app/",
							Type:        containerfile.CopyTypeBuilder,
						},
					},
				},
			},
			resolvedPullspecs: map[string]string{
				"docker.io/library/fedora:latest": "docker.io/library/fedora@sha256:bcd890",
				"docker.io/alpine/helm:latest":    "docker.io/alpine/helm@sha256:efg123",
			},
			configs: map[string]storageclient.OCIImageConfig{
				"docker.io/library/fedora:latest": configWithWorkdir("/"),
				"docker.io/alpine/helm:latest":    configWithWorkdir("/"),
			},
			expected: []packageSource{
				{
					alias:      "builder1",
					pullspec:   "docker.io/library/fedora:latest",
					digestBase: "docker.io/library/fedora@sha256:bcd890",
					sources:    []string{},
				},
				{
					alias:      "builder2",
					pullspec:   "docker.io/alpine/helm:latest",
					digestBase: "docker.io/alpine/helm@sha256:efg123",
					sources:    []string{"/app/"},
				},
			},
		},
		"complex multi-stage with multiple final copies": {
			stages: []containerfile.Stage{
				{
					Alias:  "builder1",
					Base:   "docker.io/library/fedora:latest",
					Copies: []containerfile.Copy{},
				},
				{
					Alias: "builder2",
					Base:  "docker.io/alpine/helm:latest",
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
					Alias: containerfile.FinalStage,
					Base:  "scratch",
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
			},
			resolvedPullspecs: map[string]string{
				"docker.io/library/fedora:latest": "docker.io/library/fedora@sha256:hij456",
				"docker.io/alpine/helm:latest":    "docker.io/alpine/helm@sha256:klm789",
			},
			configs: map[string]storageclient.OCIImageConfig{
				"docker.io/library/fedora:latest": configWithWorkdir("/"),
				"docker.io/alpine/helm:latest":    configWithWorkdir("/"),
			},
			expected: []packageSource{
				{
					alias:      "builder1",
					pullspec:   "docker.io/library/fedora:latest",
					digestBase: "docker.io/library/fedora@sha256:hij456",
					sources:    []string{"/lib/libc.so", "/usr/bin/kubectl"},
				},
				{
					alias:      "builder2",
					pullspec:   "docker.io/alpine/helm:latest",
					digestBase: "docker.io/alpine/helm@sha256:klm789",
					sources:    []string{"/tools/", "/usr/bin/helm"},
				},
			},
		},
		"wildcard copy in final stage": {
			stages: []containerfile.Stage{
				{
					Alias:  "builder",
					Base:   "docker.io/library/fedora:latest",
					Copies: []containerfile.Copy{},
				},
				{
					Alias: containerfile.FinalStage,
					Base:  "scratch",
					Copies: []containerfile.Copy{
						{
							From:        "builder",
							Sources:     []string{"/lib/*.so"},
							Destination: "/lib/",
						},
					},
				},
			},
			resolvedPullspecs: map[string]string{
				"docker.io/library/fedora:latest": "docker.io/library/fedora@sha256:hij456",
			},
			configs: map[string]storageclient.OCIImageConfig{
				"docker.io/library/fedora:latest": configWithWorkdir("/"),
			},
			expected: []packageSource{
				{
					alias:      "builder",
					pullspec:   "docker.io/library/fedora:latest",
					digestBase: "docker.io/library/fedora@sha256:hij456",
					sources:    []string{"/lib/*.so"},
				},
			},
		},
		"wildcard traced through multiple stages": {
			stages: []containerfile.Stage{
				{
					Alias:  "builder1",
					Base:   "docker.io/library/fedora:latest",
					Copies: []containerfile.Copy{},
				},
				{
					Alias: "builder2",
					Base:  "docker.io/alpine/helm:latest",
					Copies: []containerfile.Copy{
						{
							From:        "builder1",
							Sources:     []string{"/usr/lib/*.so"},
							Destination: "/libs/",
						},
					},
				},
				{
					Alias: containerfile.FinalStage,
					Base:  "scratch",
					Copies: []containerfile.Copy{
						{
							From:        "builder2",
							Sources:     []string{"/libs/*.so"},
							Destination: "/lib/",
						},
					},
				},
			},
			resolvedPullspecs: map[string]string{
				"docker.io/library/fedora:latest": "docker.io/library/fedora@sha256:hij456",
				"docker.io/alpine/helm:latest":    "docker.io/library/alpine/helm@sha256:abcdef",
			},
			configs: map[string]storageclient.OCIImageConfig{
				"docker.io/library/fedora:latest": configWithWorkdir("/"),
				"docker.io/alpine/helm:latest":    configWithWorkdir("/"),
			},
			expected: []packageSource{
				{
					alias:      "builder1",
					pullspec:   "docker.io/library/fedora:latest",
					digestBase: "docker.io/library/fedora@sha256:hij456",
					sources:    []string{"/usr/lib/*.so"},
				},
				{
					alias:      "builder2",
					pullspec:   "docker.io/alpine/helm:latest",
					digestBase: "docker.io/library/alpine/helm@sha256:abcdef",
					sources:    []string{"/libs/*.so"},
				},
			},
		},
		"mixed wildcards and regular files": {
			stages: []containerfile.Stage{
				{
					Alias:  "builder",
					Base:   "docker.io/library/fedora:latest",
					Copies: []containerfile.Copy{},
				},
				{
					Alias: containerfile.FinalStage,
					Base:  "scratch",
					Copies: []containerfile.Copy{
						{
							From:        "builder",
							Sources:     []string{"/usr/bin/helm", "/lib/*.so", "/etc/config.txt"},
							Destination: "/app/",
						},
					},
				},
			},
			resolvedPullspecs: map[string]string{
				"docker.io/library/fedora:latest": "docker.io/library/fedora@sha256:hij456",
			},
			configs: map[string]storageclient.OCIImageConfig{
				"docker.io/library/fedora:latest": configWithWorkdir("/"),
			},
			expected: []packageSource{
				{
					alias:      "builder",
					pullspec:   "docker.io/library/fedora:latest",
					digestBase: "docker.io/library/fedora@sha256:hij456",
					sources:    []string{"/usr/bin/helm", "/lib/*.so", "/etc/config.txt"},
				},
			},
		},
		"relative destination resolution": {
			stages: []containerfile.Stage{
				{
					Alias:  "builder1",
					Base:   "docker.io/library/fedora:latest",
					Copies: []containerfile.Copy{},
				},
				{
					Alias: "builder2",
					Base:  "docker.io/library/node:latest",
					Copies: []containerfile.Copy{
						{
							From:        "builder1",
							Sources:     []string{"/usr/bin/app"},
							Destination: "bin/app",
							Type:        containerfile.CopyTypeBuilder,
							Workdir:     "/usr/local",
						},
						{
							From:        "builder1",
							Sources:     []string{"/usr/bin/tool"},
							Destination: "tool",
							Type:        containerfile.CopyTypeBuilder,
						},
					},
				},
				{
					Alias: containerfile.FinalStage,
					Base:  "scratch",
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
			},
			resolvedPullspecs: map[string]string{
				"docker.io/library/fedora:latest": "docker.io/library/fedora@sha256:aaa111",
				"docker.io/library/node:latest":   "docker.io/library/node@sha256:bbb222",
			},
			configs: map[string]storageclient.OCIImageConfig{
				"docker.io/library/fedora:latest": configWithWorkdir("/"),
				"docker.io/library/node:latest":   configWithWorkdir("/var/data"),
			},
			expected: []packageSource{
				{
					alias:      "builder1",
					pullspec:   "docker.io/library/fedora:latest",
					digestBase: "docker.io/library/fedora@sha256:aaa111",
					sources:    []string{"/usr/bin/app", "/usr/bin/tool"},
				},
				{
					alias:      "builder2",
					pullspec:   "docker.io/library/node:latest",
					digestBase: "docker.io/library/node@sha256:bbb222",
					sources:    []string{},
				},
			},
		},
		"mixed destinations with workdir variants": {
			stages: []containerfile.Stage{
				{
					Alias:  "builder1",
					Base:   "docker.io/library/fedora:latest",
					Copies: []containerfile.Copy{},
				},
				{
					Alias: "builder2",
					Base:  "docker.io/library/alpine:latest",
					Copies: []containerfile.Copy{
						{
							From:        "builder1",
							Sources:     []string{"/usr/bin/cli"},
							Destination: "/usr/local/bin/cli",
							Type:        containerfile.CopyTypeBuilder,
						},
						{
							From:        "builder1",
							Sources:     []string{"/etc/app.conf"},
							Destination: "../config/app.conf",
							Type:        containerfile.CopyTypeBuilder,
							Workdir:     "/app/subdir",
						},
						{
							From:        "builder1",
							Sources:     []string{"/src/app"},
							Destination: ".",
							Type:        containerfile.CopyTypeBuilder,
							Workdir:     "/opt",
						},
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
					Alias: containerfile.FinalStage,
					Base:  "scratch",
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
			},
			resolvedPullspecs: map[string]string{
				"docker.io/library/fedora:latest": "docker.io/library/fedora@sha256:ccc333",
				"docker.io/library/alpine:latest": "docker.io/library/alpine@sha256:ddd444",
			},
			configs: map[string]storageclient.OCIImageConfig{
				"docker.io/library/fedora:latest": configWithWorkdir("/"),
				"docker.io/library/alpine:latest": configWithWorkdir("/"),
			},
			expected: []packageSource{
				{
					alias:      "builder1",
					pullspec:   "docker.io/library/fedora:latest",
					digestBase: "docker.io/library/fedora@sha256:ccc333",
					sources:    []string{"/usr/bin/cli", "/etc/app.conf", "/src/app", "/etc/defaults.conf"},
				},
				{
					alias:      "builder2",
					pullspec:   "docker.io/library/alpine:latest",
					digestBase: "docker.io/library/alpine@sha256:ddd444",
					sources:    []string{},
				},
			},
		},
		"multi-stage tracing with workdir in intermediate stage": {
			stages: []containerfile.Stage{
				{
					Alias:  "builder1",
					Base:   "docker.io/library/fedora:latest",
					Copies: []containerfile.Copy{},
				},
				{
					Alias: "builder2",
					Base:  "docker.io/library/alpine:latest",
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
					Alias: "builder3",
					Base:  "docker.io/library/ubuntu:latest",
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
					Alias: containerfile.FinalStage,
					Base:  "scratch",
					Copies: []containerfile.Copy{
						{
							From:        "builder3",
							Sources:     []string{"/opt/app"},
							Destination: "/opt/app",
							Type:        containerfile.CopyTypeBuilder,
						},
					},
				},
			},
			resolvedPullspecs: map[string]string{
				"docker.io/library/fedora:latest": "docker.io/library/fedora@sha256:iii999",
				"docker.io/library/alpine:latest": "docker.io/library/alpine@sha256:jjj000",
				"docker.io/library/ubuntu:latest": "docker.io/library/ubuntu@sha256:kkk111",
			},
			configs: map[string]storageclient.OCIImageConfig{
				"docker.io/library/fedora:latest": configWithWorkdir("/"),
				"docker.io/library/alpine:latest": configWithWorkdir("/"),
				"docker.io/library/ubuntu:latest": configWithWorkdir("/"),
			},
			expected: []packageSource{
				{
					alias:      "builder1",
					pullspec:   "docker.io/library/fedora:latest",
					digestBase: "docker.io/library/fedora@sha256:iii999",
					sources:    []string{"/usr/bin/app"},
				},
				{
					alias:      "builder2",
					pullspec:   "docker.io/library/alpine:latest",
					digestBase: "docker.io/library/alpine@sha256:jjj000",
					sources:    []string{},
				},
				{
					alias:      "builder3",
					pullspec:   "docker.io/library/ubuntu:latest",
					digestBase: "docker.io/library/ubuntu@sha256:kkk111",
					sources:    []string{},
				},
			},
		},
		"different stages with different base image workdirs": {
			stages: []containerfile.Stage{
				{
					Alias:  "go-builder",
					Base:   "docker.io/library/golang:latest",
					Copies: []containerfile.Copy{},
				},
				{
					Alias:  "node-builder",
					Base:   "docker.io/library/node:latest",
					Copies: []containerfile.Copy{},
				},
				{
					Alias: "go-runner",
					Base:  "registry.example.com/go-runtime:v1",
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
					Alias: "node-runner",
					Base:  "registry.example.com/nginx:v1",
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
					Alias: containerfile.FinalStage,
					Base:  "scratch",
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
			},
			resolvedPullspecs: map[string]string{
				"docker.io/library/golang:latest":    "docker.io/library/golang@sha256:lll222",
				"docker.io/library/node:latest":      "docker.io/library/node@sha256:mmm333",
				"registry.example.com/go-runtime:v1": "registry.example.com/go-runtime@sha256:nnn444",
				"registry.example.com/nginx:v1":      "registry.example.com/nginx@sha256:ooo555",
			},
			configs: map[string]storageclient.OCIImageConfig{
				"docker.io/library/golang:latest":    configWithWorkdir("/go"),
				"docker.io/library/node:latest":      configWithWorkdir("/home/node"),
				"registry.example.com/go-runtime:v1": configWithWorkdir("/app"),
				"registry.example.com/nginx:v1":      configWithWorkdir("/usr/share/nginx/html"),
			},
			expected: []packageSource{
				{
					alias:      "go-builder",
					pullspec:   "docker.io/library/golang:latest",
					digestBase: "docker.io/library/golang@sha256:lll222",
					sources:    []string{"/go/bin/server"},
				},
				{
					alias:      "node-builder",
					pullspec:   "docker.io/library/node:latest",
					digestBase: "docker.io/library/node@sha256:mmm333",
					sources:    []string{"/home/node/dist/bundle.js"},
				},
				{
					alias:      "go-runner",
					pullspec:   "registry.example.com/go-runtime:v1",
					digestBase: "registry.example.com/go-runtime@sha256:nnn444",
					sources:    []string{},
				},
				{
					alias:      "node-runner",
					pullspec:   "registry.example.com/nginx:v1",
					digestBase: "registry.example.com/nginx@sha256:ooo555",
					sources:    []string{},
				},
			},
		},
		"external mount in final stage": {
			stages: []containerfile.Stage{
				{
					Alias:  "builder",
					Base:   "docker.io/library/fedora:latest",
					Copies: []containerfile.Copy{},
				},
				{
					Alias: containerfile.FinalStage,
					Base:  "scratch",
					Copies: []containerfile.Copy{
						{
							From:        "builder",
							Sources:     []string{"/opt/"},
							Destination: "/opt/",
							Type:        containerfile.CopyTypeBuilder,
						},
					},
					Mounts: []containerfile.Mount{
						{
							From:   "quay.io/tools:1",
							Type:   containerfile.MountTypeExternal,
							Source: "/",
						},
					},
				},
			},
			resolvedPullspecs: map[string]string{
				"docker.io/library/fedora:latest": "docker.io/library/fedora@sha256:abc123",
				"quay.io/tools:1":                 "quay.io/tools@sha256:def456",
			},
			configs: map[string]storageclient.OCIImageConfig{
				"docker.io/library/fedora:latest": configWithWorkdir("/"),
			},
			expected: []packageSource{
				{
					alias:      "builder",
					pullspec:   "docker.io/library/fedora:latest",
					digestBase: "docker.io/library/fedora@sha256:abc123",
					sources:    []string{"/opt/"},
				},
				{
					alias:      "",
					pullspec:   "quay.io/tools:1",
					digestBase: "quay.io/tools@sha256:def456",
					sources:    []string{"/"},
				},
			},
		},
		"builder mount in final stage broadens sources": {
			stages: []containerfile.Stage{
				{
					Alias:  "builder",
					Base:   "docker.io/library/fedora:latest",
					Copies: []containerfile.Copy{},
				},
				{
					Alias:  containerfile.FinalStage,
					Base:   "scratch",
					Copies: []containerfile.Copy{},
					Mounts: []containerfile.Mount{
						{
							From:   "builder",
							Type:   containerfile.MountTypeBuilder,
							Source: "/",
						},
					},
				},
			},
			resolvedPullspecs: map[string]string{
				"docker.io/library/fedora:latest": "docker.io/library/fedora@sha256:abc123",
			},
			configs: map[string]storageclient.OCIImageConfig{
				"docker.io/library/fedora:latest": configWithWorkdir("/"),
			},
			expected: []packageSource{
				{
					alias:      "builder",
					pullspec:   "docker.io/library/fedora:latest",
					digestBase: "docker.io/library/fedora@sha256:abc123",
					sources:    []string{"/"},
				},
			},
		},
		"builder mount in builder stage": {
			stages: []containerfile.Stage{
				{
					Alias:  "provider",
					Base:   "docker.io/library/fedora:latest",
					Copies: []containerfile.Copy{},
				},
				{
					Alias:  "consumer",
					Base:   "docker.io/alpine/helm:latest",
					Copies: []containerfile.Copy{},
					Mounts: []containerfile.Mount{
						{
							From:   "provider",
							Type:   containerfile.MountTypeBuilder,
							Source: "/",
						},
					},
				},
				{
					Alias: containerfile.FinalStage,
					Base:  "scratch",
					Copies: []containerfile.Copy{
						{
							From:        "consumer",
							Sources:     []string{"/opt/"},
							Destination: "/opt/",
							Type:        containerfile.CopyTypeBuilder,
						},
					},
				},
			},
			resolvedPullspecs: map[string]string{
				"docker.io/library/fedora:latest": "docker.io/library/fedora@sha256:abc123",
				"docker.io/alpine/helm:latest":    "docker.io/alpine/helm@sha256:def456",
			},
			configs: map[string]storageclient.OCIImageConfig{
				"docker.io/library/fedora:latest": configWithWorkdir("/"),
				"docker.io/alpine/helm:latest":    configWithWorkdir("/"),
			},
			expected: []packageSource{
				{
					alias:      "consumer",
					pullspec:   "docker.io/alpine/helm:latest",
					digestBase: "docker.io/alpine/helm@sha256:def456",
					sources:    []string{"/opt/"},
				},
			},
			expectedMountOnly: []packageSource{
				{
					alias:      "provider",
					pullspec:   "docker.io/library/fedora:latest",
					digestBase: "docker.io/library/fedora@sha256:abc123",
					sources:    []string{"/"},
				},
			},
		},
		"external mount in builder stage": {
			stages: []containerfile.Stage{
				{
					Alias:  "builder",
					Base:   "docker.io/library/fedora:latest",
					Copies: []containerfile.Copy{},
					Mounts: []containerfile.Mount{
						{
							From:   "quay.io/tools:1",
							Type:   containerfile.MountTypeExternal,
							Source: "/",
						},
					},
				},
				{
					Alias: containerfile.FinalStage,
					Base:  "scratch",
					Copies: []containerfile.Copy{
						{
							From:        "builder",
							Sources:     []string{"/opt/"},
							Destination: "/opt/",
							Type:        containerfile.CopyTypeBuilder,
						},
					},
				},
			},
			resolvedPullspecs: map[string]string{
				"docker.io/library/fedora:latest": "docker.io/library/fedora@sha256:abc123",
				"quay.io/tools:1":                 "quay.io/tools@sha256:def456",
			},
			configs: map[string]storageclient.OCIImageConfig{
				"docker.io/library/fedora:latest": configWithWorkdir("/"),
			},
			expected: []packageSource{
				{
					alias:      "builder",
					pullspec:   "docker.io/library/fedora:latest",
					digestBase: "docker.io/library/fedora@sha256:abc123",
					sources:    []string{"/opt/"},
				},
				{
					alias:      "",
					pullspec:   "quay.io/tools:1",
					digestBase: "quay.io/tools@sha256:def456",
					sources:    []string{"/"},
				},
			},
		},
		"mount with specific source narrows scan path": {
			stages: []containerfile.Stage{
				{
					Alias:  "builder",
					Base:   "docker.io/library/fedora:latest",
					Copies: []containerfile.Copy{},
				},
				{
					Alias:  containerfile.FinalStage,
					Base:   "scratch",
					Copies: []containerfile.Copy{},
					Mounts: []containerfile.Mount{
						{
							From:   "builder",
							Type:   containerfile.MountTypeBuilder,
							Source: "/usr/lib",
						},
					},
				},
			},
			resolvedPullspecs: map[string]string{
				"docker.io/library/fedora:latest": "docker.io/library/fedora@sha256:abc123",
			},
			configs: map[string]storageclient.OCIImageConfig{
				"docker.io/library/fedora:latest": configWithWorkdir("/"),
			},
			expected: []packageSource{
				{
					alias:      "builder",
					pullspec:   "docker.io/library/fedora:latest",
					digestBase: "docker.io/library/fedora@sha256:abc123",
					sources:    []string{"/usr/lib"},
				},
			},
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			mockClient := storageclient.NewMockClient(map[string]digest.Digest{}, test.configs)

			actual, actualMountOnly, err := getPackageSources(mockClient, test.stages, test.resolvedPullspecs)
			if err != nil {
				t.Fatalf("getPackageSources returned error: %v", err)
			}

			cmpOpts := cmp.Options{
				cmp.AllowUnexported(packageSource{}),
				cmpopts.SortSlices(func(a, b packageSource) bool {
					if a.alias != b.alias {
						return a.alias < b.alias
					}
					return a.pullspec < b.pullspec
				}),
				cmpopts.EquateEmpty(),
			}

			if diff := cmp.Diff(test.expected, actual, cmpOpts...); diff != "" {
				t.Errorf("getPackageSources() mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(test.expectedMountOnly, actualMountOnly, cmpOpts...); diff != "" {
				t.Errorf("getPackageSources() mountOnly mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestGetPackageSourcesError(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		stages            []containerfile.Stage
		resolvedPullspecs map[string]string
		configs           map[string]storageclient.OCIImageConfig
		expectedErr       error
	}{
		"GetImageConfig error": {
			stages: []containerfile.Stage{
				{
					Alias:  containerfile.FinalStage,
					Base:   "docker.io/library/fedora:latest",
					Copies: []containerfile.Copy{},
				},
			},
			configs:     map[string]storageclient.OCIImageConfig{},
			expectedErr: ErrOCIConfig,
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			mockClient := testutils.NewTStorageClient(map[string]digest.Digest{}, test.configs)

			_, _, err := getPackageSources(mockClient, test.stages, test.resolvedPullspecs)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !errors.Is(err, test.expectedErr) {
				t.Fatalf("expected error wrapping %v, got: %v", test.expectedErr, err)
			}
		})
	}
}

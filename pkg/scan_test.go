//go:build unit

package capo

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/konflux-ci/capo/pkg/containerfile"
)

func TestGetPackageSources(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		stages            []containerfile.Stage
		resolvedPullspecs map[string]string
		expected          []packageSource
	}{
		"only external copy in final": {
			stages: []containerfile.Stage{
				{
					Alias: containerfile.FinalStage,
					Base:  "",
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
					Base:  "",
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
					Base:  "",
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
					Base:  "",
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
					Base:  "",
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
					Base:  "",
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
					Base:  "",
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
					Base:  "",
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
					Base:  "",
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
					Base:  "",
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
			expected: []packageSource{
				{
					alias:      "builder",
					pullspec:   "docker.io/library/fedora:latest",
					digestBase: "docker.io/library/fedora@sha256:hij456",
					sources:    []string{"/usr/bin/helm", "/lib/*.so", "/etc/config.txt"},
				},
			},
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			actual, err := getPackageSources(test.stages, test.resolvedPullspecs)
			if err != nil {
				t.Fatalf("getPackageSources returned error: %v", err)
			}

			diff := cmp.Diff(
				test.expected, actual,
				cmp.AllowUnexported(packageSource{}),
				cmpopts.SortSlices(func(a, b packageSource) bool { return a.alias < b.alias }),
				cmpopts.EquateEmpty(),
			)
			if diff != "" {
				t.Errorf("getPackageSources() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

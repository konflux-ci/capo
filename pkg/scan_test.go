//go:build unit

package capo

import (
	"slices"
	"testing"

	"github.com/konflux-ci/capo/pkg/containerfile"
)

// comparePackageSources compares two slices of packageSource, ignoring order
func comparePackageSources(a, b []packageSource) bool {
	if len(a) != len(b) {
		return false
	}

	bMatched := make([]bool, len(b))
	for _, aItem := range a {
		found := false
		for j, bItem := range b {
			if !bMatched[j] && packageSourceEqual(aItem, bItem) {
				bMatched[j] = true
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}

	return true
}

// packageSourceEqual compares two packageSource structs
func packageSourceEqual(a, b packageSource) bool {
	if a.alias != b.alias || a.pullspec != b.pullspec || a.digestPullspec != b.digestPullspec {
		return false
	}

	if len(a.sources) != len(b.sources) {
		return false
	}

	for _, aSource := range a.sources {
		if !slices.Contains(b.sources, aSource) {
			return false
		}
	}

	return true
}

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
					Alias:    containerfile.FinalStage,
					Pullspec: "",
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
					alias:          "",
					pullspec:       "docker.io/library/fedora:latest",
					digestPullspec: "docker.io/library/fedora@sha256:abc123",
					sources:        []string{"/usr/bin/oras"},
				},
			},
		},
		"copies in final stage only": {
			stages: []containerfile.Stage{
				{
					Alias:    "builder1",
					Pullspec: "docker.io/library/fedora:latest",
					Copies:   []containerfile.Copy{},
				},
				{
					Alias:    "builder2",
					Pullspec: "docker.io/alpine/helm:latest",
					Copies:   []containerfile.Copy{},
				},
				{
					Alias:    containerfile.FinalStage,
					Pullspec: "",
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
					alias:          "builder1",
					pullspec:       "docker.io/library/fedora:latest",
					digestPullspec: "docker.io/library/fedora@sha256:def456",
					sources:        []string{"/usr/bin/oras"},
				},
				{
					alias:          "builder2",
					pullspec:       "docker.io/alpine/helm:latest",
					digestPullspec: "docker.io/alpine/helm@sha256:ghi789",
					sources:        []string{"/usr/bin/helm"},
				},
			},
		},
		"recursive multi-stage file copy": {
			stages: []containerfile.Stage{
				{
					Alias:    "builder1",
					Pullspec: "docker.io/library/fedora:latest",
					Copies:   []containerfile.Copy{},
				},
				{
					Alias:    "builder2",
					Pullspec: "docker.io/alpine/helm:latest",
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
					Alias:    containerfile.FinalStage,
					Pullspec: "",
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
					alias:          "builder1",
					pullspec:       "docker.io/library/fedora:latest",
					digestPullspec: "docker.io/library/fedora@sha256:jkl012",
					sources:        []string{"/usr/bin/oras"},
				},
				{
					alias:          "builder2",
					pullspec:       "docker.io/alpine/helm:latest",
					digestPullspec: "docker.io/alpine/helm@sha256:mno345",
					sources:        []string{},
				},
			},
		},
		"recursive multi-stage file copy - mixed sources": {
			stages: []containerfile.Stage{
				{
					Alias:    "builder1",
					Pullspec: "docker.io/library/fedora:latest",
					Copies:   []containerfile.Copy{},
				},
				{
					Alias:    "builder2",
					Pullspec: "docker.io/alpine/helm:latest",
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
					Alias:    containerfile.FinalStage,
					Pullspec: "",
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
					alias:          "builder1",
					pullspec:       "docker.io/library/fedora:latest",
					digestPullspec: "docker.io/library/fedora@sha256:pqr678",
					sources:        []string{"/usr/bin/oras"},
				},
				{
					alias:          "builder2",
					pullspec:       "docker.io/alpine/helm:latest",
					digestPullspec: "docker.io/alpine/helm@sha256:stu901",
					sources:        []string{"/usr/bin/helm"},
				},
			},
		},
		"multi-stage directory copy": {
			stages: []containerfile.Stage{
				{
					Alias:    "builder1",
					Pullspec: "docker.io/library/fedora:latest",
					Copies:   []containerfile.Copy{},
				},
				{
					Alias:    "builder2",
					Pullspec: "docker.io/alpine/helm:latest",
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
					Alias:    containerfile.FinalStage,
					Pullspec: "",
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
					alias:          "builder1",
					pullspec:       "docker.io/library/fedora:latest",
					digestPullspec: "docker.io/library/fedora@sha256:vwx234",
					sources:        []string{"/usr/bin/oras", "/bin/*"},
				},
				{
					alias:          "builder2",
					pullspec:       "docker.io/alpine/helm:latest",
					digestPullspec: "docker.io/alpine/helm@sha256:yza567",
					sources:        []string{"/app/"},
				},
			},
		},
		"ignore non-copied content": {
			stages: []containerfile.Stage{
				{
					Alias:    "builder1",
					Pullspec: "docker.io/library/fedora:latest",
					Copies:   []containerfile.Copy{},
				},
				{
					Alias:    "builder2",
					Pullspec: "docker.io/alpine/helm:latest",
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
					Alias:    containerfile.FinalStage,
					Pullspec: "",
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
					alias:          "builder1",
					pullspec:       "docker.io/library/fedora:latest",
					digestPullspec: "docker.io/library/fedora@sha256:bcd890",
					sources:        []string{},
				},
				{
					alias:          "builder2",
					pullspec:       "docker.io/alpine/helm:latest",
					digestPullspec: "docker.io/alpine/helm@sha256:efg123",
					sources:        []string{"/app/"},
				},
			},
		},
		"complex multi-stage with multiple final copies": {
			stages: []containerfile.Stage{
				{
					Alias:    "builder1",
					Pullspec: "docker.io/library/fedora:latest",
					Copies:   []containerfile.Copy{},
				},
				{
					Alias:    "builder2",
					Pullspec: "docker.io/alpine/helm:latest",
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
					Alias:    containerfile.FinalStage,
					Pullspec: "",
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
					alias:          "builder1",
					pullspec:       "docker.io/library/fedora:latest",
					digestPullspec: "docker.io/library/fedora@sha256:hij456",
					sources:        []string{"/lib/libc.so", "/usr/bin/kubectl"},
				},
				{
					alias:          "builder2",
					pullspec:       "docker.io/alpine/helm:latest",
					digestPullspec: "docker.io/alpine/helm@sha256:klm789",
					sources:        []string{"/tools/", "/usr/bin/helm"},
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

			if !comparePackageSources(actual, test.expected) {
				t.Fatalf("actual package sources %+v, don't match the expected %+v", actual, test.expected)
			}
		})
	}
}

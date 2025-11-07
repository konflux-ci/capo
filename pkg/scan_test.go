package capo

import (
	"capo/pkg/containerfile"
	"testing"
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
	if a.alias != b.alias || a.pullspec != b.pullspec {
		return false
	}

	if len(a.sources) != len(b.sources) {
		return false
	}

	for _, aSource := range a.sources {
		found := false
		for _, bSource := range b.sources {
			if aSource == bSource {
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

func TestGetPackageSources(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		stages   []containerfile.Stage
		expected []packageSource
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
						},
					},
				},
			},
			expected: []packageSource{
				{
					alias:    "",
					pullspec: "docker.io/library/fedora:latest",
					sources:  []string{"/usr/bin/oras"},
				},
			},
		},
		"arg evaluation": {
			stages: []containerfile.Stage{
				{
					Alias:    "builder",
					Pullspec: "docker.io/library/alpine:latest",
					Copies:   []containerfile.Copy{},
				},
				{
					Alias:    containerfile.FinalStage,
					Pullspec: "",
					Copies: []containerfile.Copy{
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
			expected: []packageSource{
				{
					alias:    "builder",
					pullspec: "docker.io/library/alpine:latest",
					sources:  []string{"/usr/bin/binary"},
				},
				{
					alias:    "",
					pullspec: "docker.io/library/fedora:latest",
					sources:  []string{"/usr/bin/oras"},
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
						},
						{
							From:        "builder2",
							Sources:     []string{"/usr/bin/helm"},
							Destination: "/usr/bin/helm",
						},
					},
				},
			},
			expected: []packageSource{
				{
					alias:    "builder1",
					pullspec: "docker.io/library/fedora:latest",
					sources:  []string{"/usr/bin/oras"},
				},
				{
					alias:    "builder2",
					pullspec: "docker.io/alpine/helm:latest",
					sources:  []string{"/usr/bin/helm"},
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
						},
					},
				},
			},
			expected: []packageSource{
				{
					alias:    "builder1",
					pullspec: "docker.io/library/fedora:latest",
					sources:  []string{"/usr/bin/oras"},
				},
				{
					alias:    "builder2",
					pullspec: "docker.io/alpine/helm:latest",
					sources:  []string{},
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
						},
					},
				},
			},
			expected: []packageSource{
				{
					alias:    "builder1",
					pullspec: "docker.io/library/fedora:latest",
					sources:  []string{"/usr/bin/oras"},
				},
				{
					alias:    "builder2",
					pullspec: "docker.io/alpine/helm:latest",
					sources:  []string{"/usr/bin/helm"},
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
						},
						{
							From:        "builder1",
							Sources:     []string{"/bin/*"},
							Destination: "/app/",
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
						},
					},
				},
			},
			expected: []packageSource{
				{
					alias:    "builder1",
					pullspec: "docker.io/library/fedora:latest",
					sources:  []string{"/usr/bin/oras", "/bin/*"},
				},
				{
					alias:    "builder2",
					pullspec: "docker.io/alpine/helm:latest",
					sources:  []string{"/app/"},
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
						},
					},
				},
			},
			expected: []packageSource{
				{
					alias:    "builder1",
					pullspec: "docker.io/library/fedora:latest",
					sources:  []string{},
				},
				{
					alias:    "builder2",
					pullspec: "docker.io/alpine/helm:latest",
					sources:  []string{"/app/"},
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
			expected: []packageSource{
				{
					alias:    "builder1",
					pullspec: "docker.io/library/fedora:latest",
					sources:  []string{"/lib/libc.so", "/usr/bin/kubectl"},
				},
				{
					alias:    "builder2",
					pullspec: "docker.io/alpine/helm:latest",
					sources:  []string{"/tools/", "/usr/bin/helm"},
				},
			},
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			actual := getPackageSources(test.stages)

			if !comparePackageSources(actual, test.expected) {
				t.Fatalf("actual package sources %+v, don't match the expected %+v", actual, test.expected)
			}
		})
	}
}

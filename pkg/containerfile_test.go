package capo

import (
	"slices"
	"testing"
)

func TestMapPullspecsToSources(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		stages          []cfileStage
		expectedMapping map[string][]string
	}{
		"copies in final stage only": {
			stages: []cfileStage{
				{
					alias:    "builder1",
					pullspec: "docker.io/library/fedora:latest",
					copies: []copy{
						{
							sources:     []string{"/usr/bin/oras"},
							destination: "/usr/bin/oras",
							stage:       FinalStage,
						},
					},
				},
				{
					alias:    "builder2",
					pullspec: "docker.io/alpine/helm:latest",
					copies: []copy{
						{
							sources:     []string{"/usr/bin/helm"},
							destination: "/usr/bin/helm",
							stage:       FinalStage,
						},
					},
				},
			},
			expectedMapping: map[string][]string{
				"docker.io/library/fedora:latest": {"/usr/bin/oras"},
				"docker.io/alpine/helm:latest":    {"/usr/bin/helm"},
			},
		},
		"multi-stage file copy": {
			stages: []cfileStage{
				{
					alias:    "builder1",
					pullspec: "docker.io/library/fedora:latest",
					copies: []copy{
						{
							sources:     []string{"/usr/bin/oras"},
							destination: "/usr/bin/oras",
							stage:       "builder2",
						},
					},
				},
				{
					alias:    "builder2",
					pullspec: "docker.io/alpine/helm:latest",
					copies: []copy{
						{
							sources:     []string{"/usr/bin/oras"},
							destination: "/usr/bin/oras",
							stage:       FinalStage,
						},
					},
				},
			},
			expectedMapping: map[string][]string{
				"docker.io/library/fedora:latest": {"/usr/bin/oras"},
				"docker.io/alpine/helm:latest":    {"/usr/bin/oras"},
			},
		},
		"multi-stage directory copy": {
			stages: []cfileStage{
				{
					alias:    "builder1",
					pullspec: "docker.io/library/fedora:latest",
					copies: []copy{
						{
							sources:     []string{"/usr/bin/oras"},
							destination: "/app/oras",
							stage:       "builder2",
						},
						{
							sources:     []string{"/bin/*"},
							destination: "/app/",
							stage:       "builder2",
						},
					},
				},
				{
					alias:    "builder2",
					pullspec: "docker.io/alpine/helm:latest",
					copies: []copy{
						{
							sources:     []string{"/app/"},
							destination: "/app/",
							stage:       FinalStage,
						},
					},
				},
			},
			expectedMapping: map[string][]string{
				// include /usr/bin/oras since final stage copies /app/ from builder2
				// and builder2 copies /usr/bin/oras from builder1 to /app/oras
				//
				// include /bin/* since final stage copies from /app/ from builder2
				// and builder2 copies /bin/* from builder1 to /app/
				"docker.io/library/fedora:latest": {"/usr/bin/oras", "/bin/*"},
				"docker.io/alpine/helm:latest":    {"/app/"},
			},
		},
		"ignore non-copied content": {
			stages: []cfileStage{
				{
					alias:    "builder1",
					pullspec: "docker.io/library/fedora:latest",
					copies: []copy{
						{
							sources:     []string{"/usr/bin/wget"},
							destination: "/usr/bin/wget",
							stage:       "builder2",
						},
					},
				},
				{
					alias:    "builder2",
					pullspec: "docker.io/alpine/helm:latest",
					copies: []copy{
						{
							sources:     []string{"/app/"},
							destination: "/app/",
							stage:       FinalStage,
						},
					},
				},
			},
			expectedMapping: map[string][]string{
				// don't include builder1 at all since it has no copies to final stage
				// only builder2 appears because it has a copy to final stage
				"docker.io/alpine/helm:latest": {"/app/"},
			},
		},
		"complex multi-stage with multiple final copies": {
			stages: []cfileStage{
				{
					alias:    "builder1",
					pullspec: "docker.io/library/fedora:latest",
					copies: []copy{
						{
							sources:     []string{"/usr/bin/kubectl"},
							destination: "/tools/kubectl",
							stage:       "builder2",
						},
						{
							sources:     []string{"/lib/libc.so"},
							destination: "/lib/libc.so",
							stage:       FinalStage,
						},
					},
				},
				{
					alias:    "builder2",
					pullspec: "docker.io/alpine/helm:latest",
					copies: []copy{
						{
							sources:     []string{"/tools/"},
							destination: "/usr/bin/",
							stage:       FinalStage,
						},
						{
							sources:     []string{"/usr/bin/helm"},
							destination: "/usr/bin/helm",
							stage:       FinalStage,
						},
					},
				},
			},
			expectedMapping: map[string][]string{
				"docker.io/library/fedora:latest": {"/lib/libc.so", "/usr/bin/kubectl"},
				"docker.io/alpine/helm:latest":    {"/tools/", "/usr/bin/helm"},
			},
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			actualMapping := mapPullspecsToSources(test.stages)

			for expectedPullspec, expectedSources := range test.expectedMapping {
				actualSources, exists := actualMapping[expectedPullspec]
				if !exists {
					t.Errorf("Expected pullspec %s not found in actual mapping", expectedPullspec)
					continue
				}

				if !slices.Equal(actualSources, expectedSources) {
					t.Errorf("Unexpected sources for pullspec %s. Have %+v, expected %+v",
						expectedPullspec, actualSources, expectedSources)
				}
			}
		})
	}
}

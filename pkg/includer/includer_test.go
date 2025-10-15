package includer

import (
	"slices"
	"testing"
)

type testStageData struct {
	alias  string
	copies []Copier
}

func (ts testStageData) Alias() string {
	return ts.alias
}

func (ts testStageData) Pullspec() string {
	// not necessary for includer creation
	return ""
}

func (ts testStageData) Copies() []Copier {
	return ts.copies
}

type testCopier struct {
	sources     []string
	destination string
	stage       string
}

const FinalStage string = ""

func (tc testCopier) Sources() []string {
	return tc.sources
}

func (tc testCopier) Destination() string {
	return tc.destination
}

func (tc testCopier) IsFromFinalStage() bool {
	return tc.stage == FinalStage
}

func TestExternalIncluder(t *testing.T) {
	t.Parallel()
	copies := []Copier{
		testCopier{
			sources: []string{"/usr/bin/oras"},
		},
		testCopier{
			sources: []string{"/lib/*"},
		},
		testCopier{
			sources: []string{"/bin/syft"},
		},
	}

	stageD := testStageData{
		alias:  "",
		copies: copies,
	}

	inc := External(stageD)

	actual := inc.Sources()
	expected := []string{"/usr/bin/oras", "/lib/*", "/bin/syft"}
	if !slices.Equal(actual, expected) {
		t.Errorf("Unexpected sources for External includer. Have %+v, expected %+v", actual, expected)
	}
}

func TestBuilderIncluder(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		stageD         []StageData
		aliasToSources map[string][]string
	}{
		"copies in final stage only": {
			stageD: []StageData{
				testStageData{
					alias: "builder1",
					copies: []Copier{
						testCopier{
							sources:     []string{"/usr/bin/oras"},
							destination: "/usr/bin/oras",
							stage:       FinalStage,
						},
					},
				},
				testStageData{
					alias: "builder2",
					copies: []Copier{
						testCopier{
							sources:     []string{"/usr/bin/syft"},
							destination: "/usr/bin/syft",
							stage:       FinalStage,
						},
					},
				},
			},
			aliasToSources: map[string][]string{
				"builder1": {"/usr/bin/oras"},
				"builder2": {"/usr/bin/syft"},
			},
		},
		"multi-stage file copy": {
			stageD: []StageData{
				testStageData{
					alias: "builder1",
					copies: []Copier{
						testCopier{
							sources:     []string{"/usr/bin/oras"},
							destination: "/usr/bin/oras",
							stage:       "builder2",
						},
					},
				},
				testStageData{
					alias: "builder2",
					copies: []Copier{
						testCopier{
							sources:     []string{"/usr/bin/oras"},
							destination: "/usr/bin/oras",
							stage:       FinalStage,
						},
					},
				},
			},
			aliasToSources: map[string][]string{
				"builder1": {"/usr/bin/oras"},
				"builder2": {"/usr/bin/oras"},
			},
		},
		"multi-stage directory copy": {
			stageD: []StageData{
				testStageData{
					alias: "builder1",
					copies: []Copier{
						testCopier{
							sources:     []string{"/usr/bin/oras"},
							destination: "/app/oras",
							stage:       "builder2",
						},
						testCopier{
							sources:     []string{"/bin/*"},
							destination: "/app/",
							stage:       "builder2",
						},
					},
				},
				testStageData{
					alias: "builder2",
					copies: []Copier{
						testCopier{
							sources:     []string{"/app/"},
							destination: "/app/",
							stage:       FinalStage,
						},
					},
				},
			},
			aliasToSources: map[string][]string{
				// include /usr/bin/oras since final stage copies /app/ from builder2
				// and builder2 copies /usr/bin/oras from builder1 to /app/oras
				//
				// include /bin/* since final stage copies from /app/ from builder2
				// and builder2 copies /bin/* from builder1 to /app/
				"builder1": {"/usr/bin/oras", "/bin/*"},
				"builder2": {"/app/"},
			},
		},
		"ignore non-copied content": {
			stageD: []StageData{
				testStageData{
					alias: "builder1",
					copies: []Copier{
						testCopier{
							sources:     []string{"/usr/bin/wget"},
							destination: "/usr/bin/wget",
							stage:       "builder2",
						},
					},
				},
				testStageData{
					alias: "builder2",
					copies: []Copier{
						testCopier{
							sources:     []string{"/app/"},
							destination: "/app/",
							stage:       FinalStage,
						},
					},
				},
			},
			aliasToSources: map[string][]string{
				// don't include /usr/bin/wget in builder1 content, since it was only used
				// in the builder2 stage, but not copied to the final stage
				"builder1": {},
				"builder2": {"/app/"},
			},
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			includers := NewBuilderIncluders(test.stageD)

			for alias, expectedSources := range test.aliasToSources {
				inc := includers.GetIncluderForAlias(alias)
				actual := inc.Sources()

				if !slices.Equal(actual, expectedSources) {
					t.Errorf("Unexpected sources for alias %s. Have %+v, expected %+v", alias, actual, expectedSources)
				}
			}
		})
	}
}

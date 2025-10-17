package capo

import (
	"strings"
)

var FinalStage string = ""

type copy struct {
	sources     []string
	destination string
	stage       string
}

type cfileStage struct {
	alias    string
	pullspec string
	copies   []copy
}

type stage struct {
	alias    string
	pullspec string
	sources  []string
}

func (s stage) Alias() string {
	return s.alias
}

func (s stage) Pullspec() string {
	return s.pullspec
}

func (s stage) Sources() []string {
	return s.sources
}

// ParseContainerfile takes the path to a dockerfile-json output file and
// parses it into stages.
func ParseContainerfile(path string) ([]Stage, error) {
	// TODO: implement

	cfileStages := []cfileStage{
		{
			alias:    "fedora-builder",
			pullspec: "docker.io/library/fedora:latest",
			copies: []copy{
				{
					sources:     []string{"/usr/bin/kubectl"},
					destination: "/usr/bin/kubectl",
					stage:       FinalStage,
				},
			},
		},
		{
			alias:    "helm-builder",
			pullspec: "docker.io/alpine/helm:latest",
			copies: []copy{
				{
					sources:     []string{"/usr/bin/helm"},
					destination: "/usr/bin/helm",
					stage:       FinalStage,
				},
			},
		},
		{
			alias:    "",
			pullspec: "quay.io/konflux-ci/oras:41b74d6",
			copies: []copy{
				{
					sources:     []string{"/usr/bin/oras"},
					destination: "/usr/bin/oras",
					stage:       FinalStage,
				},
			},
		},
	}

	pullspecsToSources := mapPullspecsToSources(cfileStages)
	stages := make([]Stage, 0)
	for _, cfileStage := range cfileStages {
		stages = append(stages, stage{
			alias:    cfileStage.alias,
			pullspec: cfileStage.pullspec,
			sources:  pullspecsToSources[cfileStage.pullspec],
		})
	}

	return stages, nil
}

func mapPullspecsToSources(stages []cfileStage) map[string][]string {
	graphs := make([]copyNode, 0)
	for i, s := range stages {
		for _, cp := range s.copies {
			if cp.stage == FinalStage {
				root := copyNode{
					pullspec: s.pullspec,
					source:   cp.sources,
					dest:     cp.destination,
					children: make([]copyNode, 0),
				}
				buildDependencyTree(&root, stages, i)
				graphs = append(graphs, root)
			}
		}
	}

	pullspecsToSources := make(map[string][]string)
	for _, tree := range graphs {
		collectCopies(tree, pullspecsToSources)
	}

	return pullspecsToSources
}

type copyNode struct {
	pullspec string
	source   []string
	dest     string
	children []copyNode
}

// Builds a dependency tree with the root being the specified node.
// Goes through the root's source paths and traverses recursively up the builder stages,
// creating edges beween the nodes if the child node's destination
// path is a subpath of the parent node's source paths, i.e. the parent
// COPY command copies from the destination of the child's COPY.
func buildDependencyTree(node *copyNode, stages []cfileStage, currentStageIndex int) {
	for _, srcPath := range node.source {
		for i := range currentStageIndex {
			s := stages[i]
			for _, cp := range s.copies {
				if strings.HasPrefix(cp.destination, srcPath) {
					child := copyNode{
						pullspec: s.pullspec,
						source:   cp.sources,
						dest:     cp.destination,
						children: make([]copyNode, 0),
					}
					buildDependencyTree(&child, stages, i)
					node.children = append(node.children, child)
				}
			}
		}
	}
}

// Recursively goes through the dependency tree and modifies pullspecsToSources
// to include content that is reachable from the final stage.
func collectCopies(node copyNode, pullspecsToSources map[string][]string) {
	pullspecsToSources[node.pullspec] = append(pullspecsToSources[node.pullspec], node.source...)
	for _, child := range node.children {
		collectCopies(child, pullspecsToSources)
	}
}

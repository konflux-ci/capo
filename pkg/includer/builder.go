package includer

import "strings"

// TODO: implement pattern matching for all CopyMask users
// https://docs.docker.com/reference/dockerfile/#pattern-matching

// FIXME: clean up the variable names in this file

// The BuilderIncluder struct exposes methods to verify if a certain file or directory
// in a container layer should be included in an SBOM or not.
// Paths are only included if they're 'reachable' from the final stage of the build.
// This mechanism makes sure that we only syft scan content that will be present in the final built image.
type BuilderIncluder struct {
	sources []string
}

// Container for mapping builders to their copy masks.
type BuilderIncluders struct {
	aliasToMask map[string]BuilderIncluder
}

// Returns a CopyMask specific to the passed builder.
// If a mask for a builder is not found, returns a CopyMask that includes no content.
func (masks BuilderIncluders) GetMask(data StageData) Includer {
	mask, exists := masks.aliasToMask[data.Alias()]
	if !exists {
		return BuilderIncluder{sources: []string{}}
	}
	return mask
}

// Parse builders and return a struct exposing a method for
// fetching copy masks for a specific builder.
//
// The masks are built by creating a 'dependency tree' for each builder.
// The tree's root node is a COPY command in the final building stage.
// There is an edge between two nodes if the child node's destination
// path is a subpath of the parent node's source paths, i.e. the parent
// COPY command copies from the destination of the child's COPY.
func NewBuilderIncluders(data []StageData) BuilderIncluders {
	if len(data) == 0 {
		return BuilderIncluders{
			make(map[string]BuilderIncluder),
		}
	}

	graphs := make([]copyNode, 0)
	for i, d := range data {
		for _, cp := range d.Copies() {
			if cp.IsFromFinalStage() {
				root := copyNode{
					builder:  d.Alias(),
					source:   cp.Sources(),
					dest:     cp.Destination(),
					children: make([]copyNode, 0),
				}
				buildDependencyTree(&root, data, i)
				graphs = append(graphs, root)
			}
		}
	}

	mask := make(map[string][]string)
	for _, tree := range graphs {
		collectCopies(tree, mask)
	}

	aliasToMask := make(map[string]BuilderIncluder)
	for alias, sources := range mask {
		aliasToMask[alias] = BuilderIncluder{sources: sources}
	}

	return BuilderIncluders{aliasToMask: aliasToMask}
}

func (inc BuilderIncluder) Sources() []string {
	return inc.sources
}

type copyNode struct {
	builder  string
	source   []string
	dest     string
	children []copyNode
}

// Builds a dependency tree with the root being the specified node.
// Goes through the root's source paths and traverses recursively up the builder stages,
// creating edges beween the nodes if the child node's destination
// path is a subpath of the parent node's source paths, i.e. the parent
// COPY command copies from the destination of the child's COPY.
func buildDependencyTree(node *copyNode, data []StageData, currentBuilderIndex int) {
	for _, srcPath := range node.source {
		for i := range currentBuilderIndex {
			d := data[i]
			for _, copy := range d.Copies() {
				if strings.HasPrefix(copy.Destination(), srcPath) {
					child := copyNode{
						builder:  d.Alias(),
						source:   copy.Sources(),
						dest:     copy.Destination(),
						children: make([]copyNode, 0),
					}
					buildDependencyTree(&child, data, i)
					node.children = append(node.children, child)
				}
			}
		}
	}
}

// Recursively goes through the dependency tree and modifies 'mask'
// to include content that is reachable from the final stage.
func collectCopies(node copyNode, mask map[string][]string) {
	mask[node.builder] = append(mask[node.builder], node.source...)
	for _, child := range node.children {
		collectCopies(child, mask)
	}
}

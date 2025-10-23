package capo

import (
	"strings"
)

// TODO: implement pattern matching for all CopyMask users
// https://docs.docker.com/reference/dockerfile/#pattern-matching

// The CopyMask struct exposes methods to verify if a certain file or directory
// in a container layer should be included in an SBOM or not.
// Paths are only included if they're 'reachable' from the final stage of the build.
// This mechanism makes sure that we only syft scan content that will be present in the final built image.
type CopyMask struct {
	sources []string
}

// Container for mapping builders to their copy masks.
type CopyMasks struct {
	aliasToMask map[string]CopyMask
}

// Returns a CopyMask specific to the passed builder.
// If a mask for a builder is not found, returns a CopyMask that includes no content.
func (masks CopyMasks) GetMask(builder Builder) CopyMask {
	mask, exists := masks.aliasToMask[builder.Alias]
	if !exists {
		return CopyMask{sources: []string{}}
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
func NewCopyMasks(builders []Builder) CopyMasks {
	if len(builders) == 0 {
		return CopyMasks{
			make(map[string]CopyMask),
		}
	}

	graphs := make([]copyNode, 0)
	for i, bldr := range builders {
		for _, cp := range bldr.Copies {
			if cp.IsFromFinalStage() {
				root := copyNode{
					builder:  bldr.Alias,
					source:   cp.Source,
					dest:     cp.Dest,
					children: make([]copyNode, 0),
				}
				buildDependencyTree(&root, builders, i)
				graphs = append(graphs, root)
			}
		}
	}

	mask := make(map[string][]string)
	for _, tree := range graphs {
		collectCopies(tree, mask)
	}

	aliasToMask := make(map[string]CopyMask)
	for alias, sources := range mask {
		aliasToMask[alias] = CopyMask{sources: sources}
	}

	return CopyMasks{aliasToMask: aliasToMask}
}

// Returns true if a path should be included in syft scanned content.
// Paths are only included if they're 'reachable' from the final stage of the build.
// Transparently handles '/' prefixes of the specified path.
func (mask CopyMask) Includes(path string) bool {
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	for _, src := range mask.sources {
		if strings.HasPrefix(path, src) {
			return true
		}
	}

	return false
}

// Returns a slice of paths whose subpaths (including the path itself)
// should be included in syft scanned content.
func (mask CopyMask) GetSources() []string {
	return mask.sources
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
func buildDependencyTree(node *copyNode, builders []Builder, currentBuilderIndex int) {
	for _, srcPath := range node.source {
		for i := range currentBuilderIndex {
			bldr := builders[i]
			for _, copy := range bldr.Copies {
				if strings.HasPrefix(copy.Dest, srcPath) {
					child := copyNode{
						builder:  bldr.Alias,
						source:   copy.Source,
						dest:     copy.Dest,
						children: make([]copyNode, 0),
					}
					buildDependencyTree(&child, builders, i)
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

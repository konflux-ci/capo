package containerfile

import (
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strings"

	"github.com/openshift/imagebuilder"
	"github.com/openshift/imagebuilder/dockerfile/parser"
)

var FinalStage string = ""

type CopyType int

const (
	CopyTypeBuilder CopyType = iota
	CopyTypeExternal
)

// A builder-type COPY command in a Containerfile stage.
// A COPY is builder-type if it copies from a previous builder stage
// or directly from an image.
type Copy struct {
	// Sources in the command.
	Sources []string
	// Destination in the command.
	Destination string
	// Alias of the stage the command is copying from when Copy.Type==CopyTypeBuilder
	// or a pullspec when Copy.Type==CopyTypeExternal
	From string
	// Type of the COPY. Specifies whether it is a copy from a builder stage
	// or an external image directly.
	Type CopyType
}

// A builder or final stage in a Containerfile
type Stage struct {
	// Alias of the builder stage or equal to FinalStage if final
	Alias string
	// Base image for the stage
	Pullspec string
	// Builder copies in this stage
	Copies []Copy
}

type BuildOptions struct {
	// Build arguments passed to buildah for the build
	Args map[string]string
	// Target stage of the buildah build
	Target string
}

var ErrTargetNotFound = errors.New("specified target stage was not found in the containerfile")
var ErrAmbiguousRelativePath = errors.New("relative path in containerfile is ambiguous")
var ErrParse = errors.New("error while parsing containerfile")

// Parse reads a Containerfile from the passed reader and uses the passed
// BuildOptions to parse the Containerfile into stages.
func Parse(reader io.Reader, opts BuildOptions) ([]Stage, error) {
	res := make([]Stage, 0)

	node, err := imagebuilder.ParseDockerfile(reader)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrParse, err)
	}

	// TODO: At this stage, Buildah code takes into account OS and ARCH CLI args
	// and overrides the built-in TARGETOS and TARGETARCH args (and others).
	// The imagebuilder automatically injects these args when evaluating args.
	// In Konflux build, target and platform overriding is currently not supported
	// but I'm keeping this here as a guideline.
	// https://github.com/containers/buildah/blob/main/imagebuildah/build.go#L431

	builder := imagebuilder.NewBuilder(opts.Args)
	rawStages, err := imagebuilder.NewStages(node, builder)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrParse, err)
	}

	if opts.Target != "" {
		stagesTargeted, ok := rawStages.ThroughTarget(opts.Target)
		if !ok {
			return nil, fmt.Errorf("%w: %s", ErrTargetNotFound, opts.Target)
		}
		rawStages = stagesTargeted
	}

	aliasToPullspec := mapAliasesToPullspecs(rawStages)
	stageNames := make([]string, 0)

	for i, s := range rawStages {
		stageNames = append(stageNames, s.Name)
		if i == len(rawStages)-1 {
			s.Name = FinalStage
		}

		copies, err := getBuilderCopiesInStage(s, stageNames)
		if err != nil {
			return res, err
		}

		res = append(res, Stage{
			Alias:    s.Name,
			Pullspec: aliasToPullspec[s.Name],
			Copies:   copies,
		})
	}

	return res, nil
}

// argsMapToSlice returns the contents of a map[string]string as a slice of keys
// and values joined with "=".
func argsMapToSlice(m map[string]string) []string {
	s := make([]string, 0, len(m))
	for k, v := range m {
		s = append(s, k+"="+v)
	}
	return s
}

// mapAliasesToPullspecs uses the passed imagebuilder.Stage structs to create
// a mapping between stage aliases and the base image pullspecs for those stages.
func mapAliasesToPullspecs(stages []imagebuilder.Stage) map[string]string {
	res := make(map[string]string)

	// skip final stage, copies from that stage are not allowed
	for _, s := range stages[:len(stages)-1] {
		headingEnv := argsMapToSlice(s.Builder.HeadingArgs)
		userEnv := argsMapToSlice(s.Builder.Args)
		env := append(headingEnv, userEnv...)

		fromNode := s.Node.Children[0]
		res[s.Name], _ = imagebuilder.ProcessWord(fromNode.Next.Value, env)
	}

	return res
}

// getBuilderCopiesInStage parses the AST for the passed imagebuilder.Stage and
// returns a slice of Copy structs, specifying which builder-type copies are
// present in that stage.
// A COPY command is builder-type if the "--from" flag is specified and it copies from
// a builder stage or directly from an image.
// Uses the passed previous stageNames to specify whether copies are from a stage
// or directly from an image.
//
// WORKDIR commands are taken into account and the destinations of COPY commands
// are resolved to be absolute instead of relative, where needed. If the Containerfile
// contains builder-type COPY commands that copy to a relative destination and don't
// specify the WORKDIR in advance, getBuilderCopiesInStage returns a workdirError.
// This limitation exists because each base image can set its own WORKDIR and this cannot
// be determined based on just the Containerfile.
//
// WARNING: named contexts in the Containerfile are not supported
func getBuilderCopiesInStage(s imagebuilder.Stage, stageNames []string) ([]Copy, error) {
	copies := make([]Copy, 0)
	workdir := ""
	headingEnv := argsMapToSlice(s.Builder.HeadingArgs)
	userEnv := argsMapToSlice(s.Builder.Args)

	// user provided args override the heading ARGs,
	// so they're appended second to take priority
	env := append(headingEnv, userEnv...)

	for _, child := range s.Node.Children {
		switch child.Value {
		case "workdir":
			dirPath := child.Next.Value
			if filepath.IsAbs(dirPath) {
				workdir = dirPath
			} else {
				// if the path is relative, it is relative to the last set workdir
				// so we need to fail if a WORKDIR command was not yet specified
				if workdir == "" {
					return copies, fmt.Errorf("%w: %q", ErrAmbiguousRelativePath, child.Original)
				}

				workdir = filepath.Join(workdir, dirPath)
			}

		case "copy":
			cp, err := parseCopy(child, workdir, env, stageNames)
			if err != nil {
				return copies, err
			}

			if cp != nil {
				copies = append(copies, *cp)
			}
		}
	}

	return copies, nil
}

// parseCopy takes a raw dockerfile parser Node and optionally returns a pointer
// to a parsed Copy struct.
// Returns (nil, nil) if the COPY command is not builder-type, but copies from a context.
// Uses the passed workdir to resolve relative paths in the COPY's destination to absolute.
// Uses the passed env to evaluate arguments in the COPY.
// Uses the passed previous stage names to evaluate whether this COPY command is from
// a builder stage or directly from an external image.
func parseCopy(node *parser.Node, workdir string, env []string, stageNames []string) (*Copy, error) {
	for _, fl := range node.Flags {
		// TODO: When the "--from" flag is included, this is a COPY either from a builder stage,
		// an external image or a named context. We assume that named contexts aren't used,
		// as they're not supported in any current Konflux buildah tasks. To resolve this in
		// the future, we might have to include a --build-context argument to capo (to use the same
		// syntax as "buildah bud") and skip the copies that copy from these contexts.
		if !strings.HasPrefix(fl, "--from=") {
			continue
		}
		from, _ := imagebuilder.ProcessWord(strings.TrimPrefix(fl, "--from="), env)

		// aggregate the COPY arguments by iterating the nodes
		args := make([]string, 0)
		curr := node.Next
		for curr != nil {
			args = append(args, curr.Value)
			curr = curr.Next
		}

		sources := args[:len(args)-1]

		destination := args[len(args)-1]
		// resolve relative paths
		if !filepath.IsAbs(destination) {
			if workdir == "" {
				return nil, fmt.Errorf("%w: %q", ErrAmbiguousRelativePath, node.Original)

			}

			_, destFile := filepath.Split(destination)
			destIsDir := destFile == "" || destFile == ".." || destFile == "."
			if destIsDir {
				destination = filepath.Join(workdir, destination)

				// special case: only add trailing slash if not already in root
				if destination != "/" {
					destination = destination + "/"
				}
			} else {
				destination = filepath.Join(workdir, destination)
			}
		}

		cpType := CopyTypeBuilder
		if !slices.Contains(stageNames, from) {
			cpType = CopyTypeExternal
		}

		return &Copy{
			From:        from,
			Sources:     sources,
			Destination: destination,
			Type:        cpType,
		}, nil
	}

	return nil, nil
}

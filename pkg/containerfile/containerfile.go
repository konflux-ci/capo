package containerfile

import (
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strconv"
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

// A mount reference from a RUN --mount instruction in a Containerfile stage.
type Mount struct {
	// Alias of the stage the mount references when Mount.Type==MountTypeBuilder
	// or a pullspec when Mount.Type==MountTypeExternal
	From string
	// Type of the mount. Specifies whether it references a builder stage
	// or an external image directly.
	Type MountType
}

type MountType int

const (
	MountTypeBuilder MountType = iota
	MountTypeExternal
)

// A builder or final stage in a Containerfile
type Stage struct {
	// Alias of the builder stage or equal to FinalStage if final
	Alias string
	// Base image for the stage. Can be a pullspec, "scratch", or "oci:archive".
	Base string
	// Builder copies in this stage
	Copies []Copy
	// Mount references in this stage
	Mounts []Mount
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

	pullspecs := resolvePullspecs(rawStages)
	stageNames := make([]string, 0)

	for i, s := range rawStages {
		stageNames = append(stageNames, s.Name)
		if i == len(rawStages)-1 {
			s.Name = FinalStage
		}

		copies, mounts, err := parseStageRefs(s, stageNames)
		if err != nil {
			return res, err
		}

		res = append(res, Stage{
			Alias:  s.Name,
			Base:   pullspecs[i],
			Copies: copies,
			Mounts: mounts,
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

// resolvePullspecs returns the base image pullspec for each stage, in order.
func resolvePullspecs(stages []imagebuilder.Stage) []string {
	res := make([]string, 0, len(stages))

	for _, s := range stages {
		headingEnv := argsMapToSlice(s.Builder.HeadingArgs)
		userEnv := argsMapToSlice(s.Builder.Args)
		env := append(headingEnv, userEnv...)

		fromNode := s.Node.Children[0]
		pullspec, _ := imagebuilder.ProcessWord(fromNode.Next.Value, env)
		res = append(res, pullspec)
	}

	return res
}

// parseStageRefs parses the AST for the passed imagebuilder.Stage and
// returns slices of Copy and Mount structs found in the stage.
//
// A COPY command is builder-type if the "--from" flag is specified and it copies from
// a builder stage or directly from an image.
// A Mount is extracted from RUN --mount instructions that specify a "from" option.
// Uses the passed previous stageNames to specify whether references are to a stage
// or directly to an image.
//
// WORKDIR commands are taken into account and the destinations of COPY commands
// are resolved to be absolute instead of relative, where needed. If the Containerfile
// contains builder-type COPY commands that copy to a relative destination and don't
// specify the WORKDIR in advance, parseStageRefs returns a workdirError.
// This limitation exists because each base image can set its own WORKDIR and this cannot
// be determined based on just the Containerfile.
//
// WARNING: named contexts in the Containerfile are not supported
func parseStageRefs(s imagebuilder.Stage, stageNames []string) ([]Copy, []Mount, error) {
	copies := make([]Copy, 0)
	mounts := make([]Mount, 0)
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
					return copies, mounts, fmt.Errorf("%w: %q", ErrAmbiguousRelativePath, child.Original)
				}

				workdir = filepath.Join(workdir, dirPath)
			}

		case "copy":
			cp, err := parseCopy(child, workdir, env, stageNames)
			if err != nil {
				return copies, mounts, err
			}

			if cp != nil {
				copies = append(copies, *cp)
			}

		case "run":
			mounts = append(mounts, parseMounts(child, env, stageNames)...)
		}
	}

	return copies, mounts, nil
}

// isStageRef returns true if ref matches a known stage, either by name or by
// numeric index.
func isStageRef(ref string, stageNames []string) bool {
	if slices.Contains(stageNames, ref) {
		return true
	}
	if i, err := strconv.Atoi(ref); err == nil && 0 <= i && i < len(stageNames) {
		return true
	}
	return false
}

// parseMounts extracts Mount references from a RUN instruction's --mount flags.
// Only mounts with a "from" option are returned. Uses the passed previous stage
// names to classify whether the mount references a builder stage or an external image.
func parseMounts(node *parser.Node, env []string, stageNames []string) []Mount {
	var mounts []Mount
	for _, fl := range node.Flags {
		if !strings.HasPrefix(fl, "--mount=") {
			continue
		}

		mountOpts := strings.TrimPrefix(fl, "--mount=")
		from := ""
		for opt := range strings.SplitSeq(mountOpts, ",") {
			if val, ok := strings.CutPrefix(opt, "from="); ok {
				from, _ = imagebuilder.ProcessWord(val, env)
				break
			}
		}

		if from == "" {
			continue
		}

		mountType := MountTypeBuilder
		if !isStageRef(from, stageNames) {
			mountType = MountTypeExternal
		}

		mounts = append(mounts, Mount{
			From: from,
			Type: mountType,
		})
	}
	return mounts
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
		if !isStageRef(from, stageNames) {
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

// Package containerfile parses Containerfiles (Dockerfiles) into a structured
// representation of build stages, COPY commands, and RUN --mount references.
package containerfile

import (
	"errors"
	"fmt"
	"io"
	"maps"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/openshift/imagebuilder"
	"github.com/openshift/imagebuilder/dockerfile/parser"
)

// FinalStage is the sentinel alias assigned to the last stage in a parsed
// Containerfile, distinguishing it from named builder stages.
var FinalStage string = ""

// CopyType classifies a COPY command by its source origin.
type CopyType int

const (
	// CopyTypeBuilder indicates a COPY from a previous builder stage.
	CopyTypeBuilder CopyType = iota
	// CopyTypeExternal indicates a COPY directly from an external image.
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

	// Current working directory for resolving relative paths in this COPY
	// command.
	// Is empty if the containerfile does not explicitly set a working
	// directory before the COPY command.
	// If it's relative, it's always relative to the base working directory in
	// the stage the COPY command appeared in.
	Workdir string
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

// MountType classifies a RUN --mount reference by its source origin.
type MountType int

const (
	// MountTypeBuilder indicates a mount from a previous builder stage.
	MountTypeBuilder MountType = iota
	// MountTypeExternal indicates a mount directly from an external image.
	MountTypeExternal
)

// A builder or final stage in a Containerfile
// TODO: encase this in a containerfile struct?
type Stage struct {
	// Alias of the builder stage or equal to FinalStage if final.
	Alias string
	// Base image for the stage. Can be a pullspec, "scratch", or "oci-archive".
	Base string
	// Builder copies in this stage in order (top to bottom in the containerfile).
	Copies []Copy
	// Mount references in this stage.
	Mounts []Mount
	// Labels set via LABEL instructions in this stage.
	Labels map[string]string
}

// BuildOptions controls how a Containerfile is parsed.
type BuildOptions struct {
	// Build arguments passed to buildah for the build
	Args map[string]string
	// Target stage of the buildah build
	Target string
}

// ErrTargetNotFound is returned when the target stage specified in
// BuildOptions does not exist in the Containerfile.
var ErrTargetNotFound = errors.New("specified target stage was not found in the containerfile")

// ErrParse is returned when the Containerfile cannot be parsed.
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

	pullspecs, err := resolvePullspecs(rawStages)
	if err != nil {
		return nil, err
	}
	stageNames := make([]string, 0)

	for i, s := range rawStages {
		stageNames = append(stageNames, s.Name)
		if i == len(rawStages)-1 {
			s.Name = FinalStage
		}

		stage, err := parseStage(s, pullspecs[i], stageNames)
		if err != nil {
			return res, err
		}

		res = append(res, stage)
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
func resolvePullspecs(stages []imagebuilder.Stage) ([]string, error) {
	res := make([]string, 0, len(stages))

	for _, s := range stages {
		headingEnv := argsMapToSlice(s.Builder.HeadingArgs)
		userEnv := argsMapToSlice(s.Builder.Args)
		env := append(headingEnv, userEnv...)

		fromNode := s.Node.Children[0]
		pullspec, err := imagebuilder.ProcessWord(fromNode.Next.Value, env)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrParse, err)
		}
		res = append(res, pullspec)
	}

	return res, nil
}

// parseStage parses the AST for the passed imagebuilder.Stage and returns a
// Stage struct with its copies, mounts, and labels populated.
//
// A COPY command is builder-type if the "--from" flag is specified and it copies from
// a builder stage or directly from an image.
// A Mount is extracted from RUN --mount instructions that specify a "from" option.
// Uses the passed previous stageNames to specify whether references are to a stage
// or directly to an image.
// WARNING: named contexts in the Containerfile are not supported
func parseStage(s imagebuilder.Stage, pullspec string, stageNames []string) (Stage, error) {
	copies := make([]Copy, 0)
	mounts := make([]Mount, 0)
	labels := make(map[string]string)
	workdir := ""
	headingEnv := argsMapToSlice(s.Builder.HeadingArgs)
	userEnv := argsMapToSlice(s.Builder.Args)

	// user provided args override the heading ARGs,
	// so they're appended second to take priority
	env := append(headingEnv, userEnv...)

	stage := Stage{
		Alias: s.Name,
		Base:  pullspec,
	}

	for _, child := range s.Node.Children {
		switch child.Value {
		case "workdir":
			newWorkdir := child.Next.Value
			if filepath.IsAbs(newWorkdir) {
				workdir = newWorkdir
			} else {
				workdir = filepath.Join(workdir, newWorkdir)
			}

		case "copy":
			cp, err := parseCopy(child, workdir, env, stageNames)
			if err != nil {
				return stage, err
			}

			if cp != nil {
				copies = append(copies, *cp)
			}

		case "run":
			runMounts, err := parseMounts(child, env, stageNames)
			if err != nil {
				return stage, err
			}
			mounts = append(mounts, runMounts...)

		case "label":
			parsed, err := parseLabels(child, env)
			if err != nil {
				return stage, err
			}
			maps.Copy(labels, parsed)
		}
	}

	stage.Copies = copies
	stage.Mounts = mounts
	stage.Labels = labels

	return stage, nil
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
func parseMounts(node *parser.Node, env []string, stageNames []string) ([]Mount, error) {
	mounts := make([]Mount, 0)
	for _, fl := range node.Flags {
		if !strings.HasPrefix(fl, "--mount=") {
			continue
		}

		mountOpts := strings.TrimPrefix(fl, "--mount=")
		from := ""
		for opt := range strings.SplitSeq(mountOpts, ",") {
			if val, ok := strings.CutPrefix(opt, "from="); ok {
				var err error
				from, err = imagebuilder.ProcessWord(val, env)
				if err != nil {
					return nil, fmt.Errorf("%w: %w", ErrParse, err)
				}
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
	return mounts, nil
}

// normalizeSources normalizes the paths in the passed sources slice to absolute clean paths.
// It also preserves trailing slash to directory paths.
func normalizeSources(sources []string) []string {
	normalizedPaths := make([]string, 0, len(sources))
	for _, s := range sources {
		isDir := strings.HasSuffix(s, "/")
		// In COPY --from, even if the source path looks relative,
		// it is resolved from '/' workdir. To make the path resolution
		// unambiguous we prepend it with the slash. Join also cleans the path.
		s = filepath.Join("/", s)
		if isDir {
			s += "/"
		}
		normalizedPaths = append(normalizedPaths, s)
	}
	return normalizedPaths
}

// parseCopy takes a raw dockerfile parser Node and optionally returns a pointer
// to a parsed Copy struct.
// Returns (nil, nil) if the COPY command is not builder-type, but copies from a context.
// Sets the workdir of the Copy to the passed workdir.
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
		from, err := imagebuilder.ProcessWord(strings.TrimPrefix(fl, "--from="), env)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrParse, err)
		}

		// aggregate the COPY arguments by iterating the nodes
		args := make([]string, 0)
		curr := node.Next
		for curr != nil {
			args = append(args, curr.Value)
			curr = curr.Next
		}

		sources := args[:len(args)-1]
		sources = normalizeSources(sources)

		destination := args[len(args)-1]

		cpType := CopyTypeBuilder
		if !isStageRef(from, stageNames) {
			cpType = CopyTypeExternal
		}

		return &Copy{
			From:        from,
			Sources:     sources,
			Destination: destination,
			Type:        cpType,
			Workdir:     workdir,
		}, nil
	}

	return nil, nil
}

func parseLabels(node *parser.Node, env []string) (map[string]string, error) {
	labels := make(map[string]string)
	curr := node.Next
	for curr != nil && curr.Next != nil {
		key, err := imagebuilder.ProcessWord(curr.Value, env)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrParse, err)
		}
		val, err := imagebuilder.ProcessWord(curr.Next.Value, env)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrParse, err)
		}
		labels[key] = val
		curr = curr.Next.Next
	}
	return labels, nil
}

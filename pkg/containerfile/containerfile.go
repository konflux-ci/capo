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
	// CopyTypeContext indicates a COPY from a named context.
	CopyTypeContext
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
	// Value of the --from field in the RUN command for bind and cache mount types.
	FromRaw string
	// Pullspec of an image used in the --from field, if the value is a reference
	// to an image, empty otherwise. Note that the pullspec may be incomplete,
	// it is directly copied from the FROM instruction in the stage where relevant.
	Pullspec string
	// Type of the mount as specified in the RUN --mount instruction.
	MountType MountType
}

// MountType classifies a RUN --mount instruction by its type.
type MountType int

// See values https://docs.docker.com/reference/dockerfile/#run---mount
const (
	MountTypeBind MountType = iota
	MountTypeCache
	MountTypeTmpfs
	MountTypeSecret
	MountTypeSSH
)

// Containerfile is a parsed representation of a Containerfile (Dockerfile),
// containing all build stages in order.
type Containerfile struct {
	// Stages in the containerfile, in order. The last stage is the final
	// (output) stage.
	Stages []Stage
}

// Return a stage by its name (alias) or numerical (index) reference. Return
// nil if it was not found.
func (c Containerfile) StageByRef(ref string) *Stage {
	i, err := strconv.Atoi(ref)
	if err == nil {
		return c.StageByIndex(i)
	}

	for _, st := range c.Stages {
		if st.Alias == ref {
			return &st
		}
	}

	return nil
}

// Return a stage by its index or nil if the index is out of bounds.
func (c Containerfile) StageByIndex(index int) *Stage {
	if index >= 0 && index < len(c.Stages) {
		return &c.Stages[index]
	}
	return nil
}

// Return a slice of builder stages (all stages except the final).
func (c Containerfile) BuilderStages() []Stage {
	if len(c.Stages) == 0 {
		return nil
	}
	return c.Stages[:len(c.Stages)-1]
}

// A builder or final stage in a Containerfile.
type Stage struct {
	// Alias of the builder stage or equal to FinalStage if final.
	Alias string
	// Base image pullspec for this stage. For chained stages (FROM parent AS child),
	// this is resolved through the chain to the ultimate builder base image pullspec.
	Base string
	// Raw FROM reference as it appears in the Containerfile. Can be a pullspec
	// or a stage alias. For non-chained stages, BaseRef == Base.
	BaseRef string
	// Zero-based index of this builder stage. Final stage has Index -1.
	Index int
	// Builder copies in this stage in order (top to bottom in the containerfile).
	Copies []Copy
	// Mount references in this stage.
	Mounts []Mount
	// Labels set via LABEL instructions in this stage.
	Labels map[string]string
}

// BuildOptions controls how a Containerfile is parsed.
type BuildOptions struct {
	// Build arguments passed to buildah for the build.
	// Environment variable resolution for bare KEY args (without =) must be
	// done before passing args here (see buildvars.ParseAndMerge).
	Args map[string]string

	// Environment variables passed to the build.
	EnvVars map[string]string

	// Target stage of the buildah build
	Target string

	// Build contexts passed to the build.
	BuildContexts map[string]string
}

// ErrTargetNotFound is returned when the target stage specified in
// BuildOptions does not exist in the Containerfile.
var ErrTargetNotFound = errors.New("specified target stage was not found in the containerfile")

// ErrParse is returned when the Containerfile cannot be parsed.
var ErrParse = errors.New("error while parsing containerfile")

// Parse reads a Containerfile from the passed reader and uses the passed
// BuildOptions to parse the Containerfile into stages.
func Parse(reader io.Reader, opts BuildOptions) (Containerfile, error) {
	res := make([]Stage, 0)

	node, err := imagebuilder.ParseDockerfile(reader)
	if err != nil {
		return Containerfile{}, fmt.Errorf("%w: %w", ErrParse, err)
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
		return Containerfile{}, fmt.Errorf("%w: %w", ErrParse, err)
	}

	if opts.Target != "" {
		stagesTargeted, ok := rawStages.ThroughTarget(opts.Target)
		if !ok {
			return Containerfile{}, fmt.Errorf("%w: %s", ErrTargetNotFound, opts.Target)
		}
		rawStages = stagesTargeted
	}

	pullspecs, err := resolvePullspecs(rawStages)
	if err != nil {
		return Containerfile{}, err
	}
	stageNames := make([]string, 0)
	// maps stage alias to root base pullspec (resolved through chain)
	aliasToBase := make(map[string]string)

	for index, s := range rawStages {
		stageNames = append(stageNames, s.Name)

		alias := s.Name
		stageIndex := index
		if index == len(rawStages)-1 {
			alias = FinalStage
			stageIndex = -1
		}

		baseRef := pullspecs[index]
		base := baseRef
		// resolve chained stages: if baseRef is an alias of a previous stage,
		// use its already-resolved root base pullspec
		if resolvedBase, isChained := aliasToBase[baseRef]; isChained {
			base = resolvedBase
		}
		aliasToBase[alias] = base

		contextNames := slices.Collect(maps.Keys(opts.BuildContexts))
		stage, err := parseStage(s, alias, base, baseRef, stageIndex, stageNames, opts.EnvVars, contextNames)
		if err != nil {
			return Containerfile{Stages: res}, err
		}

		res = append(res, stage)
	}

	return Containerfile{Stages: res}, nil
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
// Stage.
//
// Uses the passed previous stageNames to classify whether COPY --from and
// RUN --mount references point to a stage or directly to an image.
// WARNING: named contexts in the Containerfile are not supported
func parseStage(
	s imagebuilder.Stage,
	alias, base, baseRef string,
	index int,
	stageNames []string,
	envVars map[string]string,
	contextNames []string,
) (Stage, error) {
	copies := make([]Copy, 0)
	mounts := make([]Mount, 0)
	labels := make(map[string]string)
	workdir := ""
	// populate ENV, keep a map for keeping track of overrides
	envMap := make(map[string]string)
	maps.Copy(envMap, s.Builder.HeadingArgs)
	maps.Copy(envMap, s.Builder.Args)
	maps.Copy(envMap, envVars)

	// Env variables compiled to a format understandable by imagebuilder.ProcessWord
	env := argsMapToSlice(envMap)

	for _, child := range s.Node.Children {
		switch child.Value {
		case "workdir":
			newWorkdir, err := imagebuilder.ProcessWord(child.Next.Value, env)
			if err != nil {
				return Stage{}, fmt.Errorf("%w: %w", ErrParse, err)
			}
			if filepath.IsAbs(newWorkdir) {
				workdir = newWorkdir
			} else {
				workdir = filepath.Join(workdir, newWorkdir)
			}

		case "copy":
			cp, err := parseCopy(child, workdir, env, stageNames, contextNames)
			if err != nil {
				return Stage{}, err
			}

			if cp != nil {
				copies = append(copies, *cp)
			}

		case "run":
			runMounts, err := parseMounts(child, env, stageNames)
			if err != nil {
				return Stage{}, err
			}
			mounts = append(mounts, runMounts...)

		case "label":
			parsed, err := parseLabels(child, env)
			if err != nil {
				return Stage{}, err
			}
			maps.Copy(labels, parsed)

		case "env":
			// the env should respect overwrites and should only update
			// once per instruction. See the spec for more details:
			// https://docs.docker.com/reference/dockerfile/#environment-replacement
			parsed, err := parseEnv(child, env)
			if err != nil {
				return Stage{}, err
			}
			// Update map so overriding works as expected.
			maps.Copy(envMap, parsed)
			env = argsMapToSlice(envMap)
		}
	}

	return Stage{
		Alias:   alias,
		Base:    base,
		BaseRef: baseRef,
		Index:   index,
		Copies:  copies,
		Mounts:  mounts,
		Labels:  labels,
	}, nil
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
		mount, err := parseMount(strings.TrimPrefix(fl, "--mount="), env, stageNames)
		if err != nil {
			return nil, err
		}
		if mount != nil {
			mounts = append(mounts, *mount)
		}
	}
	return mounts, nil
}

// parseMount parses a single --mount option string (without the --mount= prefix)
// and returns a Mount if it is a bind mount with a from reference, or nil otherwise.
func parseMount(mountOpts string, env []string, stageNames []string) (*Mount, error) {
	var from, buildahMountTypeStr, pullspec string
	for opt := range strings.SplitSeq(mountOpts, ",") {
		if from == "" {
			if val, ok := strings.CutPrefix(opt, "from="); ok {
				var err error
				from, err = imagebuilder.ProcessWord(val, env)
				if err != nil {
					return nil, fmt.Errorf("%w: %w", ErrParse, err)
				}
				continue
			}
		}
		if buildahMountTypeStr == "" {
			if val, ok := strings.CutPrefix(opt, "type="); ok {
				buildahMountTypeStr = val
				continue
			}
		}
	}

	if !isStageRef(from, stageNames) {
		// populate pullspec only if it is not a stage reference
		pullspec = from
	}

	var mountType MountType
	switch buildahMountTypeStr {
	case "bind", "": // "bind" is the default in Buildah.
		mountType = MountTypeBind
	case "cache":
		mountType = MountTypeCache
	case "tmpfs":
		mountType = MountTypeTmpfs
	case "secret":
		mountType = MountTypeSecret
	case "ssh":
		mountType = MountTypeSSH
	default:
		return nil, fmt.Errorf("%w: invalid buildah mount type: %s", ErrParse, buildahMountTypeStr)
	}

	return &Mount{
		FromRaw:   from,
		Pullspec:  pullspec,
		MountType: mountType,
	}, nil
}

// normalizeSources normalizes the paths in the passed sources slice to absolute clean paths.
// It also preserves trailing slash to directory paths and expands environment variables.
func normalizeSources(sources []string, env []string) ([]string, error) {
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
		expandedPath, err := imagebuilder.ProcessWord(s, env)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrParse, err)
		}
		normalizedPaths = append(normalizedPaths, expandedPath)
	}
	return normalizedPaths, nil
}

// parseCopy takes a raw dockerfile parser Node and optionally returns a pointer
// to a parsed Copy struct.
// Returns (nil, nil) if the COPY command is not builder-type, but copies from a context.
// Sets the workdir of the Copy to the passed workdir.
// Uses the passed env to evaluate arguments in the COPY.
// Uses the passed previous stage names to evaluate whether this COPY command is from
// a builder stage or directly from an external image.
// Uses the passed build context names to determine if the COPY command is
// copying from a named build context.
func parseCopy(node *parser.Node, workdir string, env []string,
	stageNames []string, contextNames []string) (*Copy, error) {
	for _, fl := range node.Flags {
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
		sources, err = normalizeSources(sources, env)
		if err != nil {
			return nil, err
		}

		destination, err := imagebuilder.ProcessWord(args[len(args)-1], env)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrParse, err)
		}

		// Determine if copying from a builder stage, an external image, or a
		// named context
		cpType := CopyTypeExternal
		if slices.Contains(contextNames, from) {
			cpType = CopyTypeContext
		} else if isStageRef(from, stageNames) {
			cpType = CopyTypeBuilder
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

// parseKeyValue is a helper function that parses key-value pairs from a parent node.
// It iterates over two nodes at the same time - key and value.
func parseKeyValue(node *parser.Node, env []string) (map[string]string, error) {
	result := make(map[string]string)
	// iterate over two nodes at the same time - key and value
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
		result[key] = val
		curr = curr.Next.Next
	}
	return result, nil
}

func parseLabels(node *parser.Node, env []string) (map[string]string, error) {
	return parseKeyValue(node, env)
}

// parseEnv parses an ENV instruction and returns a map of names and values.
// It does no changes to the passed env slice.
func parseEnv(node *parser.Node, env []string) (map[string]string, error) {
	return parseKeyValue(node, env)
}

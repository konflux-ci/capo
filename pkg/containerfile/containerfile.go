package containerfile

import (
	"fmt"
	"io"
	"strings"

	"github.com/openshift/imagebuilder"
)

var FinalStage string = ""

// A builder-type COPY command in a Containerfile stage.
// A COPY is builder-type if it copies from a previous builder stage
// or directly from an image.
type Copy struct {
	// Sources in the command.
	Sources     []string
	// Destination in the command.
	Destination string
	// Alias of the stage the command is copying from in the case
	// of stage copies or a pullspec when copying directly from an image.
	From        string
}

// A builder or final stage in a Containerfile
type Stage struct {
	// Alias of the builder stage or equal to FinalStage if final
	Alias    string
	// Base image for the stage
	Pullspec string
	// Builder copies in this stage
	Copies   []Copy
}

type BuildOptions struct {
	// Build arguments passed to buildah for the build
	Args   map[string]string
	// Target stage of the buildah build
	Target string
}

// Parse reads a Containerfile from the passed reader and uses the passed
// BuildOptions to parse the Containerfile into stages.
func Parse(reader io.Reader, opts BuildOptions) (res []Stage, err error) {
	node, err := imagebuilder.ParseDockerfile(reader)
	if err != nil {
		return res, err
	}

	// TODO: At this stage, Buildah code takes into account OS and ARCH CLI args
	// and overrides the built-in TARGETOS and TARGETARCH args (and others).
	// In Konflux build, this is currently not supported but I'm keeping this here
	// as a guideline.
	// https://github.com/containers/buildah/blob/main/imagebuildah/build.go#L431

	builder := imagebuilder.NewBuilder(opts.Args)
	rawStages, err := imagebuilder.NewStages(node, builder)
	if err != nil {
		return res, err
	}

	if opts.Target != "" {
		stagesTargeted, ok := rawStages.ThroughTarget(opts.Target)
		if !ok {
			return res, fmt.Errorf("the target %q was not found in the provided Containerfile", opts.Target)
		}
		rawStages = stagesTargeted
	}

	aliasToPullspec := mapAliasesToPullspecs(rawStages)

	for i, s := range rawStages {
		stageName := s.Name
		if i == len(rawStages)-1 {
			stageName = FinalStage
		}

		res = append(res, Stage{
			Alias:    stageName,
			Pullspec: aliasToPullspec[s.Name],
			Copies:   getBuilderCopiesInStage(s),
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
func mapAliasesToPullspecs(stages []imagebuilder.Stage) (res map[string]string) {
	res = make(map[string]string)

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
// WARNING: named contexts in the Containerfile are not supported
func getBuilderCopiesInStage(s imagebuilder.Stage) []Copy {
	copies := make([]Copy, 0)
	headingEnv := argsMapToSlice(s.Builder.HeadingArgs)
	userEnv := argsMapToSlice(s.Builder.Args)

	// user provided args override the heading ARGs,
	// so they're appended second to take priority
	env := append(headingEnv, userEnv...)

	for _, child := range s.Node.Children {
		if child.Value != "copy" {
			continue
		}

		for _, fl := range child.Flags {
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
			curr := child.Next
			for {
				if curr == nil {
					break
				}

				args = append(args, curr.Value)
				curr = curr.Next
			}

			copies = append(copies, Copy{
				From:        from,
				Sources:     args[:len(args)-1],
				Destination: args[len(args)-1],
			})
		}
	}

	return copies
}

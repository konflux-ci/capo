package containerfile

import (
	"fmt"
	"io"
	"strings"

	"github.com/openshift/imagebuilder"
)

var FinalStage string = ""

type Copy struct {
	Sources     []string
	Destination string
	From        string
}

type Stage struct {
	Alias    string
	Pullspec string
	Copies   []Copy
}

type BuildOptions struct {
	Args   map[string]string
	Target string
}

// Parse takes the path to a dockerfile-json output file and
// parses it into stages.
func Parse(reader io.Reader, opts BuildOptions) (res []Stage, err error) {
	node, err := imagebuilder.ParseDockerfile(reader)
	if err != nil {
		return res, err
	}

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

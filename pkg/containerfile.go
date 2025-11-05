package capo

import (
	"fmt"
	"io"
	"strings"

	"github.com/openshift/imagebuilder"
)

var FinalStage string = ""

type copy struct {
	sources     []string
	destination string
	from        string
}

type cfileStageId struct {
	// alias of this stage or empty for final stage
	alias string
	// pullspec of this stage or empty for final stage
	pullspec string
}

type cfileStage struct {
	id cfileStageId
	// slice of copy commands in this stage
	copies []copy
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

type BuildOptions struct {
	Args   map[string]string
	Target string
}

// ParseContainerfile takes the path to a dockerfile-json output file and
// parses it into stages.
func ParseContainerfile(reader io.Reader, opts BuildOptions) (stages []Stage, err error) {
	cfileStages, err := parseContainerfileStages(reader, opts)
	if err != nil {
		return []Stage{}, nil
	}

	aliasToStage := make(map[string]cfileStage)
	for _, st := range cfileStages[:len(cfileStages)-1] {
		aliasToStage[st.id.alias] = st
	}

	final := cfileStages[len(cfileStages)-1]
	stageIdToSources := make(map[cfileStageId][]string)
	for _, cp := range final.copies {
		for _, source := range cp.sources {

			// the copy is builder type only if there's no builder stage with alias equal to the cp.from
			// otherwise the cp.from is a pullspec and it is an external copy
			if _, isBuilder := aliasToStage[cp.from]; isBuilder {
				traceSource(source, aliasToStage[cp.from], stageIdToSources, aliasToStage)
			} else {
				externalId := cfileStageId{alias: "", pullspec: cp.from}
				stageIdToSources[externalId] = append(stageIdToSources[externalId], source)
			}
		}
	}

	// construct builder stages
	for _, cfileStage := range cfileStages[:len(cfileStages)-1] {
		stages = append(stages, stage{
			alias:    cfileStage.id.alias,
			pullspec: cfileStage.id.pullspec,
			sources:  stageIdToSources[cfileStage.id],
		})

		// the processed stageId must be deleted from the accumulator so the
		// accumulator only contains "external" stages after builder stages are constructed.
		// These are then processed in the next code block below.
		delete(stageIdToSources, cfileStage.id)
	}

	// construct "external" stages
	for id, sources := range stageIdToSources {
		stages = append(stages, stage{
			alias:    id.alias,
			pullspec: id.pullspec,
			sources:  sources,
		})
	}

	return stages, nil
}

func traceSource(
	source string,
	currStage cfileStage,
	acc map[cfileStageId][]string,
	aliasToStage map[string]cfileStage,
) {
	isDirectory := strings.HasSuffix(source, "/")

	foundAncestor := false
	for _, cp := range currStage.copies {
		if strings.HasPrefix(cp.destination, source) {
			foundAncestor = true
			for _, s := range cp.sources {
				traceSource(s, aliasToStage[cp.from], acc, aliasToStage)
			}
		}
	}

	// If the source is a directory, we want to add it to the accumulator
	// even if we traced some of the sources. This is because the directory could
	// contain mixed content - some from this stage, some copied from previous stages.
	if isDirectory || !foundAncestor {
		acc[currStage.id] = append(acc[currStage.id], source)
	}
}

func parseContainerfileStages(reader io.Reader, opts BuildOptions) (res []cfileStage, err error) {
	node, err := imagebuilder.ParseDockerfile(reader)

	builder := imagebuilder.NewBuilder(opts.Args)

	stages, err := imagebuilder.NewStages(node, builder)
	if err != nil {
		return res, err
	}

	if opts.Target != "" {
		stagesTargeted, ok := stages.ThroughTarget(opts.Target)
		if !ok {
			return res, fmt.Errorf("the target %q was not found in the provided Containerfile", opts.Target)
		}
		stages = stagesTargeted
	}

	aliasToPullspec := mapAliasesToPullspecs(stages)

	for i, s := range stages {
		stageName := s.Name
		if i == len(stages)-1 {
			stageName = FinalStage
		}

		res = append(res, cfileStage{
			id: cfileStageId{
				alias:    stageName,
				pullspec: aliasToPullspec[s.Name],
			},
			copies: getBuilderCopiesInStage(s, opts.Args),
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

func getBuilderCopiesInStage(s imagebuilder.Stage, args map[string]string) (copies []copy) {
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

			copies = append(copies, copy{
				from:        from,
				sources:     args[:len(args)-1],
				destination: args[len(args)-1],
			})
		}
	}

	return copies
}

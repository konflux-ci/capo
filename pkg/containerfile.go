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

// ParseContainerfile takes the path to a dockerfile-json output file and
// parses it into stages.
func ParseContainerfile(reader io.Reader) (stages []Stage, err error) {
	cfileStages, err := parseContainerfileStages(reader)
	if err != nil {
		return []Stage{}, nil
	}

	fmt.Printf("%+v\n", cfileStages)

	aliasToStage := make(map[string]cfileStage)
	for _, st := range cfileStages[:len(cfileStages)-1] {
		aliasToStage[st.id.alias] = st
	}

	return stages, nil
}

func parseContainerfileStages(reader io.Reader) (res []cfileStage, err error) {
	node, err := imagebuilder.ParseDockerfile(reader)

	// TODO: make sure to deal with build args and env shit once this kind of works
	builder := imagebuilder.NewBuilder(map[string]string{})

	stages, err := imagebuilder.NewStages(node, builder)
	if err != nil {
		return res, err
	}

	aliasToPullspec := mapAliasesToPullspecs(stages)
	// TODO: deal with build targets too

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
			copies: getBuilderCopiesInStage(s),
		})
	}

	return res, nil
}

func mapAliasesToPullspecs(stages []imagebuilder.Stage) (res map[string]string) {
	res = make(map[string]string)
	// skip final stage
	for _, s := range stages[:len(stages)-1] {
		fromNode := s.Node.Children[0]
		res[s.Name] = fromNode.Next.Value
	}

	return res
}

func getBuilderCopiesInStage(s imagebuilder.Stage) (copies []copy) {
	for _, child := range s.Node.Children {
		if child.Value != "copy" {
			continue
		}

		// TODO: deal with named contexts somehow
		for _, fl := range child.Flags {
			// is having a from enough to qualify this as a builder copy?
			// it could also be a named context
			if !strings.HasPrefix(fl, "--from=") {
				continue
			}
			from := strings.TrimPrefix(fl, "--from=")

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

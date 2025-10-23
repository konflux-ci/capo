package capo

import (
	"capo/pkg/includer"
	"encoding/json"
	"os"
	"path"
)

const FinalStage string = ""

// TODO: create a pair of Containerfile and the resulting data structure as an example

// BuildData is a representation of COPY-ies from builder and external images.
// Parsed from the output of the dockerfile-json tool.
type BuildData struct {
	Builders  []includer.StageData
	Externals []includer.StageData
}

// Builder represents a named stage (AS <alias>) in the Containerfile.
type Builder struct {
	// StagePullspec of the builder image.
	StagePullspec string
	// Alias of the builder stage.
	StageAlias string
	// Slice of copies from this builder image.
	// NOT the copies in this builder stage
	StageCopies []includer.Copier
}

func (b Builder) Alias() string {
	return b.StageAlias
}

func (b Builder) Pullspec() string {
	return b.StagePullspec
}

func (b Builder) Copies() []includer.Copier {
	return b.StageCopies
}

// External represents an external image that is copied FROM in the Containerfile.
// E.g. "COPY --from=quay.io/konflux-ci/mobster:123 src/ dest/"
type External struct {
	// Pullspec of the external image.
	Pullspec string
	// Slice of copies from this external image.
	Copies []Copy
}

// Copy represents a COPY command, excepting copies from context (only external image and builder copies).
type Copy struct {
	Source []string
	Dest   string
	// Alias of the builder stage this COPY is found in or FinalStage if copying from final
	Stage string
}

func (c Copy) Sources() []string {
	return c.Source
}

func (c Copy) Destination() string {
	return c.Dest
}

func (c Copy) IsFromFinalStage() bool {
	return c.Stage == FinalStage
}

// The Index contains paths to partial SBOMs and metadata
// needed for contextualizatio by Mobster
type Index struct {
	Builder  []BuilderImage  `json:"builder"`
	External []ExternalImage `json:"external"`
}

// FIXME: this of better names for these result structs
type BuilderImage struct {
	Pullspec string `json:"pullspec"`
	// absolute path to the partial intermediate layer SBOM for this image
	// if it's not present or is empty, the image doesn't have any intermediate layer
	IntermediateSBOM string `json:"intermediate_sbom,omitempty"`
	// absolute path to the partial builder layer SBOM for this image
	BuilderSBOM string `json:"builder_sbom"`
}

type ExternalImage struct {
	Pullspec string `json:"pullspec"`
	// absolute path to the partial SBOM for external content from this image
	SBOM string `json:"sbom"`
}

func (i *Index) Write(output string) (string, error) {
	iPath := path.Join(output, "index.json")
	f, err := os.Create(iPath)
	if err != nil {
		return iPath, err
	}

	encoder := json.NewEncoder(f)
	return iPath, encoder.Encode(i)
}

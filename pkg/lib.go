package capo

import (
	"capo/pkg/includer"
	"encoding/json"
	"os"
	"path"
)

const FinalStage string = ""

// TODO: create a pair of Containerfile and the resulting data structure as an example

// ParsedContainerfile is a representation of COPY-ies from builder and external images.
// Parsed from the output of the dockerfile-json tool.
type ParsedContainerfile struct {
	Builders  []includer.StageData
	Externals []includer.StageData
}

// Stage can represent a named builder stage (AS <alias>) or an
// "external" stage in the Containerfile.
// The external stage does not have an equivalent in the Containerfile,
// but we can treat all copies from an external image as a virtual stage for simplicity.
type Stage struct {
	pullspec string
	alias    string
	// Slice of copies from this builder image.
	// NOT the copies in this builder stage
	copies []includer.Copier
}

func NewStage(alias string, pullspec string, copies []includer.Copier) includer.StageData {
	return Stage{
		alias:    alias,
		pullspec: pullspec,
		copies:   copies,
	}
}

func (b Stage) Alias() string {
	return b.alias
}

func (b Stage) Pullspec() string {
	return b.pullspec
}

func (b Stage) Copies() []includer.Copier {
	return b.copies
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
// needed for contextualization by Mobster.
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

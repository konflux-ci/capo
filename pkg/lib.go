package capo

import (
	"capo/pkg/includer"
	"encoding/json"
	"os"
	"path"
)

const FinalStage string = ""

// ParsedContainerfile is a representation of COPY-ies from builder and external images.
// Currently parsed from the output of the dockerfile-json tool.
type ParsedContainerfile struct {
	BuilderStages  []includer.Stage
	ExternalStages []includer.Stage
}

// Stage can represent a named builder stage (AS <alias>) or an
// "external" stage in the Containerfile.
// The external stage does not have an equivalent in the Containerfile,
// but we can treat all copies from an external image as a virtual stage for simplicity.
type stage struct {
	pullspec string
	alias    string
	// Slice of copies from this builder image.
	// NOT the copies in this builder stage
	copies []includer.Copier
}

func NewStage(alias string, pullspec string, copies []includer.Copier) includer.Stage {
	return stage{
		alias:    alias,
		pullspec: pullspec,
		copies:   copies,
	}
}

func (s stage) Alias() string {
	return s.alias
}

func (s stage) Pullspec() string {
	return s.pullspec
}

func (s stage) Copies() []includer.Copier {
	return s.copies
}

// Represents a COPY command, excepting copies from context (only external image and builder copies).
type copy struct {
	// sources of the COPY command
	sources []string
	// destination of the COPY command
	destination string
	// Alias of the builder stage this COPY is found in
	// or FinalStage if copying in final stage
	stage string
}

func NewCopy(sources []string, destination string, stage string) includer.Copier {
	return copy{
		sources:     sources,
		destination: destination,
		stage:       stage,
	}
}

func (c copy) Sources() []string {
	return c.sources
}

func (c copy) Destination() string {
	return c.destination
}

func (c copy) IsFromFinalStage() bool {
	return c.stage == FinalStage
}

// The Index contains paths to partial SBOMs and metadata
// needed for contextualization by Mobster.
type Index struct {
	Builder  []BuilderScanResult  `json:"builder"`
	External []ExternalScanResult `json:"external"`
}

type BuilderScanResult struct {
	Pullspec string `json:"pullspec"`
	// absolute path to the partial intermediate layer SBOM for this image
	// if it's not present or is empty, the image doesn't have any intermediate layer
	IntermediateSBOM string `json:"intermediate_sbom,omitempty"`
	// absolute path to the partial builder layer SBOM for this image
	BuilderSBOM string `json:"builder_sbom"`
}

type ExternalScanResult struct {
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
	defer f.Close()

	encoder := json.NewEncoder(f)
	encoder.SetIndent("", "  ")
	return iPath, encoder.Encode(i)
}

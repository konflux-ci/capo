package capo

import (
	"encoding/json"
	"os"
	"path"
)

// The Index contains paths to partial SBOMs and metadata
// needed for contextualization by Mobster.
type Index struct {
	Builder []ScanResult `json:"builder"`
}

type ScanResult struct {
	Pullspec string `json:"pullspec"`
	// absolute path to the partial intermediate layer SBOM for this image
	// if it's not present or is empty, the image doesn't have any intermediate layer
	IntermediateSBOM string `json:"intermediate_sbom,omitempty"`
	// absolute path to the partial builder layer SBOM for this image
	BuilderSBOM string `json:"builder_sbom"`
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

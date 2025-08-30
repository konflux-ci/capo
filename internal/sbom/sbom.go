package sbom

import (
	"context"
	"os"

	"github.com/anchore/syft/syft"
	"github.com/anchore/syft/syft/format/spdxjson"
	"github.com/anchore/syft/syft/source/sourceproviders"
	_ "modernc.org/sqlite" // required for Syft's RPM cataloguer
)

var sourceConfig = syft.DefaultGetSourceConfig().WithSources(sourceproviders.DirTag)

var createSBOMConfig = syft.DefaultCreateSBOMConfig()

var encoderConfig = spdxjson.DefaultEncoderConfig()

// Performs a syft scan on the root directory and saves a JSON SPDX SBOM
// to the passed destination.
func SyftScan(root string, destination string) error {
	ctx := context.Background()

	src, err := syft.GetSource(ctx, root, sourceConfig)
	if err != nil {
		return err
	}

	sbom, err := syft.CreateSBOM(ctx, src, createSBOMConfig)
	if err != nil {
		return err
	}

	encoder, err := spdxjson.NewFormatEncoderWithConfig(encoderConfig)
	if err != nil {
		return err
	}

	file, err := os.Create(destination)
	if err != nil {
		return err
	}
	defer file.Close()

	return encoder.Encode(file, *sbom)
}

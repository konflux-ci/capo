package capo

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"

	"github.com/Masterminds/semver/v3"
	"go.podman.io/storage"
)

const MinBuildahVersion = "1.44.0"

var ErrUnsupportedBuildahVersion = errors.New("unsupported buildah version")
var ErrNoConfigBlob = errors.New("no config blob found in image")
var ErrMissingStageLabel = errors.New("intermediate image is missing io.buildah.stage.name label")

type imageConfig struct {
	Config struct {
		Labels map[string]string `json:"Labels"`
	} `json:"config"`
}

// getImageLabels retrieves labels from an image's config.
func getImageLabels(store storage.Store, img *storage.Image) (map[string]string, error) {
	var configBlobName string
	for _, name := range img.BigDataNames {
		if strings.HasPrefix(name, "sha256:") {
			configBlobName = name
			break
		}
	}

	if configBlobName == "" {
		return nil, ErrNoConfigBlob
	}

	configData, err := store.ImageBigData(img.ID, configBlobName)
	if err != nil {
		return nil, fmt.Errorf("failed to get image config: %w", err)
	}

	var config imageConfig
	if err := json.Unmarshal(configData, &config); err != nil {
		return nil, fmt.Errorf("failed to unmarshal image config: %w", err)
	}

	return config.Config.Labels, nil
}

// findIntermediateImage looks up an intermediate image by stage alias.
// Iterates all unnamed images in the store, validates buildah version and
// stage label presence on each, but defers errors until the full iteration
// completes, so a valid match is returned even if other images in the
// store are invalid. Returns all accumulated errors only if no match
// is found.
func findIntermediateImage(store storage.Store, stageAlias string) (*storage.Image, bool, error) {
	images, err := store.Images()
	if err != nil {
		return nil, false, fmt.Errorf("failed to list images: %w", err)
	}

	var errs []error
	for _, img := range images {
		// Unnamed images are intermediate images
		if len(img.Names) != 0 {
			continue
		}

		labels, err := getImageLabels(store, &img)
		if err != nil {
			continue
		}

		// Check if the intermediate image was built with a supported buildah version.
		// Intermediate images built with old buildah versions will not contain
		// io.buildah.stage.name and io.buildah.stage.base labels.
		if err := checkBuildahVersionFromImage(labels); err != nil {
			errs = append(errs, fmt.Errorf(
				"intermediate image %s: %w, "+
					"ensure buildah >= %s is used for the build and consider using "+
					"a clean image storage to avoid interference from previous builds",
				img.ID, err, MinBuildahVersion,
			))
			continue
		}

		// If the intermediate image was built with correct buildah
		// version but has no stage name label, --stage-labels was not
		// used during the build.
		stageName, hasStageLabel := labels["io.buildah.stage.name"]
		if !hasStageLabel {
			errs = append(errs, fmt.Errorf(
				"intermediate image %s (buildah %s): %w, "+
					"make sure to pass --save-stages --stage-labels to the buildah build command",
				img.ID, labels["io.buildah.version"], ErrMissingStageLabel,
			))
			continue
		}

		if stageName == stageAlias {
			log.Printf("Found intermediate image %s for stage %q.", img.ID, stageAlias)
			return &img, true, nil
		}
	}

	if len(errs) > 0 {
		return nil, false, fmt.Errorf(
			"no intermediate image found for stage %q; encountered %d problematic image(s) in storage:\n%w",
			stageAlias, len(errs), errors.Join(errs...),
		)
	}
	log.Printf("No intermediate image found for stage %q. "+
		"This is expected if the stage has no filesystem-changing instructions. "+
		"If the stage does have such instructions, ensure the build was executed "+
		"with --save-stages --stage-labels flags.", stageAlias)
	return nil, false, nil
}

// checkBuildahVersionFromImage checks if the image was built with a supported buildah version.
// Fails if io.buildah.version label is missing or below MinBuildahVersion.
func checkBuildahVersionFromImage(labels map[string]string) error {
	buildahVersionStr, ok := labels["io.buildah.version"]
	if !ok {
		return fmt.Errorf("%w: io.buildah.version label not found in image config", ErrUnsupportedBuildahVersion)
	}

	buildahVersion, err := semver.NewVersion(buildahVersionStr)
	if err != nil {
		return fmt.Errorf("%w: could not parse buildah version %q: %w", ErrUnsupportedBuildahVersion, buildahVersionStr, err)
	}

	minVersion, _ := semver.NewVersion(MinBuildahVersion)
	coreVersion, _ := semver.NewVersion(
		fmt.Sprintf("%d.%d.%d", buildahVersion.Major(), buildahVersion.Minor(), buildahVersion.Patch()),
	)
	if coreVersion.LessThan(minVersion) {
		return fmt.Errorf(
			"%w: image was built with buildah %s, requires >= %s",
			ErrUnsupportedBuildahVersion, buildahVersionStr, MinBuildahVersion,
		)
	}

	return nil
}

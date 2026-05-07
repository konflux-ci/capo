/*
The content package uses a containers/storage Store to inspect built layers
and store partial content from the build, for later syft scanning.
*/
package capo

import (
	"archive/tar"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/Masterminds/semver/v3"
	"go.podman.io/storage"
	"go.podman.io/storage/pkg/archive"
)

type imageConfig struct {
	Config struct {
		Labels map[string]string `json:"Labels"`
	} `json:"config"`
}

const MinBuildahVersion = "1.44.0"

var ErrImageNotFound = errors.New("could not find image in buildah storage")
var ErrImageMount = errors.New("could not mount image")
var ErrIO = errors.New("IO operation failed")
var ErrStorage = errors.New("storage operation failed")
var ErrUnsupportedBuildahVersion = errors.New("unsupported buildah version")
var ErrNoConfigBlob = errors.New("no config blob found in image")
var ErrMissingStageLabel = errors.New("intermediate image is missing io.buildah.stage.name label")

// getContent uses the container store to extract partial content from the build
// for the specified package source. Extracts both builder base content and
// intermediate content (created during the build) for later syft scanning.
//
// Uses buildah stage labels (io.buildah.stage.name) to identify intermediate
// image for each stage.
func getContent(
	store storage.Store,
	pkgSource packageSource,
	builderContentPath string,
	intermediateContentPath string,
) error {
	imgId, err := store.Lookup(pkgSource.pullspec)
	if err != nil {
		return fmt.Errorf("%w: %q", ErrImageNotFound, pkgSource.pullspec)
	}
	img, _ := store.Image(imgId)

	intermediate, err := getIntermediateContent(store, img, pkgSource.alias, pkgSource.sources, intermediateContentPath)
	if err != nil {
		return err
	}

	if len(intermediate) == 0 {
		log.Printf("Found no intermediate content for %s.", pkgSource.pullspec)
	} else {
		log.Printf("Included intermediate content %+v for %s.", intermediate, pkgSource.pullspec)
	}

	builder, err := getImageContent(store, img, pkgSource.sources, builderContentPath)
	if err != nil {
		return err
	}
	log.Printf("Included builder content %+v for %s.", builder, pkgSource.pullspec)

	return nil
}


// IsPathUnderPattern reports whether path matches pattern exactly (via filepath.Match)
// or is a descendant of a directory matching pattern.
func isPathUnderPattern(pattern, path string) bool {
    pattern = filepath.Clean(pattern)
	path = filepath.Clean(path)

	if matched, _ := filepath.Match(pattern, path); matched {
		return true
	}

	patternParts := strings.Split(pattern, "/")
	pathParts := strings.Split(path, "/")
	if len(pathParts) > len(patternParts) {
		prefix := strings.Join(pathParts[:len(patternParts)], "/")
		if matched, _ := filepath.Match(pattern, prefix); matched {
			return true
		}
	}

	return false
}

func includes(sources []string, path string) bool {
	if !filepath.IsAbs(path) {
		path = "/" + path
	}

	for _, src := range sources {
		if isPathUnderPattern(src, path) {
			return true
		}
	}

	return false
}

func getImageContent(
	store storage.Store,
	image *storage.Image,
	sources []string,
	contentPath string,
) (included []string, err error) {
	mountPath, err := store.MountImage(image.ID, []string{}, "")
	if err != nil {
		return included, fmt.Errorf("%w: %w", ErrImageMount, err)
	}

	defer func() {
		if _, unmountErr := store.UnmountImage(image.ID, false); unmountErr != nil {
			err = fmt.Errorf("%w: failed to unmount image: %w", ErrStorage, unmountErr)
		}
	}()

	for _, src := range sources {
		full := path.Join(mountPath, src)
		matches, err := filepath.Glob(full)
		if err != nil {
			return included, fmt.Errorf("%w: failed to glob pattern %q: %w", ErrIO, src, err)
		}

		if len(matches) == 0 {
			continue
		}

		for _, match := range matches {
			fInfo, err := os.Stat(match)
			if err != nil {
				return included, fmt.Errorf("%w: failed to stat %q: %w", ErrIO, match, err)
			}

			relPath, err := filepath.Rel(mountPath, match)
			if err != nil {
				return included, fmt.Errorf("%w: failed to get relative path for %q: %w", ErrIO, match, err)
			}
			dest := path.Join(contentPath, relPath)

			if fInfo.IsDir() {
				// CopyFS also copies and follows symlinks even if they're outside the specified source,
				// This is not a problem for us because Syft ignores symbolic links.
				if err := os.CopyFS(dest, os.DirFS(match)); err != nil {
					return included, fmt.Errorf("%w: failed to copy directory %q to %q: %w", ErrIO, match, dest, err)
				}
			} else if fInfo.Mode().IsRegular() {
				if err := copyFile(match, dest); err != nil {
					return included, err
				}
			}

			included = append(included, "/"+relPath)
		}
	}

	return included, err
}

func copyFile(src string, dest string) (err error) {
	reader, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("%w: failed to open file %q: %w", ErrIO, src, err)
	}
	defer func() {
		err = reader.Close()
	}()

	if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
		return fmt.Errorf("%w: failed to create directory %q: %w", ErrIO, filepath.Dir(dest), err)
	}
	writer, err := os.Create(dest)
	if err != nil {
		return fmt.Errorf("%w: failed to create file %q: %w", ErrIO, dest, err)
	}
	defer func() {
		err = writer.Close()
	}()

	if _, err = io.Copy(writer, reader); err != nil {
		return fmt.Errorf("%w: failed to copy file content: %w", ErrIO, err)
	}
	return nil
}

// Stores intermediate content for the specified image to the path directory.
// Uses buildah stage labels (io.buildah.stage.name) to find the intermediate
// image for the given stage, then calculates a diff between the intermediate
// top layer and the builder base image top layer.
func getIntermediateContent(
	store storage.Store,
	builderImage *storage.Image,
	stageAlias string,
	sources []string,
	path string,
) ([]string, error) {
	builderLayer, err := store.Layer(builderImage.TopLayer)
	if err != nil {
		return []string{}, fmt.Errorf("%w: failed to get builder layer: %w", ErrStorage, err)
	}

	// Find intermediate image using buildah stage labels
	intermediateImage, found, err := findIntermediateImage(store, stageAlias)
	if err != nil {
		return []string{}, fmt.Errorf("%w: failed to find intermediate image: %w", ErrStorage, err)
	}
	if !found {
		// No intermediate image for this stage
		return []string{}, nil
	}

	interLayer, err := store.Layer(intermediateImage.TopLayer)
	if err != nil {
		return []string{}, fmt.Errorf("%w: failed to get intermediate layer: %w", ErrStorage, err)
	}

	included, err := saveDiff(store, path, interLayer.ID, builderLayer.ID, sources)
	if err != nil {
		return []string{}, err
	}

	return included, nil
}

func saveDiff(
	store storage.Store,
	dest string,
	layerId string,
	parentId string,
	sources []string,
) (included []string, err error) {
	compression := archive.Uncompressed
	opts := storage.DiffOptions{
		Compression: &compression,
	}

	diff, err := store.Diff(parentId, layerId, &opts)
	if err != nil {
		return []string{}, fmt.Errorf("%w: failed to compute layer diff: %w", ErrStorage, err)
	}
	defer func() {
		err = diff.Close()
	}()

	included = make([]string, 0, 16)
	reader := tar.NewReader(diff)
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return []string{}, fmt.Errorf("%w: failed to read tar header: %w", ErrIO, err)
		}

		if !includes(sources, header.Name) {
			continue
		}

		included = append(included, header.Name)

		target := filepath.Join(dest, header.Name)

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0755); err != nil {
				return []string{}, fmt.Errorf("%w: failed to create directory %q: %w", ErrIO, target, err)
			}
		case tar.TypeReg:
			// sometimes the archive does not have headers for directories
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return []string{}, fmt.Errorf("%w: failed to create directory %q: %w", ErrIO, filepath.Dir(target), err)
			}
			f, err := os.Create(target)
			if err != nil {
				return []string{}, fmt.Errorf("%w: failed to create file %q: %w", ErrIO, target, err)
			}

			if _, err := io.Copy(f, reader); err != nil {
				_ = f.Close()
				return []string{}, fmt.Errorf("%w: failed to copy file content: %w", ErrIO, err)
			}
			_ = f.Close()
		}
	}

	return included, nil
}

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
	for i := range images {
		if len(images[i].Names) != 0 {
			continue
		}

		labels, err := getImageLabels(store, &images[i])
		if err != nil {
			errs = append(errs, fmt.Errorf("getting labels for intermediate image %s: %w", images[i].ID, err))
			continue
		}

		if err := checkBuildahVersionFromImage(labels); err != nil {
			errs = append(errs, fmt.Errorf(
				"intermediate image %s: %w, "+
					"ensure buildah >= %s is used for the build and consider using "+
					"a clean image storage to avoid interference from previous builds",
				images[i].ID, err, MinBuildahVersion,
			))
			continue
		}

		stageName, hasStageLabel := labels["io.buildah.stage.name"]
		if !hasStageLabel {
			errs = append(errs, fmt.Errorf(
				"intermediate image %s (buildah %s): %w, "+
					"make sure to pass --save-stages --stage-labels to the buildah build command",
				images[i].ID, labels["io.buildah.version"], ErrMissingStageLabel,
			))
			continue
		}

		if stageName == stageAlias {
			log.Printf("Found intermediate image %s for stage %q.", images[i].ID, stageAlias)
			return &images[i], true, nil
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

func checkBuildahVersionFromImage(labels map[string]string) error {
	buildahVersionStr, ok := labels["io.buildah.version"]
	if !ok {
		return fmt.Errorf("%w: io.buildah.version label not found in image config", ErrUnsupportedBuildahVersion)
	}

	buildahVersion, err := semver.NewVersion(buildahVersionStr)
	if err != nil {
		return fmt.Errorf("%w: could not parse buildah version %q: %w", ErrUnsupportedBuildahVersion, buildahVersionStr, err)
	}

	// Accept prerelease versions (e.g. 1.43.0-dev) for integration tests build
	// buildah from a specific commit where --save-stages --stage-labels is present
	// but the version is not yet >= 1.44.0. See ISV-7179 for removal.
	if buildahVersion.Prerelease() != "" {
		return nil
	}

	minVersion, _ := semver.NewVersion(MinBuildahVersion)
	if buildahVersion.LessThan(minVersion) {
		return fmt.Errorf(
			"%w: image was built with buildah %s, requires >= %s",
			ErrUnsupportedBuildahVersion, buildahVersionStr, MinBuildahVersion,
		)
	}

	return nil
}

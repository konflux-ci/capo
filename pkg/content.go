/*
The content package uses a containers/storage Store to inspect built layers
and store partial content from the build, for later syft scanning.
*/
package capo

import (
	"archive/tar"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/Masterminds/semver/v3"
	"github.com/konflux-ci/capo/pkg/storageclient"
	"go.podman.io/storage"
	"go.podman.io/storage/pkg/archive"
)

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
	client storageclient.Client,
	pkgSource packageSource,
	builderContentPath string,
	intermediateContentPath string,
) error {
	isSpecialBase := storageclient.IsSpecialBase(pkgSource.pullspec)
	var builderImage *storage.Image

	if !isSpecialBase {
		// Special bases are not pullable or resolvable with Lookup
		imgId, err := store.Lookup(storageclient.StripTransport(pkgSource.pullspec))
		if err != nil {
			return fmt.Errorf("%w: %q", ErrImageNotFound, pkgSource.pullspec)
		}
		builderImage, err = store.Image(imgId)
		if err != nil {
			return fmt.Errorf("%w: %q", ErrImageNotFound, pkgSource.pullspec)
		}
	}

	// Special bases will have builderImage set as nil
	intermediate, err := getIntermediateContent(
		store,
		client,
		builderImage,
		pkgSource.alias,
		pkgSource.sources,
		intermediateContentPath,
	)

	if err != nil {
		return err
	}
	logContent("intermediate", intermediate, pkgSource.pullspec)

	if !isSpecialBase {
		// Only standard bases have builder content. All content in special bases is treated as intermediate.
		builderContent, err := getImageContent(store, builderImage, pkgSource.sources, builderContentPath)
		if err != nil {
			return err
		}
		logContent("builder", builderContent, pkgSource.pullspec)
	}

	return nil
}

func logContent(kind string, content []string, pullspec string) {
	if len(content) == 0 {
		log.Printf("Found no %s content for %s.", kind, pullspec)
	} else {
		log.Printf("Included %s content %+v for %s.", kind, content, pullspec)
	}
}

// IsPathUnderPattern reports whether path matches pattern exactly (via filepath.Match)
// or is a descendant of a directory matching pattern.
func isPathUnderPattern(pattern, path string) bool {
	pattern = filepath.Clean(pattern)
	path = filepath.Clean(path)

	if pattern == "/" {
		return true
	}

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
//
// When builderImage is nil (special bases like scratch or oci-archive), mounts
// the intermediate image and reads all matching content. Filesystem-transport
// bases are local files with non-pullable identifiers, so all their content
// is treated uniformly as intermediate.
func getIntermediateContent(
	store storage.Store,
	client storageclient.Client,
	builderImage *storage.Image,
	stageAlias string,
	sources []string,
	path string,
) ([]string, error) {
	// Find intermediate image using buildah stage labels
	intermediateImage, found, err := findIntermediateImage(store, client, stageAlias)
	if err != nil {
		return []string{}, fmt.Errorf("%w: failed to find intermediate image: %w", ErrStorage, err)
	}
	if !found {
		// No intermediate image for this stage
		return []string{}, nil
	}

	if builderImage == nil {
		// Scratch or unresolvable (special) bases
		return getImageContent(store, intermediateImage, sources, path)
	}

	builderLayer, err := store.Layer(builderImage.TopLayer)
	if err != nil {
		return []string{}, fmt.Errorf("%w: failed to get builder layer: %w", ErrStorage, err)
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

// findIntermediateImage looks up an intermediate image by stage alias.
// Iterates all unnamed images in the store, validates buildah version and
// stage label presence on each, but defers errors until the full iteration
// completes, so a valid match is returned even if other images in the
// store are invalid. Returns all accumulated errors only if no match
// is found.
func findIntermediateImage(
	store storage.Store, client storageclient.Client, stageAlias string,
) (*storage.Image, bool, error) {

	images, err := store.Images()
	if err != nil {
		return nil, false, fmt.Errorf("failed to list images: %w", err)
	}

	var errs []error
	for i := range images {
		if len(images[i].Names) != 0 {
			continue
		}

		cfg, err := client.GetImageConfig(images[i].ID)
		if err != nil {
			errs = append(errs, fmt.Errorf("getting image config for intermediate image %s: %w", images[i].ID, err))
			continue
		}

		if err := checkBuildahVersionFromImage(cfg.Config.Labels); err != nil {
			errs = append(errs, fmt.Errorf(
				"intermediate image %s: %w, "+
					"ensure buildah >= %s is used for the build and consider using "+
					"a clean image storage to avoid interference from previous builds",
				images[i].ID, err, MinBuildahVersion,
			))
			continue
		}

		stageName, hasStageLabel := cfg.Config.Labels["io.buildah.stage.name"]
		if !hasStageLabel {
			errs = append(errs, fmt.Errorf(
				"intermediate image %s (buildah %s): %w, "+
					"make sure to pass --save-stages --stage-labels to the buildah build command",
				images[i].ID, cfg.Config.Labels["io.buildah.version"], ErrMissingStageLabel,
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

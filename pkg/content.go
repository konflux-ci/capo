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

var ErrImageNotFound = errors.New("ERR_IMAGE_NOT_FOUND")
var ErrImageMount = errors.New("ERR_IMAGE_MOUNT")
var ErrIO = errors.New("ERR_IO")
var ErrStorage = errors.New("ERR_STORAGE")
var ErrUnsupportedBuildahVersion = errors.New("ERR_UNSUPPORTED_BUILDAH_VERSION")
var ErrMissingStageLabel = errors.New("ERR_MISSING_STAGE_LABEL")

// getContent uses the container store to extract partial content from the build
// for the specified package source. Extracts both builder base content and
// intermediate content (created during the build) for later syft scanning.
//
// Uses buildah stage labels (io.buildah.stage.name) to identify intermediate
// image for each stage.
func (s *Scanner) getContent(
	pkgSource packageSource,
	builderContentPath string,
	intermediateContentPath string,
) error {
	isSpecialBase := storageclient.IsSpecialBase(pkgSource.pullspec)
	var builderImage *storage.Image

	if !isSpecialBase {
		// Special bases are not pullable or resolvable with Lookup
		imgId, err := s.store.Lookup(storageclient.StripTransport(pkgSource.pullspec))
		if err != nil {
			return errorf(ErrImageNotFound, "could not find image %q in buildah storage", pkgSource.pullspec)
		}
		builderImage, err = s.store.Image(imgId)
		if err != nil {
			return errorf(ErrImageNotFound, "could not find image %q in buildah storage", pkgSource.pullspec)
		}
	}

	// Special bases will have builderImage set as nil
	intermediate, err := s.getIntermediateContent(
		builderImage,
		pkgSource.alias,
		pkgSource.sources,
		intermediateContentPath,
	)

	if err != nil {
		return err
	}
	s.logContent("intermediate", intermediate, pkgSource.pullspec)

	if !isSpecialBase {
		// Only standard bases have builder content. All content in special bases is treated as intermediate.
		builderContent, err := s.getImageContent(builderImage, pkgSource.sources, builderContentPath)
		if err != nil {
			return err
		}
		s.logContent("builder", builderContent, pkgSource.pullspec)
	}

	return nil
}

func (s *Scanner) logContent(kind string, content []string, pullspec string) {
	if len(content) == 0 {
		s.logger.Debug("found no content", "kind", kind, "pullspec", pullspec)
	} else {
		s.logger.Debug("included content", "kind", kind, "content", content, "pullspec", pullspec)
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

func (s *Scanner) getImageContent(
	image *storage.Image,
	sources []string,
	contentPath string,
) (included []string, err error) {
	mountPath, err := s.store.MountImage(image.ID, []string{}, "")
	if err != nil {
		return included, errorf(ErrImageMount, "could not mount image: %w", err)
	}

	defer func() {
		if _, unmountErr := s.store.UnmountImage(image.ID, false); unmountErr != nil {
			err = errorf(ErrStorage, "failed to unmount image: %w", unmountErr)
		}
	}()

	for _, src := range sources {
		full := path.Join(mountPath, src)
		matches, err := filepath.Glob(full)
		if err != nil {
			return included, errorf(ErrIO, "failed to glob pattern %q: %w", src, err)
		}

		if len(matches) == 0 {
			continue
		}

		for _, match := range matches {
			fInfo, err := os.Stat(match)
			if err != nil {
				return included, errorf(ErrIO, "failed to stat %q: %w", match, err)
			}

			relPath, err := filepath.Rel(mountPath, match)
			if err != nil {
				return included, errorf(ErrIO, "failed to get relative path for %q: %w", match, err)
			}
			dest := path.Join(contentPath, relPath)

			if fInfo.IsDir() {
				// CopyFS also copies and follows symlinks even if they're outside the specified source,
				// This is not a problem for us because Syft ignores symbolic links.
				if err := os.CopyFS(dest, os.DirFS(match)); err != nil {
					return included, errorf(ErrIO, "failed to copy directory %q to %q: %w", match, dest, err)
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
		return errorf(ErrIO, "failed to open file %q: %w", src, err)
	}
	defer func() {
		err = reader.Close()
	}()

	if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
		return errorf(ErrIO, "failed to create directory %q: %w", filepath.Dir(dest), err)
	}
	writer, err := os.Create(dest)
	if err != nil {
		return errorf(ErrIO, "failed to create file %q: %w", dest, err)
	}
	defer func() {
		err = writer.Close()
	}()

	if _, err = io.Copy(writer, reader); err != nil {
		return errorf(ErrIO, "failed to copy file content: %w", err)
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
func (s *Scanner) getIntermediateContent(
	builderImage *storage.Image,
	stageAlias string,
	sources []string,
	path string,
) ([]string, error) {
	// Find intermediate image using buildah stage labels
	intermediateImage, found, err := s.findIntermediateImage(stageAlias)
	if err != nil {
		return []string{}, errorf(ErrStorage, "failed to find intermediate image: %w", err)
	}
	if !found {
		// No intermediate image for this stage
		return []string{}, nil
	}

	if builderImage == nil {
		// Scratch or unresolvable (special) bases
		return s.getImageContent(intermediateImage, sources, path)
	}

	builderLayer, err := s.store.Layer(builderImage.TopLayer)
	if err != nil {
		return []string{}, errorf(ErrStorage, "failed to get builder layer: %w", err)
	}

	interLayer, err := s.store.Layer(intermediateImage.TopLayer)
	if err != nil {
		return []string{}, errorf(ErrStorage, "failed to get intermediate layer: %w", err)
	}

	included, err := s.saveDiff(path, interLayer.ID, builderLayer.ID, sources)
	if err != nil {
		return []string{}, err
	}

	return included, nil
}

func (s *Scanner) saveDiff(
	dest string,
	layerId string,
	parentId string,
	sources []string,
) (included []string, err error) {
	compression := archive.Uncompressed
	opts := storage.DiffOptions{
		Compression: &compression,
	}

	diff, err := s.store.Diff(parentId, layerId, &opts)
	if err != nil {
		return []string{}, errorf(ErrStorage, "failed to compute layer diff: %w", err)
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
			return []string{}, errorf(ErrIO, "failed to read tar header: %w", err)
		}

		if !includes(sources, header.Name) {
			continue
		}

		included = append(included, header.Name)

		target := filepath.Join(dest, header.Name)

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0755); err != nil {
				return []string{}, errorf(ErrIO, "failed to create directory %q: %w", target, err)
			}
		case tar.TypeReg:
			// sometimes the archive does not have headers for directories
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return []string{}, errorf(ErrIO, "failed to create directory %q: %w", filepath.Dir(target), err)
			}
			f, err := os.Create(target)
			if err != nil {
				return []string{}, errorf(ErrIO, "failed to create file %q: %w", target, err)
			}

			if _, err := io.Copy(f, reader); err != nil {
				_ = f.Close()
				return []string{}, errorf(ErrIO, "failed to copy file content: %w", err)
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
func (s *Scanner) findIntermediateImage(
	stageAlias string,
) (*storage.Image, bool, error) {

	images, err := s.store.Images()
	if err != nil {
		return nil, false, errorf(ErrStorage, "failed to list images: %w", err)
	}

	var errs []error
	for i := range images {
		if len(images[i].Names) != 0 {
			continue
		}

		cfg, err := s.sclient.GetImageConfig(images[i].ID)
		if err != nil {
			errs = append(errs, errorf(
				ErrStorage, "getting image config for intermediate image %s: %w",
				images[i].ID, err,
			))
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
			errs = append(errs, errorf(
				ErrMissingStageLabel,
				"intermediate image %s (buildah %s) is missing io.buildah.stage.name label, "+
					"make sure to pass --save-stages --stage-labels to the buildah build command",
				images[i].ID, cfg.Config.Labels["io.buildah.version"],
			))
			continue
		}

		if stageName == stageAlias {
			s.logger.Debug("found intermediate image", "imageID", images[i].ID, "stage", stageAlias)
			return &images[i], true, nil
		}
	}

	if len(errs) > 0 {
		return nil, false, errorf(
			ErrStorage,
			"no intermediate image found for stage %q; encountered %d problematic image(s) in storage:\n%w",
			stageAlias, len(errs), errors.Join(errs...),
		)
	}
	s.logger.Debug("no intermediate image found for stage",
		"stage", stageAlias,
		"hint", "expected if the stage has no filesystem-changing instructions; "+
			"if it does, ensure the build used --save-stages --stage-labels flags",
	)
	return nil, false, nil
}

func checkBuildahVersionFromImage(labels map[string]string) error {
	buildahVersionStr, ok := labels["io.buildah.version"]
	if !ok {
		return errorf(ErrUnsupportedBuildahVersion, "io.buildah.version label not found in image config")
	}

	buildahVersion, err := semver.NewVersion(buildahVersionStr)
	if err != nil {
		return errorf(ErrUnsupportedBuildahVersion, "could not parse buildah version %q: %w", buildahVersionStr, err)
	}

	// Accept prerelease versions (e.g. 1.43.0-dev) for integration tests build
	// buildah from a specific commit where --save-stages --stage-labels is present
	// but the version is not yet >= 1.44.0. See ISV-7179 for removal.
	if buildahVersion.Prerelease() != "" {
		return nil
	}

	minVersion, _ := semver.NewVersion(MinBuildahVersion)
	if buildahVersion.LessThan(minVersion) {
		return errorf(
			ErrUnsupportedBuildahVersion,
			"image was built with buildah %s, requires >= %s",
			buildahVersionStr, MinBuildahVersion,
		)
	}

	return nil
}

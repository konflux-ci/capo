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

var ErrImageNotFound = errors.New("[ERR_IMAGE_NOT_FOUND] image not found in buildah storage")
var ErrImageMount = errors.New("[ERR_IMAGE_MOUNT] failed to mount image")
var ErrIO = errors.New("[ERR_IO] I/O operation failed")
var ErrStorage = errors.New("[ERR_STORAGE] container storage error")
var ErrUnsupportedBuildahVersion = errors.New("[ERR_UNSUPPORTED_BUILDAH_VERSION] unsupported buildah version")
var ErrMissingStageLabel = errors.New("[ERR_MISSING_STAGE_LABEL] intermediate image is missing stage label")

// getContent extracts builder base content and intermediate content for the
// specified stage from buildah storage for later syft scanning.
// Uses buildah stage labels (io.buildah.stage.name) to identify the
// intermediate image for the given stage alias.
// If the intermediateContentPath is empty, only builder/external content will
// be saved.
func (s *Scanner) getContent(
	pullspec string,
	digestBase string,
	stageAlias string,
	sources []string,
	builderContentPath string,
	intermediateContentPath string,
) error {
	isSpecialBase := storageclient.IsSpecialBase(pullspec)
	var builderImage *storage.Image

	if !isSpecialBase {
		// Special bases are not pullable or resolvable with Lookup
		imgId, err := s.store.Lookup(storageclient.StripTransport(pullspec))
		if err != nil {
			imgId, err = s.store.Lookup(storageclient.StripTransport(digestBase))
			if err != nil {
				return fmt.Errorf("could not find image %q in buildah storage: %w", pullspec, ErrImageNotFound)
			}
		}
		builderImage, err = s.store.Image(imgId)
		if err != nil {
			return fmt.Errorf("could not find image %q in buildah storage: %w", pullspec, ErrImageNotFound)
		}
	}

	if intermediateContentPath != "" {
		// Special bases will have builderImage set as nil
		intermediate, err := s.getIntermediateContent(
			builderImage,
			stageAlias,
			sources,
			intermediateContentPath,
		)

		if err != nil {
			return err
		}
		s.logContent("intermediate", intermediate, pullspec)
	}

	if !isSpecialBase {
		// Only standard bases have builder content. All content in special bases is treated as intermediate.
		builderContent, err := s.getImageContent(builderImage, sources, builderContentPath)
		if err != nil {
			return err
		}
		s.logContent("builder", builderContent, pullspec)
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

// getDescendantContent extracts intermediate content for a chained stage (node)
// by diffing its intermediate image against the provided diff base image.
// Returns the node's intermediate image and the list of extracted paths.
// The returned image is passed as diff base to further descendants in the chain.
// If the node has no intermediate image (empty stage), returns diffBase unchanged
// so it propagates through to the next descendant.
func (s *Scanner) getDescendantContent(
	stageAlias string,
	diffBase *storage.Image,
	sources []string,
	contentPath string,
) (*storage.Image, []string, error) {
	intermediateImage, err := s.findIntermediateImage(stageAlias)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: failed to find intermediate image for %q: %w", ErrStorage, stageAlias, err)
	}
	if intermediateImage == nil {
		// no intermediate image found for node - pass diffBase through unchanged
		s.logger.Debug("no intermediate image found for chained stage, skipping", "stage", stageAlias)
		return diffBase, nil, nil
	}

	diffBaseLayer, err := s.store.Layer(diffBase.TopLayer)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: failed to get diff base layer: %w", ErrStorage, err)
	}

	interLayer, err := s.store.Layer(intermediateImage.TopLayer)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: failed to get intermediate layer: %w", ErrStorage, err)
	}

	included, err := s.saveDiff(contentPath, interLayer.ID, diffBaseLayer.ID, sources)
	if err != nil {
		return nil, nil, err
	}

	return intermediateImage, included, nil
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
		return included, fmt.Errorf("could not mount image: %w: %w", err, ErrImageMount)
	}

	defer func() {
		if _, unmountErr := s.store.UnmountImage(image.ID, false); unmountErr != nil {
			err = fmt.Errorf("failed to unmount image: %w: %w", unmountErr, ErrStorage)
		}
	}()

	for _, src := range sources {
		full := path.Join(mountPath, src)
		matches, err := filepath.Glob(full)
		if err != nil {
			return included, fmt.Errorf("failed to glob pattern %q: %w: %w", src, err, ErrIO)
		}

		if len(matches) == 0 {
			continue
		}

		for _, match := range matches {
			fInfo, err := os.Stat(match)
			if err != nil {
				return included, fmt.Errorf("failed to stat %q: %w: %w", match, err, ErrIO)
			}

			relPath, err := filepath.Rel(mountPath, match)
			if err != nil {
				return included, fmt.Errorf("failed to get relative path for %q: %w: %w", match, err, ErrIO)
			}
			dest := path.Join(contentPath, relPath)

			if fInfo.IsDir() {
				// CopyFS also copies and follows symlinks even if they're outside the specified source,
				// This is not a problem for us because Syft ignores symbolic links.
				if err := os.CopyFS(dest, os.DirFS(match)); err != nil {
					return included, fmt.Errorf("failed to copy directory %q to %q: %w: %w", match, dest, err, ErrIO)
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
		return fmt.Errorf("failed to open file %q: %w: %w", src, err, ErrIO)
	}
	defer func() {
		err = reader.Close()
	}()

	if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
		return fmt.Errorf("failed to create directory %q: %w: %w", filepath.Dir(dest), err, ErrIO)
	}
	writer, err := os.Create(dest)
	if err != nil {
		return fmt.Errorf("failed to create file %q: %w: %w", dest, err, ErrIO)
	}
	defer func() {
		err = writer.Close()
	}()

	if _, err = io.Copy(writer, reader); err != nil {
		return fmt.Errorf("failed to copy file content: %w: %w", err, ErrIO)
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
	intermediateImage, err := s.findIntermediateImage(stageAlias)
	if err != nil {
		return []string{}, fmt.Errorf("failed to find intermediate image: %w: %w", err, ErrStorage)
	}
	if intermediateImage == nil {
		// No intermediate image for this stage
		return []string{}, nil
	}

	if builderImage == nil {
		// Scratch or unresolvable (special) bases
		return s.getImageContent(intermediateImage, sources, path)
	}

	builderLayer, err := s.store.Layer(builderImage.TopLayer)
	if err != nil {
		return []string{}, fmt.Errorf("failed to get builder layer: %w: %w", err, ErrStorage)
	}

	interLayer, err := s.store.Layer(intermediateImage.TopLayer)
	if err != nil {
		return []string{}, fmt.Errorf("failed to get intermediate layer: %w: %w", err, ErrStorage)
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
		return []string{}, fmt.Errorf("failed to compute layer diff: %w: %w", err, ErrStorage)
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
			return []string{}, fmt.Errorf("failed to read tar header: %w: %w", err, ErrIO)
		}

		if !includes(sources, header.Name) {
			continue
		}

		included = append(included, header.Name)

		target := filepath.Join(dest, header.Name)

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0755); err != nil {
				return []string{}, fmt.Errorf("failed to create directory %q: %w: %w", target, err, ErrIO)
			}
		case tar.TypeReg:
			// sometimes the archive does not have headers for directories
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return []string{}, fmt.Errorf("failed to create directory %q: %w: %w", filepath.Dir(target), err, ErrIO)
			}
			f, err := os.Create(target)
			if err != nil {
				return []string{}, fmt.Errorf("failed to create file %q: %w: %w", target, err, ErrIO)
			}

			if _, err := io.Copy(f, reader); err != nil {
				_ = f.Close()
				return []string{}, fmt.Errorf("failed to copy file content: %w: %w", err, ErrIO)
			}
			_ = f.Close()
		}
	}

	return included, nil
}

// findIntermediateImage looks up an intermediate image by stage alias.
// Iterates all unnamed images in the store, validates buildah version and
// stage label presence on each.
// An error will be returned if image config retrieval fails.
// The logger will emit warnings for missing stage labels, older buildah
// versions and missing intermediate images. These warnings might not mean
// something went wrong in the process, the intermediate image retrieval is
// unfortunately a little non-robust right now.
func (s *Scanner) findIntermediateImage(
	stageAlias string,
) (*storage.Image, error) {

	images, err := s.store.Images()
	if err != nil {
		return nil, fmt.Errorf("failed to list images: %w: %w", err, ErrStorage)
	}

	for i := range images {
		if len(images[i].Names) != 0 {
			continue
		}

		cfg, err := s.sclient.GetImageConfig(images[i].ID)
		if err != nil {
			return nil, fmt.Errorf(
				"getting image config for intermediate image %s: %w: %w",
				images[i].ID, err, ErrStorage,
			)
		}

		if err := checkBuildahVersionFromImage(cfg.Config.Labels); err != nil {
			s.logger.Warn(
				"intermediate image built by old version of buildah; ensure buildah 1.44.0 and higher is used  " +
				"and consider using a clean image storage to avoid interference from previous builds",
				"imageID", images[i].ID,
			)
			continue
		}

		stageName := cfg.Config.Labels["io.buildah.stage.name"]
		switch stageName {
		case stageAlias: {
			s.logger.Debug("found intermediate image", "imageID", images[i].ID, "stage", stageAlias)
			return &images[i], nil
		}
		case "": {
			s.logger.Warn(
				"io.buildah.stage.name label is missing for image",
				"imageID", images[i].ID,
			)
		}
		}
	}

	s.logger.Warn("no intermediate image found for stage",
		"stage", stageAlias,
		"hint", "expected if the stage has no filesystem-changing instructions; "+
			"if it does, ensure the build used --save-stages --stage-labels flags",
	)
	return nil, nil
}

func checkBuildahVersionFromImage(labels map[string]string) error {
	buildahVersionStr, ok := labels["io.buildah.version"]
	if !ok {
		return fmt.Errorf("io.buildah.version label not found in image config: %w", ErrUnsupportedBuildahVersion)
	}

	buildahVersion, err := semver.NewVersion(buildahVersionStr)
	if err != nil {
		return fmt.Errorf("could not parse buildah version %q: %w: %w", buildahVersionStr, err, ErrUnsupportedBuildahVersion)
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
			"image was built with buildah %s, requires >= %s: %w",
			buildahVersionStr, MinBuildahVersion, ErrUnsupportedBuildahVersion,
		)
	}

	return nil
}

func dirSize(path string) (int64, error) {
	var size int64
	err := filepath.WalkDir(path, func(_ string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return fmt.Errorf("failed to stat entry: %w", err)
		}
		size += info.Size()
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("failed to walk directory %q: %w", path, err)
	}
	return size, nil
}

func formatSize(bytes int64) string {
	const (
		kb = 1024
		mb = 1024 * kb
		gb = 1024 * mb
	)
	switch {
	case bytes >= gb:
		return fmt.Sprintf("%.1fGB", float64(bytes)/float64(gb))
	case bytes >= mb:
		return fmt.Sprintf("%.1fMB", float64(bytes)/float64(mb))
	case bytes >= kb:
		return fmt.Sprintf("%.1fKB", float64(bytes)/float64(kb))
	default:
		return fmt.Sprintf("%dB", bytes)
	}
}

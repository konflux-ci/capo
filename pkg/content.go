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

	"go.podman.io/storage"
	"go.podman.io/storage/pkg/archive"
)

var ErrImageNotFound = errors.New("could not find image in buildah storage")
var ErrImageMount = errors.New("could not mount image")
var ErrIO = errors.New("IO operation failed")
var ErrStorage = errors.New("storage operation failed")

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

func includes(sources []string, path string) bool {
	if !filepath.IsAbs(path) {
		path = "/" + path
	}

	for _, src := range sources {
		if matched, _ := filepath.Match(src, path); matched || strings.HasPrefix(path, src) {
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

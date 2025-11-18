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

// Uses the container store to returns a struct containing absolute paths to
// partial content for the specified pullspec.
//
// Uses the includer to filter content that should be included.
//
// Stores content to path/intermediate/ and path/builder/ directorties
// for intermediate and builder content respectively.
//
// WARNING: currently there is a limitation on the intermediate content that can be retrieved.
// If the store after a 'buildah build' contains multiple intermediate layers in different buildah stages
// that use a builder image with the same pullspec, only one intermediate layer can be retrieved.
// This is because it is currently impossible to differentiate between the two layers, a contribution
// to buildah will be most likely required (such as storing the ids of the last layers/images in a stage).
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

	intermediate, err := getIntermediateContent(store, img, pkgSource.sources, intermediateContentPath)
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
			return included, err
		}

		if len(matches) == 0 {
			continue
		}

		for _, match := range matches {
			fInfo, err := os.Stat(match)
			if err != nil {
				return included, err
			}

			relPath, err := filepath.Rel(mountPath, match)
			if err != nil {
				return included, err
			}
			dest := path.Join(contentPath, relPath)

			if fInfo.IsDir() {
				// CopyFS also copies and follows symlinks even if they're outside the specified source,
				// This is not a problem for us because Syft ignores symbolic links.
				if err := os.CopyFS(dest, os.DirFS(match)); err != nil {
					return included, err
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
// Calculates a diff between the last intermediate layer in a stage and its
// respective builder base image, then uses the includer to filter content of interest.
//
// Tries to find last intermediate layer by looking for all intermediate images,
// and filtering the ones whose layer parent ids eventually reach the builder
// image. Out of these, the last intermediate layer is the one with the longest
// chain to the builder image.
//
// WARNING: This approach is not totally correct, specifically it cannot handle
// builds where multiple builder stages use the same builder base pullspec.
// In this case only one such intermediate layer can be found.
// A contribution to buildah might be required, see [content.GetContent] documentation.
func getIntermediateContent(
	store storage.Store,
	builderImage *storage.Image,
	sources []string,
	path string,
) ([]string, error) {
	builderLayer, err := store.Layer(builderImage.TopLayer)
	if err != nil {
		return []string{}, fmt.Errorf("%w: failed to get builder layer: %w", ErrStorage, err)
	}

	interLayer, err := getLastIntermediateLayer(store, builderLayer)
	if err != nil {
		return []string{}, err
	}
	if interLayer == nil {
		return []string{}, nil
	}

	included, err := saveDiff(store, path, interLayer.ID, builderLayer.ID, sources)
	if err != nil {
		return []string{}, err
	}

	return included, nil
}

func getIntermediateLayers(store storage.Store, builderLayer *storage.Layer) ([]*storage.Layer, error) {
	images, err := store.Images()
	if err != nil {
		return nil, fmt.Errorf("%w: failed to list images: %w", ErrStorage, err)
	}

	var candidates []*storage.Layer

	for _, img := range images {
		// The image for the last intermediate layer never has a name
		if len(img.Names) != 0 {
			continue
		}

		// This is an image for the builder layer itself so it
		// cannot be an intermediate layer.
		if img.TopLayer == builderLayer.ID {
			continue
		}

		imgTopLayer, err := store.Layer(img.TopLayer)
		if err != nil {
			return nil, fmt.Errorf("%w: failed to get image top layer: %w", ErrStorage, err)
		}

		layerId := img.TopLayer
		for layerId != "" {
			if layerId == builderLayer.ID {
				candidates = append(candidates, imgTopLayer)
				break
			}

			layer, err := store.Layer(layerId)
			if err != nil {
				return nil, fmt.Errorf("%w: failed to get layer: %w", ErrStorage, err)
			}

			layerId = layer.Parent
		}
	}

	return candidates, nil
}

func getLastIntermediateLayer(store storage.Store, builderLayer *storage.Layer) (*storage.Layer, error) {
	candidates, err := getIntermediateLayers(store, builderLayer)
	if err != nil {
		return nil, err
	}

	if len(candidates) == 0 {
		return nil, nil
	}

	// Find the candidate with the longest layer chain (furthest from builder)
	var longestChain *storage.Layer
	maxDepth := 0

	for _, candidate := range candidates {
		depth := 0
		layerId := candidate.ID

		for layerId != "" && layerId != builderLayer.ID {
			layer, err := store.Layer(layerId)
			if err != nil {
				return nil, fmt.Errorf("%w: failed to get layer in chain: %w", ErrStorage, err)
			}

			depth++
			layerId = layer.Parent
		}

		if depth > maxDepth {
			maxDepth = depth
			longestChain = candidate
		}
	}

	return longestChain, nil
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

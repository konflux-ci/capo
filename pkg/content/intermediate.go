package content

import (
	"archive/tar"
	"io"
	"os"
	"path/filepath"

	"go.podman.io/storage"
	"go.podman.io/storage/pkg/archive"
)

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
func GetIntermediateContent(
	store storage.Store,
	builderImage *storage.Image,
	includer Includer,
	path string,
) ([]string, error) {
	builderLayer, err := store.Layer(builderImage.TopLayer)
	if err != nil {
		return []string{}, err
	}

	interLayer, err := getLastIntermediateLayer(store, builderLayer)
	if err != nil {
		return []string{}, err
	}
	if interLayer == nil {
		return []string{}, nil
	}

	included, err := saveDiff(store, path, interLayer.ID, builderLayer.ID, includer)
	if err != nil {
		return []string{}, err
	}

	return included, nil
}

func getIntermediateLayers(store storage.Store, builderLayer *storage.Layer) ([]*storage.Layer, error) {
	images, err := store.Images()
	if err != nil {
		return nil, err
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
			return nil, err
		}

		layerId := img.TopLayer
		for {
			if layerId == "" {
				break
			}
			if layerId == builderLayer.ID {
				candidates = append(candidates, imgTopLayer)
				break
			}

			layer, err := store.Layer(layerId)
			if err != nil {
				return nil, err
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

		for {
			if layerId == "" {
				break
			}
			if layerId == builderLayer.ID {
				break
			}

			layer, err := store.Layer(layerId)
			if err != nil {
				return nil, err
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

func saveDiff(store storage.Store, dest string, layerId string, parentId string, includer Includer) ([]string, error) {
	compression := archive.Uncompressed
	opts := storage.DiffOptions{
		Compression: &compression,
	}

	diff, err := store.Diff(parentId, layerId, &opts)
	if err != nil {
		return []string{}, err
	}
	defer diff.Close()

	included := make([]string, 0, 16)
	reader := tar.NewReader(diff)
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return []string{}, err
		}

		if !includer.Includes(header.Name) {
			continue
		}
		included = append(included, header.Name)

		target := filepath.Join(dest, header.Name)

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0755); err != nil {
				return []string{}, err
			}
		case tar.TypeReg:
			// sometimes the archive does not have headers for directories
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return []string{}, err
			}
			f, err := os.Create(target)
			if err != nil {
				return []string{}, err
			}

			if _, err := io.Copy(f, reader); err != nil {
				f.Close()
				return []string{}, err
			}
			f.Close()
		}
	}

	return included, nil
}

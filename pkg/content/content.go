/*
The content package uses a containers/storage Store to inspect built layers
and store partial content from the build, for later syft scanning.
*/
package content

import (
	"capo/pkg/includer"
	"fmt"
	"io"
	"log"
	"os"
	"path"
	"path/filepath"
	"strings"

	"go.podman.io/storage"
)

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
func GetBuilderContent(
	store storage.Store,
	pullspec string,
	includer includer.Includer,
	builderContentPath string,
	intermediateContentPath string,
) error {
	imgId, err := store.Lookup(pullspec)
	if err != nil {
		return fmt.Errorf("Could not find image: %s in buildah storage.", pullspec)
	}
	img, _ := store.Image(imgId)

	intermediate, err := GetIntermediateContent(store, img, includer, intermediateContentPath)
	if err != nil {
		return err
	}

	if len(intermediate) == 0 {
		log.Printf("Found no intermediate content for %s.", pullspec)
	} else {
		log.Printf("Included intermediate content %+v for %s.", intermediate, pullspec)
	}

	builder, err := getImageContent(store, img, includer, builderContentPath)
	if err != nil {
		return err
	}
	log.Printf("Included builder content %+v for %s.", builder, pullspec)

	return nil
}

func GetExternalContent(
	store storage.Store,
	externalPullspec string,
	includer includer.Includer,
	contentPath string,
) error {
	externalImgId, err := store.Lookup(externalPullspec)
	if err != nil {
		return fmt.Errorf("Could not find image: %s in buildah storage.", externalPullspec)
	}
	externalImg, _ := store.Image(externalImgId)

	external, err := getImageContent(store, externalImg, includer, contentPath)
	if err != nil {
		return err
	}

	log.Printf("Included external content %+v for %s.", external, externalPullspec)

	return nil
}

func includes(sources []string, path string) bool {
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}

	for _, src := range sources {
		if strings.HasPrefix(path, src) {
			return true
		}
	}

	return false
}

func getImageContent(
	store storage.Store,
	image *storage.Image,
	includer includer.Includer,
	contentPath string,
) (included []string, err error) {
	mountPath, err := store.MountImage(image.ID, []string{}, "")
	if err != nil {
		return included, err
	}
	defer store.UnmountImage(image.ID, false)

	sources := includer.Sources()
	for _, src := range sources {
		full := path.Join(mountPath, src)

		fInfo, err := os.Stat(full)
		if os.IsNotExist(err) {
			// If the path doesn't exist, it's likely intermediate content.
			// We ignore it and continue looking for content.
			continue
		} else if err != nil {
			return included, err
		}

		dest := path.Join(contentPath, src)

		if fInfo.IsDir() {
			// CopyFS also copies and follows symlinks even if they're outside the specified source,
			// This is not a problem for us because Syft ignores symbolic links.
			if err := os.CopyFS(contentPath, os.DirFS(full)); err != nil {
				return included, err
			}
		} else if fInfo.Mode().IsRegular() {
			if err := copyFile(full, dest); err != nil {
				return included, err
			}
		}
		included = append(included, src)
	}

	return included, err
}

func copyFile(src string, dest string) error {
	reader, err := os.Open(src)
	if err != nil {
		return err
	}
	defer reader.Close()

	if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
		return err
	}
	writer, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer writer.Close()

	_, err = io.Copy(writer, reader)
	return err
}

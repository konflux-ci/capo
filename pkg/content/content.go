/*
The content package uses a containers/storage Store to inspect built layers
and store partial content from the build, for later syft scanning.
*/
package content

import (
	"fmt"
	"log"
	"os"
	"path/filepath"

	"go.podman.io/storage"
)

// Content contains paths to saved partial image content.
type Content struct {
	IntermediatePath string // content of the last intermediate layer in a stage
	BuilderPath      string // content of the builder image
	ExternalPath     string // content from all copies from this pullspec (UNIMPLEMENTED)
}

type Includer interface {
	// Returns true if content in the specified path should be included.
	// Used to filter intermediate content.
	Includes(path string) bool

	// Returns a slice of strings of paths whose content (including subpaths) should be included.
	// Used to filter builder content more effectively.
	GetSources() []string
}

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
func GetContent(store storage.Store, pullspec string, includer Includer, path string) (Content, error) {
	var content = Content{}

	imgId, err := store.Lookup(pullspec)
	if err != nil {
		return content, fmt.Errorf("Could not find image: %s in buildah storage.", pullspec)
	}
	img, _ := store.Image(imgId)

	content.IntermediatePath, err = filepath.Abs(filepath.Join(path, "intermediate/"))
	if err != nil {
		return content, err
	}

	if os.Mkdir(content.IntermediatePath, 0755) != nil {
		return content, err
	}
	intermediate, err := GetIntermediateContent(store, img, includer, content.IntermediatePath)
	if err != nil {
		return content, err
	}

	if len(intermediate) == 0 {
		content.IntermediatePath = ""
		log.Printf("Found no intermediate content for %s.", pullspec)
	} else {
		log.Printf("Included intermediate content %+v for %s.", intermediate, pullspec)
	}

	content.BuilderPath, err = filepath.Abs(filepath.Join(path, "builder/"))
	if err != nil {
		return content, err
	}
	if os.Mkdir(content.BuilderPath, 0755) != nil {
		return content, err
	}
	builder, err := GetBuilderContent(store, img, includer, content.BuilderPath)
	log.Printf("Included builder content %+v for %s.", builder, pullspec)

	return content, nil
}

package content

import (
	"go.podman.io/storage"
	"io"
	"os"
	"path"
	"path/filepath"
)

// Stores builder content for the specified image to the contentPath directory.
// Mounts the specified image and copies the content that should be included based on the includer.
// Returns paths that were found during content fetching.
func GetBuilderContent(
	store storage.Store,
	builderImage *storage.Image,
	includer Includer,
	contentPath string,
) ([]string, error) {
	mountPath, err := store.MountImage(builderImage.ID, []string{}, "")
	if err != nil {
		return []string{}, err
	}
	defer store.UnmountImage(builderImage.ID, false)

	sources := includer.GetSources()
	for _, src := range sources {
		full := path.Join(mountPath, src)
		fInfo, err := os.Stat(full)
		if err != nil {
			return []string{}, err
		}
		dest := path.Join(contentPath, src)

		if fInfo.IsDir() {
			// CopyFS also copies and follows symlinks even if they're outside the specified source,
			// This is not a problem for us because Syft ignores symbolic links.
			if err := os.CopyFS(contentPath, os.DirFS(full)); err != nil {
				return []string{}, err
			}
		} else if fInfo.Mode().IsRegular() {
			if err := copyFile(full, dest); err != nil {
				return []string{}, err
			}
		}
	}

	return sources, err
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

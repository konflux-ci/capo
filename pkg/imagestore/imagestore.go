// Package imagestore provides access to container image storage for resolving
// image pullspecs to their digests.
package imagestore

import (
	"errors"
	"fmt"

	"go.podman.io/storage"
	"go.podman.io/storage/pkg/reexec"
)

// ImageStore abstracts container image storage operations, primarily the
// resolution of image pullspecs to their content digests.
type ImageStore interface {
	ResolveDigest(string) (string, error)
}

// BuildahStore is an ImageStore backed by a local buildah/containers storage.
type BuildahStore struct {
	store storage.Store
}

// ErrPullspecResolve is returned when a pullspec cannot be found or resolved
// in the storage.
var ErrPullspecResolve = errors.New("failed to resolve pullspec")

// ErrBuildahStorageSetup is returned when the buildah storage cannot be
// initialized.
var ErrBuildahStorageSetup = errors.New("error while setting up buildah storage")

// NewBuildahStore creates a BuildahStore using the default
// containers/storage location.
func NewBuildahStore() (ImageStore, error) {
	// The containers/storage library requires this to run for some operations
	if reexec.Init() {
		return &BuildahStore{}, fmt.Errorf("%w: failed to init reexec", ErrBuildahStorageSetup)
	}

	opts, err := storage.DefaultStoreOptions()
	if err != nil {
		return &BuildahStore{},
			fmt.Errorf("%w: failed to create default storage options: %w", ErrBuildahStorageSetup, err)
	}

	store, err := storage.GetStore(opts)
	if err != nil {
		return &BuildahStore{}, fmt.Errorf("%w: failed to create storage: %w", ErrBuildahStorageSetup, err)
	}

	return &BuildahStore{
		store: store,
	}, nil
}

// ResolveDigest looks up the given pullspec in the local storage and returns
// its content digest in the form "sha256:<hex>".
func (repo *BuildahStore) ResolveDigest(pullspec string) (string, error) {
	id, err := repo.store.Lookup(pullspec)
	if err != nil {
		return "", fmt.Errorf("%w %q: %w", ErrPullspecResolve, pullspec, err)
	}

	img, err := repo.store.Image(id)
	if err != nil {
		return "", fmt.Errorf("%w %q: %w", ErrPullspecResolve, pullspec, err)
	}

	return fmt.Sprintf("sha256:%s", img.Digest.Encoded()), nil
}

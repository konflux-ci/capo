package repository

// FIXME: add documentation in this package

import (
	"errors"
	"fmt"

	"go.podman.io/storage"
	"go.podman.io/storage/pkg/reexec"
)

type Repository interface {
	ResolveDigest(string) (string, error)
}

type BuildahRepository struct {
	store storage.Store
}

var ErrPullspecResolve = errors.New("failed to resolve pullspec")
var ErrBuildahStorageSetup = errors.New("error while setting up buildah storage")

func NewBuildahRepository() (Repository, error) {
	// The containers/storage library requires this to run for some operations
	if reexec.Init() {
		return &BuildahRepository{}, fmt.Errorf("%w: failed to init reexec", ErrBuildahStorageSetup)
	}

	opts, err := storage.DefaultStoreOptions()
	if err != nil {
		return &BuildahRepository{},
			fmt.Errorf("%w: failed to create default storage options: %w", ErrBuildahStorageSetup, err)
	}

	store, err := storage.GetStore(opts)
	if err != nil {
		return &BuildahRepository{}, fmt.Errorf("%w: failed to create storage: %w", ErrBuildahStorageSetup, err)
	}

	return &BuildahRepository{
		store: store,
	}, nil
}

func (repo *BuildahRepository) ResolveDigest(pullspec string) (string, error) {
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

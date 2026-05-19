// Package storageclient provides utility functions with access to container image
// storage.
package storageclient

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/opencontainers/go-digest"
	"go.podman.io/storage"
	"go.podman.io/storage/pkg/reexec"
)

// Wrapper for the application/vnd.oci.image.config.v1+json media type.
// https://github.com/opencontainers/image-spec/blob/main/config.md
type OCIImageConfig struct {
	Config struct {
		Labels  map[string]string `json:"Labels"`
		Workdir string            `json:"WorkingDir"`
	} `json:"config"`
}

// Client provides methods for container image storage operations.
type Client interface {
	ResolveDigest(string) (digest.Digest, error)
	GetImageConfig(string) (OCIImageConfig, error)
}

// BuildahClient is a Storage Client backed by a local buildah containers storage.
type BuildahClient struct {
	store storage.Store
}

// ErrPullspecResolve is returned when a pullspec cannot be found or resolved
// in the storage.
var ErrPullspecResolve = errors.New("failed to resolve pullspec")

// ErrBuildahStorageSetup is returned when the buildah storage cannot be
// initialized.
var ErrBuildahStorageSetup = errors.New("error while setting up buildah storage")

// ErrOCIImageConfig is returned when an unexpected error occurs while trying to
// get the config object of an image.
var ErrOCIImageConfig = errors.New("could not get config for image")

// DefaultBuildahClient creates a Client using the default
// containers/storage location.
func DefaultBuildahClient() (Client, error) {
	// The containers/storage library requires this to run for some operations
	if reexec.Init() {
		return nil, fmt.Errorf("%w: failed to init reexec", ErrBuildahStorageSetup)
	}

	opts, err := storage.DefaultStoreOptions()
	if err != nil {
		return nil,
			fmt.Errorf("%w: failed to create default storage options: %w", ErrBuildahStorageSetup, err)
	}

	store, err := storage.GetStore(opts)
	if err != nil {
		return nil, fmt.Errorf("%w: failed to create storage: %w", ErrBuildahStorageSetup, err)
	}

	return NewBuildahClient(store), nil
}

// NewBuildahClient the passed containers/storage.Store object to create a Client.
func NewBuildahClient(store storage.Store) Client {
	return &BuildahClient{
		store: store,
	}
}

// ResolveDigest looks up the given pullspec in the local storage and returns
// its content digest in the form "sha256:<hex>".
func (c *BuildahClient) ResolveDigest(pullspec string) (digest.Digest, error) {
	id, err := c.store.Lookup(pullspec)
	if err != nil {
		return "", fmt.Errorf("%w %q: %w", ErrPullspecResolve, pullspec, err)
	}

	img, err := c.store.Image(id)
	if err != nil {
		return "", fmt.Errorf("%w %q: %w", ErrPullspecResolve, pullspec, err)
	}

	return img.Digest, nil
}

// Get an OCIImageConfig struct for the passed pullspec via buildah's container
// storage.
func (c *BuildahClient) GetImageConfig(pullspec string) (OCIImageConfig, error) {
	imgId, err := c.store.Lookup(pullspec)
	if err != nil {
		return OCIImageConfig{}, fmt.Errorf("%w %s: %w", ErrOCIImageConfig, pullspec, err)
	}

	img, err := c.store.Image(imgId)
	if err != nil {
		return OCIImageConfig{}, fmt.Errorf("%w %s: %w", ErrOCIImageConfig, pullspec, err)
	}

	var configBlobName string
	for _, name := range img.BigDataNames {
		if strings.HasPrefix(name, "sha256:") {
			configBlobName = name
			break
		}
	}

	if configBlobName == "" {
		return OCIImageConfig{}, fmt.Errorf("%w %s: %w", ErrOCIImageConfig, pullspec, err)
	}

	configData, err := c.store.ImageBigData(img.ID, configBlobName)
	if err != nil {
		return OCIImageConfig{}, fmt.Errorf("%w %s: %w", ErrOCIImageConfig, pullspec, err)
	}

	var config OCIImageConfig
	if err := json.Unmarshal(configData, &config); err != nil {
		return OCIImageConfig{}, fmt.Errorf("%w %s: %w", ErrOCIImageConfig, pullspec, err)
	}

	return config, nil
}

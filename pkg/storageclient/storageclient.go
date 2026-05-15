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

// Transports that prefix a docker-reference resolvable via store.Lookup.
// These are stripped before lookup and re-added to the resolved result.
// See https://github.com/containers/image/blob/main/docs/containers-transports.5.md
var strippableTransports = []string{
	"docker://",
	"docker-daemon:",
}

// Filesystem-based transports that reference local paths rather than images
// in containers/storage. These cannot be resolved via store.Lookup.
// See https://github.com/containers/image/blob/main/docs/containers-transports.5.md
var filesystemTransports = []string{
	"oci-archive:",
	"docker-archive:",
	"oci:",
	"dir:",
}

// StripTransport removes known resolvable transport prefixes (e.g.
// "docker://", "docker-daemon:") from a pullspec so it can be used with
// store.Lookup and reference.ParseNamed, which only understand plain image
// references.
// See https://github.com/containers/image/blob/main/docs/containers-transports.5.md
func StripTransport(pullspec string) string {
	for _, prefix := range strippableTransports {
		if after, ok := strings.CutPrefix(pullspec, prefix); ok {
			return after
		}
	}
	return pullspec
}

// IsFilesystemTransport checks if the pullspec uses a filesystem-based
// transport (oci-archive:, docker-archive:, oci:, dir:) that references a
// local path rather than an image in containers/storage.
// See https://github.com/containers/image/blob/main/docs/containers-transports.5.md
func IsFilesystemTransport(base string) bool {
	for _, prefix := range filesystemTransports {
		if strings.HasPrefix(base, prefix) {
			return true
		}
	}
	return false
}

// IsSpecialBase checks if the base pullspec is a special base that cannot be
// resolved via store.Lookup. This includes scratch and all filesystem-based
// transports (oci-archive:, docker-archive:, oci:, dir:).
// See https://github.com/containers/image/blob/main/docs/containers-transports.5.md
func IsSpecialBase(base string) bool {
	return base == "scratch" || IsFilesystemTransport(base)
}

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
// its content digest in the form "sha256:<hex>". Transport prefixes (e.g.
// "docker://") are stripped before the lookup. The reference can be the
// image's name or ID.
func (c *BuildahClient) ResolveDigest(ref string) (digest.Digest, error) {
	id, err := c.store.Lookup(StripTransport(ref))
	if err != nil {
		return "", fmt.Errorf("%w %q: %w", ErrPullspecResolve, ref, err)
	}

	img, err := c.store.Image(id)
	if err != nil {
		return "", fmt.Errorf("%w %q: %w", ErrPullspecResolve, ref, err)
	}

	return img.Digest, nil
}

// Get an OCIImageConfig struct for the passed pullspec via buildah's container
// storage. The reference can be the image's name or ID.
func (c *BuildahClient) GetImageConfig(ref string) (OCIImageConfig, error) {
	imgId, err := c.store.Lookup(StripTransport(ref))
	if err != nil {
		return OCIImageConfig{}, fmt.Errorf("%w %s: %w", ErrOCIImageConfig, ref, err)
	}

	img, err := c.store.Image(imgId)
	if err != nil {
		return OCIImageConfig{}, fmt.Errorf("%w %s: %w", ErrOCIImageConfig, ref, err)
	}

	var configBlobName string
	for _, name := range img.BigDataNames {
		if strings.HasPrefix(name, "sha256:") {
			configBlobName = name
			break
		}
	}

	if configBlobName == "" {
		return OCIImageConfig{}, fmt.Errorf("%w %s: %w", ErrOCIImageConfig, ref, err)
	}

	configData, err := c.store.ImageBigData(img.ID, configBlobName)
	if err != nil {
		return OCIImageConfig{}, fmt.Errorf("%w %s: %w", ErrOCIImageConfig, ref, err)
	}

	var config OCIImageConfig
	if err := json.Unmarshal(configData, &config); err != nil {
		return OCIImageConfig{}, fmt.Errorf("%w %s: %w", ErrOCIImageConfig, ref, err)
	}

	return config, nil
}

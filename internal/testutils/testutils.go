//go:build unit

package testutils

import (
	"fmt"

	"github.com/opencontainers/go-digest"

	"github.com/konflux-ci/capo/pkg/storageclient"
)

// TStorageClient provides a mock implementation of storageclient.Client for testing.
type TStorageClient struct {
	// Mapping of image pullspec to the digest of the image
	digests map[string]digest.Digest
	// Mapping of image pullspec to the OCIImageConfig of the image
	configs map[string]storageclient.OCIImageConfig
}

// NewTStorageClient creates a new MockClient with the provided digests and configs.
func NewTStorageClient(digests map[string]digest.Digest, configs map[string]storageclient.OCIImageConfig) *TStorageClient {
	return &TStorageClient{
		digests: digests,
		configs: configs,
	}
}

// ResolveDigest returns the digest for the given pullspec if it exists in the mock data.
func (c *TStorageClient) ResolveDigest(pullspec string) (digest.Digest, error) {
	dig, ok := c.digests[pullspec]
	if !ok {
		return "", fmt.Errorf("digest for %q not found", pullspec)
	}

	return dig, nil
}

// GetImageConfig returns the config for the given pullspec if it exists in the mock data.
func (c *TStorageClient) GetImageConfig(pullspec string) (storageclient.OCIImageConfig, error) {
	cfg, ok := c.configs[pullspec]
	if !ok {
		return storageclient.OCIImageConfig{}, fmt.Errorf("config for %q not found", pullspec)
	}

	return cfg, nil
}

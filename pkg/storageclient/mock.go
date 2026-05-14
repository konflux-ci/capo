//go:build unit

package storageclient

import (
	"fmt"

	"github.com/opencontainers/go-digest"
)

// MockClient provides a mock implementation of Client for testing.
type MockClient struct {
	digests map[string]digest.Digest
	configs map[string]OCIImageConfig
}

// NewMockClient creates a new MockClient with the provided digests and configs.
func NewMockClient(digests map[string]digest.Digest, configs map[string]OCIImageConfig) *MockClient {
	return &MockClient{
		digests: digests,
		configs: configs,
	}
}

// ResolveDigest returns the digest for the given pullspec if it exists in the mock data.
func (c *MockClient) ResolveDigest(pullspec string) (digest.Digest, error) {
	dig, ok := c.digests[pullspec]
	if !ok {
		return "", fmt.Errorf("digest for %q not found", pullspec)
	}

	return dig, nil
}

// GetImageConfig returns the config for the given pullspec if it exists in the mock data.
func (c *MockClient) GetImageConfig(pullspec string) (OCIImageConfig, error) {
	cfg, ok := c.configs[pullspec]
	if !ok {
		return OCIImageConfig{}, fmt.Errorf("config for %q not found", pullspec)
	}

	return cfg, nil
}
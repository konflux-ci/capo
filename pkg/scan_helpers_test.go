//go:build unit

package capo

import (
	"errors"
	"testing"
)

func TestCheckBuildahVersionFromImage(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		labels    map[string]string
		expectErr bool
	}{
		"supported version": {
			labels:    map[string]string{"io.buildah.version": "1.44.0"},
			expectErr: false,
		},
		"newer patch version": {
			labels:    map[string]string{"io.buildah.version": "1.44.1"},
			expectErr: false,
		},
		"newer minor version": {
			labels:    map[string]string{"io.buildah.version": "1.45.0"},
			expectErr: false,
		},
		"newer major version": {
			labels:    map[string]string{"io.buildah.version": "2.0.0"},
			expectErr: false,
		},
		"dev version of supported release": {
			labels:    map[string]string{"io.buildah.version": "1.44.0-dev"},
			expectErr: false,
		},
		"old minor version": {
			labels:    map[string]string{"io.buildah.version": "1.43.0"},
			expectErr: true,
		},
		"old patch version": {
			labels:    map[string]string{"io.buildah.version": "1.43.1"},
			expectErr: true,
		},
		"very old version": {
			labels:    map[string]string{"io.buildah.version": "1.0.0"},
			expectErr: true,
		},
		"dev version of old release": {
			labels:    map[string]string{"io.buildah.version": "1.43.0-dev"},
			expectErr: true,
		},
		"missing label": {
			labels:    map[string]string{},
			expectErr: true,
		},
		"unparseable version": {
			labels:    map[string]string{"io.buildah.version": "garbage"},
			expectErr: true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			err := checkBuildahVersionFromImage(tc.labels)
			if tc.expectErr && err == nil {
				t.Errorf("expected error but got nil")
			}
			if !tc.expectErr && err != nil {
				t.Errorf("expected no error but got: %v", err)
			}
			if tc.expectErr && err != nil && !errors.Is(err, ErrUnsupportedBuildahVersion) {
				t.Errorf("expected ErrUnsupportedBuildahVersion but got: %v", err)
			}
		})
	}
}

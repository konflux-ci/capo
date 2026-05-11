//go:build unit

package capo

import (
	"errors"
	"testing"
)

func TestIncludes(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		sources []string
		path    string
		want    bool
	}{
		// Exact matches
		"exact match absolute path": {
			sources: []string{"/opt"},
			path:    "/opt",
			want:    true,
		},
		"exact match file": {
			sources: []string{"/opt/go.mod"},
			path:    "/opt/go.mod",
			want:    true,
		},
		"no match different path": {
			sources: []string{"/opt"},
			path:    "/usr",
			want:    false,
		},

		// Subdirectory matching (path is under source)
		"path under source directory": {
			sources: []string{"/opt"},
			path:    "/opt/go.mod",
			want:    true,
		},
		"path deeply nested under source": {
			sources: []string{"/opt"},
			path:    "/opt/app/sub/go.mod",
			want:    true,
		},
		"path under nested source": {
			sources: []string{"/opt/app"},
			path:    "/opt/app/go.mod",
			want:    true,
		},

		// Prefix collision (bug #1: /opt must NOT match /optional)
		"prefix collision opt vs optional": {
			sources: []string{"/opt"},
			path:    "/optional",
			want:    false,
		},
		"prefix collision opt vs optional nested": {
			sources: []string{"/opt"},
			path:    "/optional/go.mod",
			want:    false,
		},
		"prefix collision app vs application": {
			sources: []string{"/app"},
			path:    "/application/go.mod",
			want:    false,
		},
		"prefix collision content vs contentful": {
			sources: []string{"/content"},
			path:    "/contentful/data",
			want:    false,
		},

		// Relative path handling (tar entries may lack leading /)
		"relative path matches absolute source": {
			sources: []string{"/opt"},
			path:    "opt/go.mod",
			want:    true,
		},
		"relative path exact match": {
			sources: []string{"/opt"},
			path:    "opt",
			want:    true,
		},
		"relative path prefix collision": {
			sources: []string{"/opt"},
			path:    "optional/go.mod",
			want:    false,
		},

		// Wildcard exact match (same depth, no directory crossing)
		"wildcard match single level": {
			sources: []string{"/app*"},
			path:    "/app1",
			want:    true,
		},
		"wildcard no match": {
			sources: []string{"/app*"},
			path:    "/other",
			want:    false,
		},

		// Wildcard across directory separators (bug #2)
		"wildcard matches child path across separator": {
			sources: []string{"/app*"},
			path:    "/app1/go.mod",
			want:    true,
		},
		"wildcard matches deeply nested child": {
			sources: []string{"/app*"},
			path:    "/app1/sub/go.mod",
			want:    true,
		},
		"wildcard does not match unrelated nested path": {
			sources: []string{"/app*"},
			path:    "/other/go.mod",
			want:    false,
		},
		"wildcard relative path across separator": {
			sources: []string{"/app*"},
			path:    "app1/go.mod",
			want:    true,
		},
		"wildcard question mark matches child": {
			sources: []string{"/app?"},
			path:    "/app1/go.mod",
			want:    true,
		},
		"wildcard question mark no match extra chars": {
			sources: []string{"/app?"},
			path:    "/app12/go.mod",
			want:    false,
		},

		// Trailing slash source
		"trailing slash source matches child": {
			sources: []string{"/opt/"},
			path:    "/opt/go.mod",
			want:    true,
		},
		"trailing slash source prefix collision": {
			sources: []string{"/opt/"},
			path:    "/optional/go.mod",
			want:    false,
		},

		// Multiple sources
		"multiple sources first matches": {
			sources: []string{"/opt", "/usr"},
			path:    "/opt/go.mod",
			want:    true,
		},
		"multiple sources second matches": {
			sources: []string{"/opt", "/usr"},
			path:    "/usr/lib",
			want:    true,
		},
		"multiple sources none match": {
			sources: []string{"/opt", "/usr"},
			path:    "/var/log",
			want:    false,
		},
		"multiple sources with wildcard": {
			sources: []string{"/base", "/app*"},
			path:    "/app1/go.mod",
			want:    true,
		},

		// Empty sources
		"empty sources": {
			sources: []string{},
			path:    "/opt",
			want:    false,
		},

		// Path sanitization (tar entry paths may have "..", ".", double slashes)
		"dotdot in path resolved": {
			sources: []string{"/foo/bar"},
			path:    "/foo/bar/../bar/file.txt",
			want:    true,
		},
		"double slashes in path normalized": {
			sources: []string{"/foo/bar"},
			path:    "/foo//bar/file.txt",
			want:    true,
		},
		"dot in path normalized": {
			sources: []string{"/foo/bar"},
			path:    "/foo/./bar/file.txt",
			want:    true,
		},
		// Root source
		"root source matches any path": {
			sources: []string{"/"},
			path:    "/etc/passwd",
			want:    true,
		},
		"root source wildcard matches any path": {
			sources: []string{"/*"},
			path:    "/etc/passwd",
			want:    true,
		},
		"root source matches deeply nested path": {
			sources: []string{"/"},
			path:    "/opt/app/sub/go.mod",
			want:    true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got := includes(tc.sources, tc.path)
			if got != tc.want {
				t.Errorf("includes(%v, %q) = %v, want %v", tc.sources, tc.path, got, tc.want)
			}
		})
	}
}

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
			expectErr: false,
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

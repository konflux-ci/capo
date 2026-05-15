//go:build unit

package storageclient

import (
	"testing"
)

func TestIsSpecialBase(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		base string
		want bool
	}{
		"scratch": {
			base: "scratch",
			want: true,
		},
		"oci-archive with reference": {
			base: "oci-archive:base.ociarchive:latest",
			want: true,
		},
		"oci-archive without reference": {
			base: "oci-archive:path/to/file",
			want: true,
		},
		"docker-archive with reference": {
			base: "docker-archive:/tmp/image.tar:latest",
			want: true,
		},
		"oci layout with reference": {
			base: "oci:/tmp/oci-layout:latest",
			want: true,
		},
		"dir transport": {
			base: "dir:/tmp/some-dir",
			want: true,
		},
		"normal pullspec": {
			base: "docker.io/library/alpine:latest",
			want: false,
		},
		"case sensitive - Scratch": {
			base: "Scratch",
			want: false,
		},
		"prefix collision - scratchy": {
			base: "scratchy",
			want: false,
		},
		"oci-archive not at prefix": {
			base: "my-oci-archive:foo",
			want: false,
		},
		"normal pullspec with colon": {
			base: "quay.io/rhel:9",
			want: false,
		},
		"docker:// is not special": {
			base: "docker://docker.io/library/alpine:latest",
			want: false,
		},
		"docker-daemon: is not special": {
			base: "docker-daemon:alpine:latest",
			want: false,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got := IsSpecialBase(tc.base)
			if got != tc.want {
				t.Errorf("IsSpecialBase(%q) = %v, want %v", tc.base, got, tc.want)
			}
		})
	}
}

func TestStripTransport(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		input string
		want  string
	}{
		"docker:// prefix stripped": {
			input: "docker://docker.io/library/alpine:latest",
			want:  "docker.io/library/alpine:latest",
		},
		"docker-daemon: with registry": {
			input: "docker-daemon:docker.io/library/nginx:latest",
			want:  "docker.io/library/nginx:latest",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got := StripTransport(tc.input)
			if got != tc.want {
				t.Errorf("StripTransport(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

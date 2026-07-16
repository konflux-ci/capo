//go:build unit

package buildvars

import (
	"errors"
	"os"
	"testing"

	"github.com/google/go-cmp/cmp"
)

type parseAndMergeTC struct {
	fileContents []string
	buildArgs    []string
	env          map[string]string
	expected     map[string]string
	wantErr      error
}

func TestParseAndMerge(t *testing.T) {
	testCases := map[string]parseAndMergeTC{
		"one buildargfile": {
			fileContents: []string {
				"foo=bar\nbar=baz",
			},
			expected: map[string]string{
				"foo": "bar",
				"bar": "baz",
			},
		},
		"multiple buildargfiles": {
			fileContents: []string {
				"foo=bar\nbar=baz\nboo=see\n",
				"foo=baz", // overrides foo from previous file
				"boo", // deletes the boo key from previous file
			},
			expected: map[string]string{
				"foo": "baz",
				"bar": "baz",
			},
		},
		"just buildargs": {
			buildArgs: []string {
				"foo=bar", "bar=baz",
			},
			expected: map[string]string{
				"foo": "bar",
				"bar": "baz",
			},
		},
		"buildarg overrides": {
			fileContents: []string {
				"foo=bar\nbar=baz\n",
			},
			buildArgs: []string {
				"foo=baz", // overrides file
				"bar",     // deletes the key
				"goo=zamp",
				"goo=ximp", // overrides previous buildarg
			},
			expected: map[string]string{
				"foo": "baz",
				"goo": "ximp",
			},
		},
		"env resolve": {
			fileContents: []string {
				"foo\n",
			},
			buildArgs: []string {
				"bar",
			},
			env: map[string]string {
				"foo": "baz",
				"bar": "ximp",
			},
			expected: map[string]string{
				"foo": "baz",
				"bar": "ximp",
			},
		},
		"invalid line in build arg file": {
			fileContents: []string{
				"=value\n",
			},
			wantErr: ErrInvalidBuildArg,
		},
		"invalid build arg": {
			buildArgs: []string{
				"=value",
			},
			wantErr: ErrInvalidBuildArg,
		},
	}

	for name, tc := range testCases {
		dir := t.TempDir()
		var buildArgFiles []string
		for _, fileContent := range tc.fileContents {
			f, err := os.CreateTemp(dir, "argfile")
			if err != nil {
				t.Fatalf("failed to create tempdir")
			}

			_, err = f.WriteString(fileContent)
			if err != nil {
				t.Fatalf("failed write to prepare build arg file")
			}
			f.Close()
			buildArgFiles = append(buildArgFiles, f.Name())
		}

		t.Run(name, func(t *testing.T){
			for k, v := range tc.env {
				t.Setenv(k, v)
			}

			res, err := ParseAndMerge(buildArgFiles, tc.buildArgs)
			if tc.wantErr != nil {
				if err == nil {
					t.Fatalf("expected error wrapping %v, got nil", tc.wantErr)
				}
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("expected error wrapping %v, got: %v", tc.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			diff := cmp.Diff(tc.expected, res)
			if diff != "" {
				t.Errorf("ParseAndMerge returned unexpected result (-want +got): \n%s", diff)
			}
		})
	}
}

func TestParseAndMergeNonexistentFile(t *testing.T) {
	t.Parallel()
	_, err := ParseAndMerge([]string{"/nonexistent/path"}, nil)
	if err == nil {
		t.Fatal("expected error for nonexistent file")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected error wrapping os.ErrNotExist, got: %v", err)
	}
}

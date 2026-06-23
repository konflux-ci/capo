//go:build unit
package buildargs

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestParseBuildArgFile(t *testing.T) {
	t.Parallel()
	content := `# This is a comment
FOO=bar

BAZ=qux
# Another comment
DUP=first
DUP=second
EQUALS=a=b=c
`
	dir := t.TempDir()
	path := filepath.Join(dir, "args.conf")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	args, err := ParseBuildArgFile(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expected := map[string]string{
		"FOO":    "bar",
		"BAZ":    "qux",
		"DUP":    "second",
		"EQUALS": "a=b=c",
	}

	if diff := cmp.Diff(expected, args); diff != "" {
		t.Errorf("ParseBuildArgFile() mismatch (-want +got):\n%s", diff)
	}
}

func TestParseBuildArgFileNotFound(t *testing.T) {
	t.Parallel()
	_, err := ParseBuildArgFile("/nonexistent/path")
	if err == nil {
		t.Fatal("expected error for nonexistent file")
	}
}

func TestParseBuildArgFileInvalidLine(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.conf")
	if err := os.WriteFile(path, []byte("=value\n"), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := ParseBuildArgFile(path)
	if err == nil {
		t.Fatal("expected error for empty key")
	}
}

func TestParseBuildArgFileBareKeyInheritsFromEnv(t *testing.T) {
	t.Setenv("INHERIT_ME", "from-env")

	content := "EXPLICIT=value\nINHERIT_ME\n"
	dir := t.TempDir()
	path := filepath.Join(dir, "args.conf")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	args, err := ParseBuildArgFile(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expected := map[string]string{
		"EXPLICIT":   "value",
		"INHERIT_ME": "from-env",
	}
	if diff := cmp.Diff(expected, args); diff != "" {
		t.Errorf("ParseBuildArgFile() mismatch (-want +got):\n%s", diff)
	}
}

func TestParseBuildArgFileBareKeyDeletedWhenNotInEnv(t *testing.T) {
	t.Parallel()

	content := "KEEP=value\nNOT_IN_ENV\n"
	dir := t.TempDir()
	path := filepath.Join(dir, "args.conf")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	args, err := ParseBuildArgFile(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expected := map[string]string{
		"KEEP": "value",
	}
	if diff := cmp.Diff(expected, args); diff != "" {
		t.Errorf("ParseBuildArgFile() mismatch (-want +got):\n%s", diff)
	}
}

func TestParseBuildArgFileExplicitEmptyStaysEmpty(t *testing.T) {
	t.Setenv("EMPTY_KEY", "should-not-be-used")

	content := "EMPTY_KEY=\n"
	dir := t.TempDir()
	path := filepath.Join(dir, "args.conf")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	args, err := ParseBuildArgFile(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expected := map[string]string{
		"EMPTY_KEY": "",
	}
	if diff := cmp.Diff(expected, args); diff != "" {
		t.Errorf("ParseBuildArgFile() mismatch (-want +got):\n%s", diff)
	}
}

func TestMergeBuildArgs(t *testing.T) {
	t.Parallel()
	fileArgs := map[string]string{
		"A": "from-file",
		"B": "from-file",
	}
	cliArgs := map[string]string{
		"B": "from-cli",
		"C": "from-cli",
	}

	merged := MergeBuildArgs(fileArgs, cliArgs)

	expected := map[string]string{
		"A": "from-file",
		"B": "from-cli",
		"C": "from-cli",
	}

	if diff := cmp.Diff(expected, merged); diff != "" {
		t.Errorf("MergeBuildArgs() mismatch (-want +got):\n%s", diff)
	}
}

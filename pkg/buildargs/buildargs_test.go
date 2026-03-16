package buildargs

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseBuildArgLine(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		input   string
		wantKey string
		wantVal string
		wantErr bool
	}{
		{name: "simple", input: "FOO=bar", wantKey: "FOO", wantVal: "bar"},
		{name: "value with equals", input: "FOO=bar=baz", wantKey: "FOO", wantVal: "bar=baz"},
		{name: "empty value", input: "FOO=", wantKey: "FOO", wantVal: ""},
		{name: "no equals", input: "FOOBAR", wantErr: true},
		{name: "empty key", input: "=value", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			key, val, err := ParseBuildArgLine(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got key=%q val=%q", key, val)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if key != tt.wantKey || val != tt.wantVal {
				t.Errorf("got key=%q val=%q, want key=%q val=%q", key, val, tt.wantKey, tt.wantVal)
			}
		})
	}
}

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

	if len(args) != len(expected) {
		t.Fatalf("got %d args, want %d", len(args), len(expected))
	}
	for k, want := range expected {
		if got := args[k]; got != want {
			t.Errorf("args[%q] = %q, want %q", k, got, want)
		}
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
	if err := os.WriteFile(path, []byte("NOEQUALS\n"), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := ParseBuildArgFile(path)
	if err == nil {
		t.Fatal("expected error for invalid line")
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

	if len(merged) != len(expected) {
		t.Fatalf("got %d args, want %d", len(merged), len(expected))
	}
	for k, want := range expected {
		if got := merged[k]; got != want {
			t.Errorf("merged[%q] = %q, want %q", k, got, want)
		}
	}
}

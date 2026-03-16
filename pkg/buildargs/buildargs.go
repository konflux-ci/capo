package buildargs

import (
	"bufio"
	"errors"
	"fmt"
	"maps"
	"os"
	"strings"
)

// ErrInvalidBuildArg is returned when a build argument line is not in KEY=VALUE format.
var ErrInvalidBuildArg = errors.New("invalid build arg")

// ParseBuildArgLine parses a single KEY=VALUE line into its key and value.
// Values may contain '=' characters.
func ParseBuildArgLine(s string) (string, string, error) {
	key, value, ok := strings.Cut(s, "=")
	if !ok || key == "" {
		return "", "", fmt.Errorf("%w: expected KEY=VALUE, got %q", ErrInvalidBuildArg, s)
	}
	return key, value, nil
}

// ParseBuildArgFile reads a file of build arguments, one KEY=VALUE per line.
// Blank lines and lines starting with '#' are ignored.
// When the same key appears multiple times, the last value wins.
func ParseBuildArgFile(path string) (result map[string]string, err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("opening build-arg-file: %w", err)
	}
	defer func() {
		if cerr := f.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("closing build-arg-file: %w", cerr)
		}
	}()

	args := make(map[string]string)
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, err := ParseBuildArgLine(line)
		if err != nil {
			return nil, fmt.Errorf("in %s: %w", path, err)
		}
		args[key] = value
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	return args, nil
}

// MergeBuildArgs merges file args with CLI args. CLI args take precedence.
func MergeBuildArgs(fileArgs, cliArgs map[string]string) map[string]string {
	merged := make(map[string]string, len(fileArgs)+len(cliArgs))
	maps.Copy(merged, fileArgs)
	maps.Copy(merged, cliArgs)
	return merged
}

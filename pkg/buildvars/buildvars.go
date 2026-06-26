// Package buildvars provides functions for parsing build arguments environment
// variables passed to the build.
package buildvars

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

// ReadBuildVariable parses a build argument or env variable and writes it into args, matching
// buildah semantics: KEY=VALUE stores the literal value (even if empty),
// bare KEY inherits from the host environment (or deletes the key if unset).
func ReadBuildVariable(variable string, vars map[string]string) error {
	key, value, hasValue, err := parseBuildVarLine(variable)
	if err != nil {
		return err
	}
	if hasValue {
		vars[key] = value
	} else if val, ok := os.LookupEnv(key); ok {
		vars[key] = val
	} else {
		delete(vars, key)
	}
	return nil
}

// parseBuildVarLine parses a single build argument string.
// It accepts both KEY=VALUE (explicit value) and KEY (no equals, inherit from
// environment). The hasValue return indicates whether an explicit value was
// provided: true for KEY= or KEY=VALUE, false for bare KEY.
func parseBuildVarLine(s string) (key string, value string, hasValue bool, err error) {
	k, v, ok := strings.Cut(s, "=")
	if k == "" {
		return "", "", false, fmt.Errorf("%w: empty key in %q", ErrInvalidBuildArg, s)
	}
	if !ok {
		return k, "", false, nil
	}
	return k, v, true, nil
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
		if err := ReadBuildVariable(line, args); err != nil {
			return nil, fmt.Errorf("in %s: %w", path, err)
		}
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

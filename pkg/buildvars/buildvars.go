// Package buildvars provides functions for parsing build arguments environment
// variables passed to the build.
package buildvars

import (
	"bufio"
	"errors"
	"fmt"
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

// parseBuildArgFile reads a file of build arguments, one KEY=VALUE per line.
// Blank lines and lines starting with '#' are ignored.
// When the same key appears multiple times, the last value wins.
// buildah semantics: KEY=VALUE stores the literal value (even if empty),
// bare KEY inherits from the host environment (or deletes the key if unset).
func parseBuildArgFile(path string, vars map[string]string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("opening build-arg-file: %w", err)
	}
	defer func() {
		if cerr := f.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("closing build-arg-file: %w", cerr)
		}
	}()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if err := ReadBuildVariable(line, vars); err != nil {
			return fmt.Errorf("in %s: %w", path, err)
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("reading %s: %w", path, err)
	}
	return nil
}

// ParseAndMerge parses the passed buildArgFiles and buildArgs and merges them
// according to buildah's logic.
// BuildArgFiles are read before buildArgs, so buildArgs always have priority.
// Multiple buildArgs are parsed according to the order provided, so the later
// buildArgs have priority.
// Also handles resolving args from env for bare keys.
func ParseAndMerge(buildArgFiles []string, buildArgs []string) (map[string]string, error) {
	args := make(map[string]string)

	for _, fpath := range buildArgFiles {
		err := parseBuildArgFile(fpath, args)
		if err != nil {
			return args, fmt.Errorf("failed to parse file %q: %w", fpath, err)
		}
	}

	for _, bArg := range buildArgs {
		err := ReadBuildVariable(bArg, args)
		if err != nil {
			return args, fmt.Errorf("failed to parse build variable %q: %w", bArg, err)
		}
	}

	return args, nil
}

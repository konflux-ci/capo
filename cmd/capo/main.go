package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"runtime/debug"
	"strings"

	"github.com/konflux-ci/capo/pkg"
	"github.com/konflux-ci/capo/pkg/containerfile"
)

type args struct {
	// Path to the containerfile to parse
	containerfilePath string
	// Build arguments passed to buildah for the build
	buildArgs map[string]string
	// Target stage of the buildah build
	target string
}

var ErrBuildArg = errors.New("invalid build args syntax")
var ErrNoContainerfile = errors.New("containerfile argument is required")
var ErrJSONEncode = errors.New("error while encoding package metadata")

// Define and parse command line arguments and return an "args" struct or an error.
func parseArgs() (args, error) {
	cfPath := flag.String(
		"containerfile",
		"",
		"Path to the Containerfile used in the build. Required.",
	)

	buildArgs := make(map[string]string)
	flag.Func(
		"build-arg",
		"Build argument passed to buildah in the form KEY=VALUE. Can be used multiple times.",
		func(s string) error {
			parts := strings.Split(s, "=")
			if len(parts) != 2 || parts[0] == "" {
				return ErrBuildArg
			}
			buildArgs[parts[0]] = parts[1]
			return nil
		},
	)

	target := flag.String(
		"target",
		"",
		"Build target passed to buildah, if any.",
	)

	flag.Parse()

	if *cfPath == "" {
		flag.Usage()
		return args{}, ErrNoContainerfile
	}

	return args{
		containerfilePath: *cfPath,
		target:            *target,
		buildArgs:         buildArgs,
	}, nil
}

// Build buildah-specific arguments from capo commandline arguments.
// These are used in the containerfile parser.
func buildOptsFromArgs(args args) containerfile.BuildOptions {
	return containerfile.BuildOptions{
		Args:   args.buildArgs,
		Target: args.target,
	}
}

func logRevision() {
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, s := range info.Settings {
			if s.Key == "vcs.revision" {
				log.Printf("Capo was built from revision %q", s.Value)
				return
			}
		}
		// vcs.revision is only available after build, "go run" isn't enough
		log.Println("Could not find key vcs.revision in build information")
	} else {
		log.Println("Could not read capo build information")
	}
}

func main() {
	logRevision()

	args, err := parseArgs()
	if err != nil {
		log.Fatalf("%v", err)
	}

	r, err := os.Open(args.containerfilePath)
	if err != nil {
		log.Fatalf("Could not open %s: %+v", args.containerfilePath, err)
	}
	defer func() {
		if r.Close() != nil {
			log.Fatalf("Could not close %s", args.containerfilePath)
		}
	}()

	stages, err := containerfile.Parse(r, buildOptsFromArgs(args))
	if err != nil {
		log.Fatalf("Failed to parse containerfile %+v", err)
	}
	log.Printf("Parsed stages: %+v", stages)

	pkgMetadata, err := capo.Scan(stages)
	if err != nil {
		log.Fatalf("Failed to scan stages: %+v", err)
	}

	if err := printPkgMetadata(pkgMetadata); err != nil {
		log.Fatalf("Failed to serialize and print package metadata")
	}
}

// Serialize and print package metadata to stdout.
func printPkgMetadata(pkgMetadata capo.PackageMetadata) error {
	var buf bytes.Buffer

	encoder := json.NewEncoder(&buf)
	encoder.SetIndent("", "  ")
	err := encoder.Encode(pkgMetadata)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrJSONEncode, err)
	}

	fmt.Println(buf.String())
	return nil
}

package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
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
			if len(parts) != 2 {
				return fmt.Errorf("Invalid build arg syntax for %s.", s)
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
		fmt.Fprintln(os.Stderr, "Path to the used Containerfile argument is required.")
		flag.Usage()
		return args{}, fmt.Errorf("Error while parsing arguments, exiting.")
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

func main() {
	args, err := parseArgs()
	if err != nil {
		log.Fatalf("%+v", err)
	}

	r, err := os.Open(args.containerfilePath)
	if err != nil {
		log.Fatalf("Could not open %s: %+v", args.containerfilePath, err)
	}

	stages, err := containerfile.Parse(r, buildOptsFromArgs(args))
	if err != nil {
		log.Fatalf("Failed to parse containerfile %+v", err)
	}
	log.Printf("Parsed stages: %+v", stages)

	pkgMetadata, err := capo.Scan(stages)
	if err != nil {
		log.Fatalf("Failed to scan stages: %+v", err)
	}

	printPkgMetadata(pkgMetadata)
}

// Serialize and print package metadata to stdout.
func printPkgMetadata(pkgMetadata capo.PackageMetadata) {
	var buf bytes.Buffer

	encoder := json.NewEncoder(&buf)
	encoder.SetIndent("", "  ")
	encoder.Encode(pkgMetadata)

	fmt.Println(buf.String())
}

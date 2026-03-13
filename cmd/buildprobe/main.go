package main

import (
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"runtime/debug"
	"strings"

	"github.com/konflux-ci/capo/pkg/probe"
	"github.com/konflux-ci/capo/pkg/repository"
	"go.yaml.in/yaml/v3"
)

func logRevision() {
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, s := range info.Settings {
			if s.Key == "vcs.revision" {
				log.Printf("buildprobe was built from revision %q", s.Value)
				return
			}
		}
		// vcs.revision is only available after build, "go run" isn't enough
		log.Println("Could not find key vcs.revision in build information")
	} else {
		log.Println("Could not read buildprobe build information")
	}

}

type args struct {
	// TODO: description
	tag string
	// Path to the containerfile to parse
	containerfilePath string
	// Build arguments passed to buildah for the build
	buildArgs map[string]string
	// Target stage of the buildah build
	target string
}

var ErrBuildArg = errors.New("invalid build args syntax")
var ErrNoContainerfile = errors.New("containerfile argument is required")
var ErrNoTag = errors.New("tag argument is required")
var ErrYAMLEncode = errors.New("error while encoding build metadata")

// Define and parse command line arguments and return an "args" struct or an error.
func parseArgs() (args, error) {
	tag := flag.String(
		"tag",
		"",
		"Tag of the built image.",
	)

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

	// FIXME: build arg file argument support

	flag.Parse()

	if *cfPath == "" {
		flag.Usage()
		return args{}, ErrNoContainerfile
	}

	if *tag == "" {
		flag.Usage()
		return args{}, ErrNoTag
	}

	return args{
		tag:               *tag,
		containerfilePath: *cfPath,
		target:            *target,
		buildArgs:         buildArgs,
	}, nil
}

func main() {
	logRevision()

	args, err := parseArgs()
	if err != nil {
		log.Fatalf("%v", err)
	}

	cfReader, err := os.Open(args.containerfilePath)
	if err != nil {
		log.Fatalf("Could not open %s: %+v", args.containerfilePath, err)
	}
	defer func() {
		if cfReader.Close() != nil {
			log.Fatalf("Could not close %s", args.containerfilePath)
		}
	}()

	repo, err := repository.NewBuildahRepository()
	if err != nil {
		log.Fatalf("Could not create buildah repository: %s", err)
	}

	meta, err := probe.Probe(probe.ProbeOpts{
		Containerfile: cfReader,
		Target:        args.target,
		Tag:           args.tag,
		Args:          args.buildArgs,
	}, repo)
	if err != nil {
		log.Fatalf("Failed to probe build metadata %+v", err)
	}

	if err := printBuildMetadata(meta); err != nil {
		log.Fatalf("Failed to encode build metadata: %+v", err)
	}
}

// Serialize and print package metadata to stdout.
func printBuildMetadata(meta probe.BuildMetadata) error {
	d, err := yaml.Marshal(&meta)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrYAMLEncode, err)
	}

	fmt.Println(string(d))
	return nil
}

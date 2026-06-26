package main

import (
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"runtime/debug"

	"github.com/konflux-ci/capo/pkg/buildvars"
	"github.com/konflux-ci/capo/pkg/probe"
	"github.com/konflux-ci/capo/pkg/storageclient"
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
	// Tag of the built image
	tag string
	// Path to the containerfile to parse
	containerfilePath string
	// Build arguments passed to buildah for the build
	buildArgs map[string]string
	// Environment variables passed to the build
	envVars map[string]string
	// Target stage of the buildah build
	target string
}

var ErrBuildArg = errors.New("invalid build args syntax")
var ErrBuildEnv = errors.New("invalid build env syntax")
var ErrNoContainerfile = errors.New("containerfile argument is required")
var ErrNoTag = errors.New("tag argument is required")
var ErrYAMLEncode = errors.New("error while encoding build metadata")

// Define and parse command line arguments and return an "args" struct or an error.
func parseArgs() (args, error) {
	tag := flag.String(
		"tag",
		"",
		"Tag of the built image. Required.",
	)

	cfPath := flag.String(
		"containerfile",
		"",
		"Path to the Containerfile used in the build. Required.",
	)

	cliArgs := make(map[string]string)
	flag.Func(
		"build-arg",
		"Build argument in the form KEY=VALUE or bare KEY (inherits from environment). Can be used multiple times.",
		func(s string) error {
			if err := buildvars.ReadBuildVariable(s, cliArgs); err != nil {
				return ErrBuildArg
			}
			return nil
		},
	)

	cliEnv := make(map[string]string)
	flag.Func(
		"env",
		"Environment variable in the form KEY=VALUE or bare KEY (inherits from environment). Can be used multiple times.",
		func(s string) error {
			if err := buildvars.ReadBuildVariable(s, cliEnv); err != nil {
				return ErrBuildEnv
			}
			return nil
		},
	)
	target := flag.String(
		"target",
		"",
		"Build target passed to buildah, if any.",
	)

	buildArgFile := flag.String(
		"build-arg-file",
		"",
		"Path to a file of build arguments (one KEY=VALUE per line). Read before --build-arg values.",
	)

	flag.Parse()

	var buildArgs map[string]string
	if *buildArgFile != "" {
		fileArgs, err := buildvars.ParseBuildArgFile(*buildArgFile)
		if err != nil {
			return args{}, fmt.Errorf("parsing build-arg-file: %w", err)
		}
		buildArgs = buildvars.MergeBuildArgs(fileArgs, cliArgs)
	} else {
		buildArgs = cliArgs
	}

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
		envVars:           cliEnv,
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

	client, err := storageclient.DefaultBuildahClient()
	if err != nil {
		log.Fatalf("Could not create storage client: %s", err)
	}

	meta, err := probe.Probe(probe.ProbeOpts{
		Containerfile: cfReader,
		Target:        args.target,
		Tag:           args.tag,
		Args:          args.buildArgs,
		EnvVars:       args.envVars,
	}, client)
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

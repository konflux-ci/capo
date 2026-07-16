package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"os"
	"runtime/debug"
	"strings"

	"github.com/konflux-ci/capo/pkg"
	"github.com/konflux-ci/capo/pkg/buildvars"
	"github.com/konflux-ci/capo/pkg/containerfile"
)

type args struct {
	// Path to the containerfile to parse
	containerfilePath string
	// Build arguments passed to buildah for the build
	buildArgs []string
	// Build arg files passed to buildah for the build
	buildArgFiles []string
	// Environment variables passed to the build
	envVars map[string]string
	// Target stage of the buildah build
	target string
	// Named build contexts passed to the build
	buildContexts map[string]string
	// Cataloger selection expressions for syft (same syntax as syft --select-catalogers)
	selectCatalogers []string
}

var ErrBuildContext = errors.New("invalid build context syntax, expected name=value")
var ErrEnvVar = errors.New("invalid environment variable syntax")
var ErrNoContainerfile = errors.New("containerfile argument is required")
var ErrJSONEncode = errors.New("error while encoding package metadata")

// Define and parse command line arguments and return an "args" struct or an error.
func parseArgs() (args, error) {
	cfPath := flag.String(
		"containerfile",
		"",
		"Path to the Containerfile used in the build. Required.",
	)

	var buildArgs []string
	flag.Func(
		"build-arg",
		"Build argument in the form KEY=VALUE or bare KEY (inherits from environment). Can be used multiple times.",
		func(s string) error {
			buildArgs = append(buildArgs, s)
			return nil
		},
	)

	var buildArgFiles []string
	flag.Func(
		"build-arg-file",
		"Path to a file of build arguments (one KEY=VALUE per line). " +
		"Read before --build-arg values. Can be used multiple times.",
		func (s string) error {
			buildArgFiles = append(buildArgFiles, s)
			return nil
		},
	)

	buildEnvVars := make(map[string]string)
	flag.Func(
		"env",
		"Environment variable in the form KEY=VALUE or bare KEY (inherits from environment). Can be used multiple times.",
		func(s string) error {
			if err := buildvars.ReadBuildVariable(s, buildEnvVars); err != nil {
				return ErrEnvVar
			}
			return nil
		},
	)

	buildContexts := make(map[string]string)
	flag.Func(
		"build-context",
		"Named build context in the form name=value. Can be used multiple times.",
		func(s string) error {
			name, value, ok := strings.Cut(s, "=")
			if !ok || name == "" {
				return ErrBuildContext
			}
			buildContexts[name] = value
			return nil
		},
	)

	selectCatalogersFlag := flag.String(
		"select-catalogers",
		"",
		"Comma-separated cataloger selection expressions for syft (e.g. \"os,+rpm-db-cataloger,-python\").",
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

	var selectCatalogers []string
	if *selectCatalogersFlag != "" {
		selectCatalogers = strings.Split(*selectCatalogersFlag, ",")
	}

	return args{
		containerfilePath: *cfPath,
		target:            *target,
		buildArgs:         buildArgs,
		buildArgFiles:     buildArgFiles,
		envVars:           buildEnvVars,
		buildContexts:     buildContexts,
		selectCatalogers:  selectCatalogers,
	}, nil
}

// Build buildah-specific arguments from capo commandline arguments.
// These are used in the containerfile parser.
func buildOptsFromArgs(args args) (containerfile.BuildOptions, error) {
	buildArgs, err := buildvars.ParseAndMerge(args.buildArgFiles, args.buildArgs)
	if err != nil {
		return containerfile.BuildOptions{},
			fmt.Errorf("failed to parse build args: %w", err)
	}

	return containerfile.BuildOptions{
		Args:          buildArgs,
		EnvVars:       args.envVars,
		Target:        args.target,
		BuildContexts: args.buildContexts,
	}, nil
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

	buildOpts, err := buildOptsFromArgs(args)
	if err != nil {
		log.Fatalf("Failed to create build options: %+v", err)
	}

	cf, err := containerfile.Parse(r, buildOpts)
	if err != nil {
		log.Fatalf("Failed to parse containerfile %+v", err)
	}
	log.Printf("Parsed stages: %+v", cf.Stages)

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))

	scanner, err := capo.NewScanner(
		capo.WithLogger(logger),
		capo.WithSelectCatalogers(args.selectCatalogers...),
	)
	if err != nil {
		log.Fatalf("Failed to create scanner: %+v", err)
	}

	pkgMetadata, err := scanner.Scan(cf)
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

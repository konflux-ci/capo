package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"

	"capo/pkg"
)

type args struct {
	containerfilePath string
}

func parseArgs() (args, error) {
	cfPathFlag := "containerfile"
	cfPathUsage := "Path to the Containerfile used in the build. Required."
	cfPath := flag.String(cfPathFlag, "", cfPathUsage)

	flag.Parse()

	if *cfPath == "" {
		fmt.Fprintln(os.Stderr, "Path to the used Containerfile argument is required.")
		flag.Usage()
		return args{}, fmt.Errorf("Error while parsing arguments, exiting.")
	}

	return args{
		containerfilePath: *cfPath,
	}, nil
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

	stages, err := capo.ParseContainerfile(r)
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

func printPkgMetadata(pkgMetadata capo.PackageMetadata) {
	var buf bytes.Buffer

	encoder := json.NewEncoder(&buf)
	encoder.SetIndent("", "  ")
	encoder.Encode(pkgMetadata)

	fmt.Println(buf.String())
}

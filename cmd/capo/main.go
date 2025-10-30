package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"os"

	"capo/pkg"
)

func main() {
	r, err := os.Open("./Containerfile")
	stages, err := capo.ParseContainerfile(r)
	if err != nil {
		log.Fatalf("Failed to parse containerfile %+v", err)
	}
	log.Printf("Parsed stages: %+v", stages)
	return

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

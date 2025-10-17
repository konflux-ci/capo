package main

import (
	"log"

	"capo/pkg"
)

func main() {
	stages, err := capo.ParseContainerfile("")
	if err != nil {
		log.Fatalf("Failed to parse containerfile %+v", err)
	}
	log.Printf("Parsed stages: %+v", stages)

	output := "./output"

	result, err := capo.Scan(stages, output)
	if err != nil {
		log.Fatalf("Failed to scan stages: %+v", err)
	}

	result.Print()
}

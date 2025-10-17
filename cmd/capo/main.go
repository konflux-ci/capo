package main

import (
	"log"

	"capo/pkg"
	"capo/pkg/stage"

	"go.podman.io/storage"
	"go.podman.io/storage/pkg/reexec"
)

func setupStore() storage.Store {
	// The containers/storage library requires this to run for some operations
	if reexec.Init() {
		log.Fatalln("Failed to init reexec")
	}

	opts, err := storage.DefaultStoreOptions()
	if err != nil {
		log.Fatalln("Failed to create default container storage options")
	}

	store, err := storage.GetStore(opts)
	if err != nil {
		log.Fatalln("Failed to create container storage")
	}

	return store
}

func main() {
	store := setupStore()

	stages, err := stage.ParseContainerfile("")
	if err != nil {
		log.Fatalf("Failed to parse containerfile %+v", err)
	}
	log.Printf("Parsed stages: %+v", stages)

	builderData := make([]capo.ScanResult, 0)
	output := "./output"
	for _, stage := range stages {
		data, err := capo.Scan(store, stage, output)
		if err != nil {
			log.Fatalf("Failed to scan stage %+v with error: %v.", stage, err)
		}

		builderData = append(builderData, data)
	}

	// build and write the index
	index := capo.Index{
		Builder: builderData,
	}
	iPath, err := index.Write(output)
	if err != nil {
		log.Fatalf("Failed to write index to %s with error: %v.", iPath, err)
	}

	log.Printf("Written index to %s.", iPath)
}

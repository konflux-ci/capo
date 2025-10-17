package main

import (
	"log"

	"capo/pkg"
	"capo/pkg/includer"

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
	input := capo.ParsedContainerfile{
		BuilderStages: []includer.Stage{
			capo.NewStage(
				"fedora-builder",
				"docker.io/library/fedora:latest",
				[]includer.Copier{
					capo.NewCopy(
						[]string{"/usr/bin/kubectl"},
						"/usr/bin/kubectl",
						capo.FinalStage,
					),
				},
			),
			capo.NewStage(
				"helm-builder",
				"docker.io/alpine/helm:latest",
				[]includer.Copier{
					capo.NewCopy(
						[]string{"/usr/bin/helm"},
						"/usr/bin/helm",
						capo.FinalStage,
					),
				},
			),
		},
		ExternalStages: []includer.Stage{
			capo.NewStage(
				"",
				"quay.io/konflux-ci/oras:41b74d6",
				[]includer.Copier{
					capo.NewCopy(
						[]string{"/usr/bin/oras"},
						"/usr/bin/oras",
						capo.FinalStage,
					),
				},
			),
		},
	}

	store := setupStore()

	// scan builder and intermediate content
	builderIncluders := includer.NewBuilderIncluders(input.BuilderStages)
	log.Printf("Parsed builder includers: %+v", builderIncluders)

	builderData := make([]capo.BuilderScanResult, 0)
	output := "./output"
	for _, builder := range input.BuilderStages {
		inc := builderIncluders.GetIncluderForAlias(builder.Alias())

		data, err := capo.ScanBuilder(store, builder, inc, output)
		if err != nil {
			log.Fatalf("Failed to scan builder %+v with error: %v.", builder, err)
		}

		builderData = append(builderData, data)
	}

	// scan external image content in final stage
	externalData := make([]capo.ExternalScanResult, 0)
	for _, external := range input.ExternalStages {
		inc := includer.External(external)
		log.Printf("Parsed external includer for %s: %+v", external.Pullspec(), inc)

		data, err := capo.ScanExternal(store, external, inc, output)
		if err != nil {
			log.Fatalf("Failed to scan external image %+v with error: %v.", external, err)
		}

		externalData = append(externalData, data)
	}

	// build and write the index
	index := capo.Index{
		Builder:  builderData,
		External: externalData,
	}
	iPath, err := index.Write(output)
	if err != nil {
		log.Fatalf("Failed to write index to %s with error: %v.", iPath, err)
	}

	log.Printf("Written index to %s.", iPath)
}

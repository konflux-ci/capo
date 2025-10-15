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
		Builders: []includer.StageData{},
		Externals: []includer.StageData{
			capo.NewStage(
				"",
				"quay.io/konflux-ci/oras:41b74d6",
				[]includer.Copier{
					capo.Copy{
						Source: []string{"/usr/bin/oras"},
						Dest:   "/bin/oras",
						Stage:  capo.FinalStage,
					},
				},
			),
		},
	}

	store := setupStore()
	builderIncluders := includer.NewBuilderIncluders(input.Builders)
	log.Printf("Parsed builder includers: %+v", builderIncluders)

	builderData := make([]capo.BuilderImage, 0)
	output := "./output"
	for _, builder := range input.Builders {
		inc := builderIncluders.GetMask(builder)

		data, err := capo.ScanBuilder(store, output, builder, inc)
		if err != nil {
			log.Fatalf("Failed to scan builder %+v with error: %v.", builder, err)
		}

		builderData = append(builderData, data)
	}

	// scan external image content in final stage
	externalData := make([]capo.ExternalImage, 0)
	for _, external := range input.Externals {
		inc := includer.External(external)
		log.Printf("Parsed external includer for %s: %+v", external.Pullspec(), inc)

		data, err := capo.ScanExternal(store, external, output, inc)
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

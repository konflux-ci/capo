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
	input := capo.BuildData{
		Builders: []includer.StageData{
			capo.Builder{
				StagePullspec: "quay.io/konflux-ci/oras:41b74d6",
				StageAlias:    "builder1",
				StageCopies: []includer.Copier{
					capo.Copy{
						Source: []string{"/content"},
						Dest:   "/content",
						Stage:  capo.FinalStage,
					},
					capo.Copy{
						Source: []string{"/usr/bin/oras"},
						Dest:   "/usr/bin/oras",
						Stage:  capo.FinalStage,
					},
				},
			},
		},
	}

	store := setupStore()
	builderIncluders := includer.NewBuilderIncluders(input.Builders)
	log.Printf("Parsed builder includers: %+v", builderIncluders)

	builderData := make([]capo.BuilderImage, 0)
	output := "./output"
	for _, builder := range input.Builders {
		copyMask := builderIncluders.GetMask(builder)
		data, err := capo.Scan(store, output, builder, copyMask)
		if err != nil {
			log.Fatalf("Failed to scan builder %+v with error: %v.", builder, err)
		}
		builderData = append(builderData, data)
	}

	index := capo.Index{
		Builder: builderData,
	}
	iPath, err := index.Write(output)
	if err != nil {
		log.Fatalf("Failed to write index to %s with error: %v.", iPath, err)
	}

	log.Printf("Written index to %s.", iPath)
}

package capo

// TODO: is Stage a good name for this? It might be confused with actual stages.
// Maybe name this PackageSource or smth better.
type Stage interface {
	// Get the stage alias of this stage or empty string
	// if this is only an external stage, i.e. there are COPY commands
	// only in the form of 'COPY --from=image:tag ... ...'.
	Alias() string

	// Get the pullspec of this stage.
	Pullspec() string

	// Get a slice of paths to content in the layer/image which should be syft-scanned.
	Sources() []string
}

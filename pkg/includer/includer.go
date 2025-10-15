package includer

type Includer interface {
	// Returns a slice of paths to content in the layer/image which should be syft-scanned.
	Sources() []string
}

type Copier interface {
	// Get slice of sources of this COPY command.
	Sources() []string

	// Get the destination of this COPY command.
	Destination() string

	// Returns true if this is a COPY command from the final build stage.
	IsFromFinalStage() bool
}

type StageData interface {
	// Get the stage alias of this stage or empty string
	// if the stage has no alias (such as in external copies).
	Alias() string

	// Get slice of structs implementing the Copier interface.
	// This represents COPY commands from this stage in other stages.
	Copies() []Copier

	// Get the pullspec of this stage.
	Pullspec() string
}

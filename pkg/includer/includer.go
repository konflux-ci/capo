package includer

// FIXME: extend the documentation in this file

type Includer interface {
	// Returns true if content in the specified path should be included.
	Includes(path string) bool

	// Returns a slice of strings of paths whose content (including subpaths) should be included.
	// Used to filter builder content more effectively.
	GetSources() []string
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

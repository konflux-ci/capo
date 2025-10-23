package includer

type external struct {
	sources []string
}

func External(data StageData) Includer {
	sources := make([]string, 0)
	for _, cp := range data.Copies() {
		sources = append(sources, cp.Sources()...)
	}

	return external{
		sources: sources,
	}
}

func (e external) Sources() []string {
	return e.sources
}

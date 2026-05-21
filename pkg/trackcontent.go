/*
The trackcontent package traces content origins from COPY and RUN --mount
instructions across stages in a containerfile.
*/
package capo

import (
	"fmt"
	"slices"

	"github.com/konflux-ci/capo/pkg/containerfile"
)

// collectStageSources traces content origins from copies and mounts across stages.
// Returns stageToSources (stages with content reaching the final image) and
// mountStageSources (stages referenced only via mounts in non-final stages).
func collectStageSources(
	stages []containerfile.Stage,
	aliasToStage map[string]*containerfile.Stage,
	baseToWorkdir map[string]string,
) (stageToSources, mountStageSources map[*containerfile.Stage][]string) {
	stageToSources = make(map[*containerfile.Stage][]string)
	mountStageSources = make(map[*containerfile.Stage][]string)
	final := &stages[len(stages)-1]

	traceCopySources(final.Copies, aliasToStage, baseToWorkdir, stageToSources)

	for _, mount := range final.Mounts {
		collectMountSource(mount, aliasToStage, stageToSources, stageToSources)
	}

	for i := range stages[:len(stages)-1] {
		for _, mount := range stages[i].Mounts {
			collectMountSource(mount, aliasToStage, mountStageSources, stageToSources)
		}
	}

	return stageToSources, mountStageSources
}

// traceCopySources traces content origins for all copies in the final stage.
// Builder copies are recursively traced via traceSource; external copies create
// synthetic stages in the accumulator.
func traceCopySources(
	copies []containerfile.Copy,
	aliasToStage map[string]*containerfile.Stage,
	baseToWorkdir map[string]string,
	stageToSources map[*containerfile.Stage][]string,
) {
	for _, cp := range copies {
		for _, source := range cp.Sources {
			if _, isBuilder := aliasToStage[cp.From]; isBuilder {
				traceSource(source, aliasToStage[cp.From], stageToSources, aliasToStage, baseToWorkdir)
				continue
			}
			external := &containerfile.Stage{
				Alias:  "",
				Base:   cp.From,
				Copies: []containerfile.Copy{},
			}
			stageToSources[external] = append(stageToSources[external], source)
		}
	}
}

// collectMountSource adds a mount's source path to the appropriate target map.
// Builder mounts add to builderTarget, external mounts create a synthetic stage
// in externalTarget. Source paths are deduplicated per stage.
func collectMountSource(
	mount containerfile.Mount,
	aliasToStage map[string]*containerfile.Stage,
	builderTarget map[*containerfile.Stage][]string,
	externalTarget map[*containerfile.Stage][]string,
) {
	if mount.Type == containerfile.MountTypeBuilder {
		if stage, ok := aliasToStage[mount.From]; ok {
			if !slices.Contains(builderTarget[stage], mount.Source) {
				builderTarget[stage] = append(builderTarget[stage], mount.Source)
			}
		}
		return
	}
	external := &containerfile.Stage{
		Alias:  "",
		Base:   mount.From,
		Copies: []containerfile.Copy{},
	}
	externalTarget[external] = append(externalTarget[external], mount.Source)
}

// buildPackageSourceSlices constructs the reportable and mount-only packageSource
// slices from the collected stage-to-sources maps.
func buildPackageSourceSlices(
	stages []containerfile.Stage,
	stageToSources map[*containerfile.Stage][]string,
	mountStageSources map[*containerfile.Stage][]string,
	resolvedPullspecs map[string]string,
) (reportable []packageSource, mountOnly []packageSource, err error) {
	reportable = make([]packageSource, 0, len(stages))

	for i := range stages[:len(stages)-1] {
		stage := &stages[i]

		digestPullspec, ok := resolvedPullspecs[stage.Base]
		if !ok {
			return nil, nil,
				fmt.Errorf("%w %q: could not find resolved pullspec", ErrPullspecResolve, stage.Base)
		}

		sources := mergeStageSources(stageToSources[stage], mountStageSources[stage])
		if sources == nil && mountStageSources[stage] != nil {
			mountOnly = append(mountOnly, packageSource{
				alias:      stage.Alias,
				pullspec:   stage.Base,
				digestBase: digestPullspec,
				sources:    mountStageSources[stage],
			})
			continue
		}

		reportable = append(reportable, packageSource{
			alias:      stage.Alias,
			pullspec:   stage.Base,
			digestBase: digestPullspec,
			sources:    sources,
		})

		delete(stageToSources, stage)
	}

	for stage, sources := range stageToSources {
		digestPullspec, ok := resolvedPullspecs[stage.Base]
		if !ok {
			return nil, nil,
				fmt.Errorf("%w %q: could not find resolved pullspec", ErrPullspecResolve, stage.Base)
		}
		reportable = append(reportable, packageSource{
			alias:      stage.Alias,
			pullspec:   stage.Base,
			digestBase: digestPullspec,
			sources:    sources,
		})
	}

	return reportable, mountOnly, nil
}


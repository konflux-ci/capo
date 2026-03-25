// Package probe extracts build metadata from a Containerfile without
// performing a build. It parses the Containerfile, identifies base and extra
// images, and optionally resolves their digests via an ImageStore.
package probe

import (
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/konflux-ci/capo/pkg/containerfile"
	"github.com/konflux-ci/capo/pkg/imagestore"
)

// Image represents a container image identified by its pullspec and,
// when resolved, its content digest.
type Image struct {
	// Pullspec of the image as found in the Containerfile
	Pullspec string
	// Digest in the form sha256:<digest>
	Digest   string
}

// BuildMetadata holds the images involved in a container build: the built
// image itself, its base images (FROM lines), and any extra images referenced
// via COPY --from or RUN --mount.
type BuildMetadata struct {
	Image       Image   `yaml:"image"`
	BaseImages  []Image `yaml:"base_images"`
	ExtraImages []Image `yaml:"extra_images"`
}

// ProbeOpts configures a Probe invocation.
type ProbeOpts struct {
	// Tag of the built image
	Tag string
	// Reader of the containerfile
	Containerfile io.Reader
	// Target stage of the build
	Target string
	// Build args
	Args map[string]string
}

// ErrParseContainerfile is returned when the Containerfile cannot be parsed.
var ErrParseContainerfile = errors.New("could not parse containerfile")

// ErrDigestResolve is returned when an image digest cannot be resolved.
var ErrDigestResolve = errors.New("failed to resolve digest of image")

// Probe parses the Containerfile described in opts and collects build metadata.
// When repo is non-nil, image digests are resolved through the image store.
func Probe(opts ProbeOpts, store imagestore.ImageStore) (BuildMetadata, error) {
	meta := BuildMetadata{}

	meta.Image.Pullspec = opts.Tag

	if store != nil {
		digest, err := store.ResolveDigest(opts.Tag)
		if err != nil {
			return meta, fmt.Errorf("%w %q: %w", ErrDigestResolve, opts.Tag, err)
		}

		meta.Image.Digest = digest
	}

	stages, err := containerfile.Parse(
		opts.Containerfile,
		containerfile.BuildOptions{
			Args:   opts.Args,
			Target: opts.Target,
		},
	)
	if err != nil {
		return meta, fmt.Errorf("%w: %w", ErrParseContainerfile, err)
	}

	reachable := reachableStages(stages)

	baseImages, err := resolveBaseImages(store, reachable)
	if err != nil {
		return meta, err
	}

	meta.BaseImages = baseImages

	extraImages, err := resolveExtraImages(store, reachable)
	if err != nil {
		return meta, err
	}
	meta.ExtraImages = extraImages

	return meta, nil
}

// reachableStages returns only the stages transitively reachable (via BFS) from the
// final stage via FROM, COPY --from, and RUN --mount references.
func reachableStages(stages []containerfile.Stage) []containerfile.Stage {
	if len(stages) == 0 {
		return stages
	}

	// Map stage aliases to all their indexes. Multiple stages can share the
	// same alias. Buildah builds all matching stages, so we must track every
	// occurrence. The final stage has Alias==FinalStage so it won't collide
	// with named builder stages.
	stagesByAlias := make(map[string][]int)
	for stageIndex, stage := range stages {
		stagesByAlias[stage.Alias] = append(stagesByAlias[stage.Alias], stageIndex)
	}

	// findMatchingStages returns all stage indexes that match a reference,
	// either by alias or by numeric index.
	findMatchingStages := func(ref string) ([]int, bool) {
		if indexes, ok := stagesByAlias[ref]; ok {
			return indexes, true
		}
		if stageIndex, err := strconv.Atoi(ref); err == nil && 0 <= stageIndex && stageIndex < len(stages) {
			return []int{stageIndex}, true
		}
		return nil, false
	}

	isReachable := make([]bool, len(stages))
	stagesToProcess := []int{len(stages) - 1}
	isReachable[len(stages)-1] = true

	enqueue := func(stageIndex int) {
		if !isReachable[stageIndex] {
			isReachable[stageIndex] = true
			stagesToProcess = append(stagesToProcess, stageIndex)
		}
	}

	for len(stagesToProcess) > 0 {
		stageIndex := stagesToProcess[0]
		stagesToProcess = stagesToProcess[1:]
		stage := stages[stageIndex]

		for _, ref := range stageRefs(stage) {
			if matches, ok := findMatchingStages(ref); ok {
				for _, matchIndex := range matches {
					enqueue(matchIndex)
				}
			}
		}
	}

	reachableStages := make([]containerfile.Stage, 0, len(stages))
	for stageIndex, stage := range stages {
		if isReachable[stageIndex] {
			reachableStages = append(reachableStages, stage)
		}
	}
	return reachableStages
}

// stageRefs returns all references to other stages from a given stage:
// the FROM base image and all builder-type COPY --from and RUN --mount refs.
func stageRefs(stage containerfile.Stage) []string {
	refs := []string{stage.Base}
	for _, cp := range stage.Copies {
		if cp.Type == containerfile.CopyTypeBuilder {
			refs = append(refs, cp.From)
		}
	}
	for _, mount := range stage.Mounts {
		if mount.Type == containerfile.MountTypeBuilder {
			refs = append(refs, mount.From)
		}
	}
	return refs
}

func resolveBaseImages(store imagestore.ImageStore, stages []containerfile.Stage) ([]Image, error) {
	res := make([]Image, 0)
	seen := make(map[string]bool)

	for _, stage := range stages {
		if stage.Base == "scratch" || strings.HasPrefix(stage.Base, "oci:archive") {
			continue
		}

		if seen[stage.Base] {
			continue
		}
		seen[stage.Base] = true

		digest := ""
		var err error

		if store != nil {
			digest, err = store.ResolveDigest(stage.Base)
			if err != nil {
				return nil, fmt.Errorf("%w %q: %w", ErrDigestResolve, stage.Base, err)
			}
		}
		res = append(res, Image{
			Pullspec: stage.Base,
			Digest:   digest,
		})
	}

	return res, nil
}

func resolveExtraImages(store imagestore.ImageStore, stages []containerfile.Stage) ([]Image, error) {
	res := make([]Image, 0)
	seen := make(map[string]bool)

	addImage := func(pullspec string) error {
		if seen[pullspec] {
			return nil
		}
		seen[pullspec] = true

		digest := ""
		var err error

		if store != nil {
			digest, err = store.ResolveDigest(pullspec)
			if err != nil {
				return fmt.Errorf("%w %q: %w", ErrDigestResolve, pullspec, err)
			}
		}

		res = append(res, Image{
			Pullspec: pullspec,
			Digest:   digest,
		})
		return nil
	}

	for _, stage := range stages {
		for _, cp := range stage.Copies {
			if cp.Type != containerfile.CopyTypeExternal {
				continue
			}
			if err := addImage(cp.From); err != nil {
				return nil, err
			}
		}

		for _, mount := range stage.Mounts {
			if mount.Type != containerfile.MountTypeExternal {
				continue
			}
			if err := addImage(mount.From); err != nil {
				return nil, err
			}
		}
	}

	return res, nil
}

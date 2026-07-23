// Package probe extracts build metadata from a Containerfile without
// performing a build. It parses the Containerfile, identifies base and extra
// images, and optionally resolves their digests via an ImageStore.
package probe

import (
	"errors"
	"fmt"
	"io"
	"strconv"

	"github.com/konflux-ci/capo/pkg/containerfile"
	"github.com/konflux-ci/capo/pkg/storageclient"
	"github.com/opencontainers/go-digest"
)

// Image represents a container image identified by its pullspec and,
// when resolved, its content digest.
type Image struct {
	// Pullspec of the image as found in the Containerfile
	Pullspec string
	// Digest in the form sha256:<digest>
	Digest string
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
	tag string
	// Reader of the containerfile
	containerfile io.Reader
	// Target stage of the build
	target string
	// Build args
	args map[string]string
	// Environment variables passed to the build
	envVars map[string]string
	// Named build contexts passed to the build
	buildContexts map[string]string
	// In multi-stage builds, skip stages that don't contribute to the final
	// stage.
	skipUnusedStages bool
}

// ErrParseContainerfile is returned when the Containerfile cannot be parsed.
var ErrParseContainerfile = errors.New("could not parse containerfile")

// ErrDigestResolve is returned when an image digest cannot be resolved.
var ErrDigestResolve = errors.New("failed to resolve digest of image")

type ProbeOption func (*ProbeOpts)

// Set the target used for the build. If unset, defaults to last stage.
func WithTarget(target string) ProbeOption {
	return func (opts *ProbeOpts) {
		opts.target = target
	}
}

// Set the build args used for the build.
func WithArgs(args map[string]string) ProbeOption {
	return func (opts *ProbeOpts) {
		opts.args = args
	}
}

// Set the envs used for the build.
func WithEnvVars(envVars map[string]string) ProbeOption {
	return func (opts *ProbeOpts) {
		opts.envVars = envVars
	}
}

// Set the BuildContexts used for the build.
func WithBuildContexts(buildContexts map[string]string) ProbeOption {
	return func (opts *ProbeOpts) {
		opts.buildContexts = buildContexts
	}
}

// Set the SkipUnusedStages option. If unset, defaults to true.
func WithSkipUnusedStages(skipUnusedStages bool) ProbeOption {
	return func (opts *ProbeOpts) {
		opts.skipUnusedStages = skipUnusedStages
	}
}

// Probe parses the passed containerfile and collects build metadata. When
// client is non-nil, image digests are resolved through the storage client.
func Probe(
	tag string, cfile io.Reader, client storageclient.Client, options ...ProbeOption,
) (BuildMetadata, error) {
	opts := ProbeOpts {
		tag: tag,
		containerfile: cfile,
		target: "",
		skipUnusedStages: true,
		args: make(map[string]string),
		buildContexts: make(map[string]string),
		envVars: make(map[string]string),
	}

	for _, o := range options {
		o(&opts)
	}

	meta := BuildMetadata{}
	meta.Image.Pullspec = opts.tag

	if client != nil {
		digest, err := client.ResolveDigest(opts.tag)
		if err != nil {
			return meta, fmt.Errorf("%w %q: %w", ErrDigestResolve, opts.tag, err)
		}

		meta.Image.Digest = digest.String()
	}

	cf, err := containerfile.Parse(
		opts.containerfile,
		containerfile.BuildOptions{
			Args:          opts.args,
			EnvVars:       opts.envVars,
			Target:        opts.target,
			BuildContexts: opts.buildContexts,
		},
	)
	if err != nil {
		return meta, fmt.Errorf("%w: %w", ErrParseContainerfile, err)
	}

	// If SkipUnusedStages == false, all stages up to and including the target
	// will be built by buildah.
	reachable := cf.Stages
	if opts.skipUnusedStages {
		reachable = reachableStages(cf.Stages)
	}

	baseImages, err := resolveBaseImages(client, reachable)
	if err != nil {
		return meta, err
	}

	meta.BaseImages = baseImages

	extraImages, err := resolveExtraImages(client, reachable)
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
	refs := []string{stage.BaseRef}
	for _, cp := range stage.Copies {
		if cp.Type == containerfile.CopyTypeBuilder {
			refs = append(refs, cp.From)
		}
	}
	for _, mount := range stage.Mounts {
		if mount.Pullspec == "" {
			refs = append(refs, mount.FromRaw)
		}
	}
	return refs
}

func resolveBaseImages(client storageclient.Client, stages []containerfile.Stage) ([]Image, error) {
	res := make([]Image, 0)
	seen := make(map[string]bool)

	for _, stage := range stages {
		if storageclient.IsSpecialBase(stage.Base) {
			continue
		}

		if seen[stage.Base] {
			continue
		}
		seen[stage.Base] = true

		var digest digest.Digest = ""
		var err error

		if client != nil {
			digest, err = client.ResolveDigest(stage.Base)
			if err != nil {
				return nil, fmt.Errorf("%w %q: %w", ErrDigestResolve, stage.Base, err)
			}
		}
		res = append(res, Image{
			Pullspec: stage.Base,
			Digest:   digest.String(),
		})
	}

	return res, nil
}

func resolveExtraImages(client storageclient.Client, stages []containerfile.Stage) ([]Image, error) {
	res := make([]Image, 0)
	seen := make(map[string]bool)

	addImage := func(pullspec string) error {
		if seen[pullspec] {
			return nil
		}
		seen[pullspec] = true

		var digest digest.Digest = ""
		var err error

		if client != nil {
			digest, err = client.ResolveDigest(pullspec)
			if err != nil {
				return fmt.Errorf("%w %q: %w", ErrDigestResolve, pullspec, err)
			}
		}

		res = append(res, Image{
			Pullspec: pullspec,
			Digest:   digest.String(),
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
			if mount.Pullspec == "" {
				continue
			}
			if err := addImage(mount.Pullspec); err != nil {
				return nil, err
			}
		}
	}

	return res, nil
}

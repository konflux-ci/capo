// Package probe extracts build metadata from a Containerfile without
// performing a build. It parses the Containerfile, identifies base and extra
// images, and optionally resolves their digests via a Repository.
package probe

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/konflux-ci/capo/pkg/containerfile"
	"github.com/konflux-ci/capo/pkg/repository"
)

// Image represents a container image identified by its pullspec and,
// when resolved, its content digest.
type Image struct {
	Pullspec string
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
	// If empty, the built image won't be resolved.
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
// When repo is non-nil, image digests are resolved through the repository.
func Probe(opts ProbeOpts, repo repository.Repository) (BuildMetadata, error) {
	meta := BuildMetadata{}

	meta.Image.Pullspec = opts.Tag

	if repo != nil {
		digest, err := repo.ResolveDigest(opts.Tag)
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

	baseImages, err := resolveBaseImages(repo, stages)
	if err != nil {
		return meta, err
	}

	meta.BaseImages = baseImages

	extraImages, err := resolveExtraImages(repo, stages)
	if err != nil {
		return meta, err
	}
	meta.ExtraImages = extraImages

	return meta, nil
}

func resolveBaseImages(repo repository.Repository, stages []containerfile.Stage) ([]Image, error) {
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

		if repo != nil {
			digest, err = repo.ResolveDigest(stage.Base)
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

func resolveExtraImages(repo repository.Repository, stages []containerfile.Stage) ([]Image, error) {
	res := make([]Image, 0)
	seen := make(map[string]bool)

	addImage := func(pullspec string) error {
		if seen[pullspec] {
			return nil
		}
		seen[pullspec] = true

		digest := ""
		var err error

		if repo != nil {
			digest, err = repo.ResolveDigest(pullspec)
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

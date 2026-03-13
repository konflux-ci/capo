package probe

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/konflux-ci/capo/pkg/containerfile"
	"github.com/konflux-ci/capo/pkg/repository"
)

type Image struct {
	Pullspec string
	Digest   string
}

type BuildMetadata struct {
	Image       Image   `yaml:"image"`
	BaseImages  []Image `yaml:"base_images"`
	ExtraImages []Image `yaml:"extra_images"`
}

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

var ErrParseContainerfile = errors.New("could not parse containerfile")
var ErrDigestResolve = errors.New("failed to resolve digest of image")

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

	for _, stage := range stages {
		for _, cp := range stage.Copies {
			if cp.Type != containerfile.CopyTypeExternal {
				continue
			}

			if seen[cp.From] {
				continue
			}
			seen[cp.From] = true

			digest := ""
			var err error

			if repo != nil {
				digest, err = repo.ResolveDigest(cp.From)
				if err != nil {
					return nil, fmt.Errorf("%w %q: %w", ErrDigestResolve, stage.Base, err)
				}
			}

			res = append(res, Image{
				Pullspec: cp.From,
				Digest:   digest,
			})
		}
	}

	return res, nil
}

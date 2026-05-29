# Design Intent

Key invariants, preconditions, and architectural rationale for capo and
buildprobe.

## Why `buildah unshare`

Capo and buildprobe access the local buildah image store via
`go.podman.io/storage`. This library requires root-level access to mount image
layers and read image configuration. `buildah unshare` creates a user namespace
that maps the current UID to root (UID 0) inside the namespace, allowing
storage access without actual root privileges. Without it, both tools fail with
permission errors.

## Why `--save-stages --stage-labels`

By default, buildah discards intermediate images after a multi-stage build.
Capo needs these images to diff their layers against the builder base image and
determine what content was installed during the build. The `--save-stages` flag
tells buildah to preserve intermediate images in storage, and `--stage-labels`
adds labels to each one (`io.buildah.stage.name` - this is alias of the stage
and `io.buildah.stage.base` - this is pullspec of the stage).
Capo uses these labels to match intermediate images to their corresponding
builder stages and base images. These flags are available since
[buildah 1.44.0](https://github.com/containers/buildah/releases). Without
them, capo fails to find intermediate images and returns
`ErrMissingStageLabel` or `ErrUnsupportedBuildahVersion`.

## Why `exclude_graphdriver_btrfs`

The `go.podman.io/storage` library supports multiple storage drivers including
BTRFS. Compiling with BTRFS support requires C libraries (`libbtrfs-dev`) that
are not needed — capo uses the overlay driver. The `exclude_graphdriver_btrfs`
build tag excludes BTRFS code from compilation, removing the C dependency.

## Why `openshift/imagebuilder`

Capo uses `github.com/openshift/imagebuilder` to parse Containerfiles — the
same library that buildah uses internally. Imagebuilder handles Dockerfile AST
parsing, ARG evaluation, and stage resolution, ensuring these produce identical
results to what buildah sees during the actual build. COPY and RUN --mount
extraction is done by capo's own code (`parseStageRefs`, `parseCopy`,
`parseMounts`) by walking the AST nodes from imagebuilder.

## Builder vs intermediate content

For each builder stage, capo extracts two categories of content:

- **Builder content** — packages present in the builder base image itself
  (e.g. from the FROM image). Extracted by mounting the base image and copying
  only the paths that were COPY-ied into the final stage. Not applicable for
  special bases - scratch have no builder content and oci-archive content is
  considered as an intermediate content.
- **Intermediate content** — packages installed during the build stage (e.g.
  by RUN commands). For standard bases, it is extracted by diffing the intermediate
  image top layer against the builder base image top layer as a tar stream,
  isolating only the newly added content and copying only the paths that were
  COPY-ied into the final stage. For special bases (scratch, oci-archive),
  there is no base to diff against.

This distinction is captured in the `origin_type` field of the output
(`"builder"` or `"intermediate"`).

## Why Syft extracts only top-level packages

`internal/sbom/` uses Anchore Syft to scan extracted content directories. Only
packages with a direct CONTAINS relationship from the document root are
included — transitive dependencies are excluded. This keeps the output focused
on packages that are directly present in the image content rather than their
full dependency trees. During contextualization in mobster, only CONTAINS
relationships are reparented to the true origin image, associated DEPENDENCY_OF
relationships and the full dependency tree remain unchanged in the final
image SBOM.

## Capo, buildprobe and mobster pipeline

Capo and buildprobe are two independent tools whose outputs are consumed by
[mobster](https://github.com/konflux-ci/mobster):

- **capo** - JSON with per-package origin metadata - mobster uses this for
  builder content expression (after parent conten identification) in the
  [Contextual SBOM](https://github.com/konflux-ci/mobster/blob/main/docs/sboms/oci_image.md#contextual-sbom)
- **buildprobe** - YAML with image pullspecs and digests - mobster uses this
  as `SBOMMetadata` to extend the SBOM with base and extra image references
  and to identify the parent image for the contextual SBOM workflow

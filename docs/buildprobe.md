# Usage

buildprobe extracts build metadata from a Containerfile without performing an
actual build. It parses the Containerfile, resolves image digests via the local
buildah store, and prints the result as YAML to stdout.

Because buildprobe accesses the buildah image store, it must run under
`buildah unshare` (rootless) or as root.

## Synopsis

```
buildah unshare buildprobe [flags]
```

## Flags

- `-containerfile` _path_ — Path to the Containerfile used in the build. **Required.**
- `-tag` _pullspec_ — Tag of the built image. **Required.**
- `-build-arg` _KEY=VALUE_ — Build argument passed to buildah. Can be specified multiple times.
- `-build-arg-file` _path_ — Path to a file of build arguments, one `KEY=VALUE` per line. Blank lines and lines starting with `#` are ignored. Read before `-build-arg` values.
- `-target` _stage_ — Build target stage passed to buildah, if any. When set, only stages reachable from the target are included in the output.

When the same build argument key appears in both `-build-arg-file` and
`-build-arg`, the `-build-arg` value takes precedence.

## Output

buildprobe writes YAML to stdout with the following structure:

```yaml
image:
    pullspec: <tag>
    digest: <resolved_digest>
base_images:
    - pullspec: <image_reference>
      digest: <resolved_digest>
extra_images:
    - pullspec: <image_reference>
      digest: <resolved_digest>
```

- **image** — The built image identified by `-tag`, with its resolved digest.
- **base_images** — Images from `FROM` instructions that are reachable from the
  final (or target) stage. Images named `scratch` and `oci-archive:` references
  are excluded.
- **extra_images** — Images referenced via `COPY --from=<image>` or
  `RUN --mount=type=bind,from=<image>` that are not builder stages defined in
  the Containerfile.

## Examples

Basic usage:

```bash
buildah unshare buildprobe \
    -containerfile=Containerfile \
    -tag=quay.io/myorg/myimage:latest
```

With build arguments from a file and CLI overrides:

```bash
buildah unshare buildprobe \
    -build-arg-file=build-args.env \
    -build-arg ALPINE_TAG=3.21 \
    -containerfile=Containerfile \
    -tag=quay.io/myorg/myimage:latest
```

Building a specific target stage:

```bash
buildah unshare buildprobe \
    -containerfile=Containerfile \
    -target=packager \
    -tag=quay.io/myorg/myimage:latest
```

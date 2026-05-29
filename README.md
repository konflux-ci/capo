# Capo

Capo is a program and library that traces the origin of content copied from
builder stages to the final built image (by `COPY --from=stage_alias`) for multi-stage container builds. After a
[buildah](https://github.com/containers/buildah) build, it parses the
Containerfile, inspects all participating images in the local buildah image
store (builder base images, their intermediate images, and extra images from
`COPY --from=image:tag` instructions), and uses
[Syft](https://github.com/anchore/syft) to identify packages in extracted
content. For each package it distinguishes whether it originates from the
builder base image or was installed during the intermediate build stage.
Capo's output is JSON with per-package metadata (PURL, origin type, pullspec,
stage alias), consumed by [mobster](https://github.com/konflux-ci/mobster) for
builder content expression in Contextual SBOM or used as standalone manifest
of the origin of the builder content for given built multistage image.

The repository also contains **buildprobe**, a separate CLI tool that
identifies all images participating in a build and resolves their digests.
See [docs/buildprobe.md](docs/buildprobe.md) for details.

For architectural rationale and key invariants, see
[docs/design/design.md](docs/design/design.md).

Part of the [Konflux CI](https://github.com/konflux-ci) project.

> [!WARNING]
> This project is a work in progress, and its API is unstable. Until version
> v1.0.0 is available, the API might change on minor version increase.


## Prerequisites

- Go (version in `go.mod`)
- [buildah](https://github.com/containers/buildah) >= 1.44.0 with
  `--save-stages --stage-labels` support

## Quickstart

Install capo:
```sh
go install github.com/konflux-ci/capo/cmd/capo@latest
```

Build your image with buildah (note `--save-stages --stage-labels` flags
required for capo to find intermediate images and pair them with related builder base images) and run capo under
`buildah unshare`:
```sh
buildah build --save-stages --stage-labels -f Containerfile
buildah unshare capo --containerfile=Containerfile
```

When building with `--target` or `--build-arg`, pass the same options to capo:
```sh
buildah build --save-stages --stage-labels -f Containerfile \
    --target builder --build-arg KEY=VAL
buildah unshare capo --containerfile=Containerfile \
    --target=builder --build-arg=KEY=VAL
```

For the full list of options:
```sh
capo -h
```

### Example output

Given a Containerfile (producing `localhost/myimage:latest`):
```dockerfile
FROM registry.access.redhat.com/ubi9/ubi-minimal:latest AS builder
RUN microdnf install -y python3 && microdnf clean all

FROM scratch
COPY --from=builder /usr /usr
COPY --from=ghcr.io/anchore/syft:latest /syft /usr/local/bin/syft
```

Capo outputs JSON to stdout. Each entry identifies a package, its origin type
(`builder` = from the base image, `intermediate` = installed during the build
stage), and the source image pullspec with digest:

```json
{
  "packages": [
    {
      "purl": "pkg:rpm/rhel/python3@3.9.18-3.el9",
      "origin_type": "intermediate",
      "pullspec": "registry.access.redhat.com/ubi9/ubi-minimal@sha256:def456...",
      "stage_alias": "builder"
    },
    {
      "purl": "pkg:rpm/rhel/glibc@2.34-83.el9",
      "origin_type": "builder",
      "pullspec": "registry.access.redhat.com/ubi9/ubi-minimal@sha256:def456...",
      "stage_alias": "builder"
    },
    {
      "purl": "pkg:golang/github.com/anchore/syft@v1.32.0",
      "origin_type": "builder",
      "pullspec": "ghcr.io/anchore/syft@sha256:789fed..."
    }
  ]
}
```

Buildprobe outputs YAML to stdout with the built image, base images, and
extra images with their resolved digests:

```yaml
image:
    pullspec: localhost/myimage:latest
    digest: sha256:abc123...
base_images:
    - pullspec: registry.access.redhat.com/ubi9/ubi-minimal:latest
      digest: sha256:def456...
extra_images:
    - pullspec: ghcr.io/anchore/syft:latest
      digest: sha256:789fed...
```

## Contributing
The project uses [mage](https://github.com/magefile/mage) as a runner, to make
common operations simple to run. To list available targets for the project, you
can run:
```sh
mage -l
```

If you don't already have mage available, you can use the following command to
install it:
```sh
go install github.com/magefile/mage@latest
```

The project also uses
[golangci-lint](https://github.com/golangci/golangci-lint) to run linters:
```sh
curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/HEAD/install.sh | sh -s -- -b $(go env GOPATH)/bin v2.6.1
mage lint
```

### Running Tests

Unit tests:
```sh
mage test
```

Unit tests with coverage profile:
```sh
mage coverage
```

All builds and tests require the `exclude_graphdriver_btrfs` build tag (handled
automatically by mage). Unit tests use the `unit` build tag, integration tests
use the `integration` tag — see `magefile.go` for exact flags.

To run capo locally during development:
```sh
mage run '--containerfile=Containerfile'
```

This wraps the command in `buildah unshare` automatically.

### Commit Messages

This project enforces
[Conventional Commits](https://www.conventionalcommits.org/) via
[gitlint](https://jorisroovers.com/gitlint/) in CI. Commits must follow:

```
type(scope): description
```

- **Allowed types:** `chore`, `docs`, `feat`, `fix`, `refactor`, `style`, `test`, `revert`
- **Scope** is optional (e.g. `fix(ISV-1234): resolve layer matching`)
- **Title** max 72 characters, **body** max 88 characters per line

To validate your commits locally before pushing:
```sh
pip install gitlint
gitlint --commits origin/main..HEAD
```

### Integration Tests

Integration tests require buildah with `--save-stages --stage-labels` support
(>= 1.44.0). Until that version is widely available, a pre-release version of
buildah is built from source:

```sh
mage buildCustomBuildah
```

This places the binary in `testdata/bin/buildah` without modifying your system
buildah. If your system buildah already supports `--save-stages`, the build is
skipped automatically.

To run integration tests:

```sh
mage integrationTest
```

The test runner uses `testdata/bin/buildah` if present, otherwise falls back to
system buildah. Test cases are defined in `pkg/integration_test.go` — see the
`TestCase` and `BuildDefinition` struct documentation for details on writing
new tests.

# AGENTS.md

Repository guidance for coding agents. Human-oriented setup, CI, and contributing info live in [README.md](README.md); this file stays short and navigation-focused (max 60 lines enforced in CI).

## What is Capo

Capo is a CLI tool and library that traces the origin of content sourced from builder images to final built image in multi-stage container builds. It parses a Containerfile, inspects all images involved in the build (builder base images, their intermediate images, and extra images from `COPY --from` / `RUN --mount`), and for each stage-sourced package distinguishes whether it originates from the builder base image or was installed in the intermediate image. For each traced stage it diffs intermediate image against builder base image to resolve content origin and runs Syft to produce per-package metadata (PURL, origin type, pullspec). It is a part of [Konflux CI](https://github.com/konflux-ci) - output is consumed by [mobster](https://github.com/konflux-ci/mobster) for builder content expression in Contextual SBOMs, enabling faster CVE source identification and remediation for multi-stage builds.

## What is Buildprobe

Buildprobe (`cmd/buildprobe/`) is a separate CLI tool in this repo. It parses a Containerfile after a build, identifies all participating images (final built image, base images from FROM, extra images from COPY --from / RUN --mount), and resolves their digests via buildah storage. Output is YAML (`image`, `base_images`, `extra_images`) consumed by [mobster](https://github.com/konflux-ci/mobster) as `SBOMMetadata` for base/extra image inclusion in the SBOM. See `docs/buildprobe.md` for flags and examples.

## Non-negotiables

- **Never run capo or buildprobe directly** - always wrap with `buildah unshare` (user namespace required by containers/storage).
- **Build tags** - all builds require `exclude_graphdriver_btrfs`. Unit tests need `unit` tag, integration tests need `integration` tag. See `magefile.go` for exact flags.
- **Buildah >= 1.44.0** - capo expects images built with `buildah build --save-stages --stage-labels` to find saved and labeled intermediate images and pair them with appropriate builder base image of given stage for proper content tracing.
- **Merge-ready:** `mage lint`, `mage test` and `mage integrationTest` must pass before treating work as complete.
- **Error conventions** - sentinel errors in `pkg/` must contain a machine-readable code in square brackets followed by a description, e.g. `errors.New("[ERR_STORAGE_SETUP] failed to set up container storage")`. Wrap sentinels with `fmt.Errorf("detail: %w", ErrSentinel)` — no custom error types needed.

## Toolchain

- Go (version in `go.mod`), task runner: **[mage](https://github.com/magefile/mage)** (`go install github.com/magefile/mage@latest`), linter: **golangci-lint** (config: `.golangci.yaml`).
- Quality gates: `mage lint` (golangci-lint), `mage test` (unit), `mage coverage` (unit + profile). Integration: `mage integrationTest` (requires buildah >= 1.44.0 with `--save-stages --stage-labels`).
- Single-file check: `golangci-lint run path/to/file.go`, `go vet ./pkg/...`.

## Repository map

| Area | Path |
|------|------|
| CLI (capo) | `cmd/capo/` |
| CLI (buildprobe) | `cmd/buildprobe/` (see `docs/buildprobe.md`) |
| Core logic | `pkg/scan.go` (source tracing), `pkg/content.go` (content extraction and layer diffing), `pkg/containerfile/containerfile.go` (Containerfile parsing) |
| Build arg handling | `pkg/buildargs/` |
| Image store abstraction | `pkg/imagestore/` |
| Buildprobe logic | `pkg/probe/` (used by `cmd/buildprobe/`) |
| SBOM scanning (Syft) | `internal/sbom/` |
| Unit tests | `pkg/*_test.go`, `pkg/containerfile/*_test.go`, `pkg/buildargs/*_test.go`, `pkg/probe/*_test.go` |
| Integration tests | `pkg/integration_test.go` (builds real images, needs buildah) |
| Build orchestration | `magefile.go` |
| Design intent | `docs/design/design.md` (invariants, rationale) |

## Architecture hooks

- **scan.go** entry point (`Scan()`): sets up buildah storage via `reexec.Init()` (process may fork), resolves pullspecs to digests, traces COPY sources recursively through stages, extracts content, runs Syft. Set `CAPO_DEBUG=1` to preserve temp directories.
- **content.go** mounts images via containers/storage, diffs intermediate image layers against base image as tar streams, finds intermediate images by buildah stage labels.
- **containerfile/** uses `openshift/imagebuilder` (same parser as buildah). ARG values evaluated during parsing. Named contexts (`--build-context`) not yet supported.
- **probe/** does BFS reachability from final stage through FROM/COPY/mount chains. Digest resolution requires buildah storage, but works without it (returns pullspecs only).
- **internal/sbom/** wraps Anchore Syft for package scanning. Requires `modernc.org/sqlite` import for RPM cataloger. Only extracts top-level packages (CONTAINS relationship).

## Testing notes

Unit tests use table-driven patterns with `google/go-cmp`. Integration tests build real container images - run `mage buildCustomBuildah` first if system buildah < 1.44.0 (binary goes to `testdata/bin/buildah`, does not affect system).

## Scope for agents

Keep changes minimal and scoped to the task. Avoid drive-by refactors - fix what's broken, leave the rest alone.

<!-- Last reviewed: 2026-Q2. Next review: 2026-Q3 in https://redhat.atlassian.net/browse/ISV-7230 -->

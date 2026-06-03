---
name: debugging
description: "Use when debugging capo scan failures, inspecting builder, intermediate or extra images in buildah storage, investigating why packages are missing or unexpected in output, understanding what content capo extracted, or when CI fails with build tag or buildah version errors"
---

# Debugging

## Overview

Capo operates inside a buildah user namespace on images in local storage.
Debugging requires inspecting both capo's extracted content and the state of
images (their labels or content) participated in given build in buildah storage.

## When to Use

- Capo returns empty `{"packages": []}` unexpectedly
- Capo returns unexpected packages
- Any capo error (see error reference below)
- Need to verify what content capo extracted before Syft scanning
- Fixing a bug — start by reproducing with a failing test (see [testing skill](../testing/SKILL.md#bug-fix-workflow))

## Error Reference

| Error | Cause | Fix |
|-------|-------|-----|
| `ErrStorageSetup` | Can't connect to buildah storage | Run under `buildah unshare`; if already under unshare, check storage with `buildah info` |
| `ErrImageNotFound` / `ErrPullspecResolve` | Image not in storage | Build the image first, verify with `buildah images`, check same user namespace |
| `ErrImageMount` | Can't mount image layers | Storage permissions or corruption, try `buildah info` |
| `ErrUnsupportedBuildahVersion` | Image built with buildah < 1.44.0 | Rebuild image with buildah >= 1.44.0 - ask user for updating system buildah or use `mage buildCustomBuildah` and use built binary for build depending on the situation |
| `ErrMissingStageLabel` | Intermediate image has no stage label | Rebuild with `--save-stages --stage-labels` (buildah >= 1.44.0) |
| `ErrParse` | Containerfile parsing failed | Can be invalid syntax, ARG resolution error, or bug in capo's COPY/mount parsing — check wrapped error message |
| `ErrTargetNotFound` | `--target` stage doesn't exist | Check stage name in Containerfile |
| `ErrSyft` | Syft scan failed | Use `CAPO_DEBUG=1` to inspect extracted content directory |

## Quick Reference

| Tool | Command |
|------|---------|
| Run capo locally | `buildah unshare capo '--containerfile=Containerfile'` |
| Preserve extracted content | `CAPO_DEBUG=1 buildah unshare capo --containerfile=...` |
| List images in storage | `buildah images` |
| Inspect image labels | `buildah inspect <image ID>` |
| Check buildah version | `buildah --version` |

## CAPO_DEBUG Mode

When `CAPO_DEBUG=1` is set, capo:
- Prints temp directory paths for each stage's builder and intermediate content
- Does NOT delete temp directories after scanning
- Allows manual inspection of extracted files before Syft processes them

```
DEBUG builder content path pullspec=registry.example.com/base:latest path=/tmp/12345
DEBUG intermediate content path pullspec=registry.example.com/base:latest path=/tmp/67890
```

Inspect the `path=` directories to verify correct content was extracted.

## Debugging Empty Output

Empty output is **expected** when:
- Containerfile is not multi-stage (no builder stages)
- Final OR target stage has no `COPY --from=stage` instructions
- COPY paths don't contain any package manifests (go.mod, RPM db, etc.)

Checklist when capo returns no packages unexpectedly:

1. **Check buildah version** — must be >= 1.44.0
2. **Check build flags** — was image built with `--save-stages --stage-labels`?
3. **Check intermediate images** — `buildah images` should show intermediate
   images with stage labels
4. **Inspect labels** — `buildah inspect <intermediate-image>` should have
   `io.buildah.stage.name` (stage alias) and `io.buildah.stage.base` (stage pullspec
   or alias of parent stage if stage uses another stage as base - is chained stage)
   in config labels.
   Note: builder base images do NOT have these labels — only intermediate images do.
5. **Enable CAPO_DEBUG** — run with `CAPO_DEBUG=1`, check extracted content in
   logged temp directories (builder and intermediate content paths are printed)
6. **Check COPY paths** — capo only extracts paths that were COPY-ied into the
   final stage; if the COPY path doesn't contain package manifests (go.mod,
   RPM db), Syft won't find packages
7. **Mount and inspect images manually** — create a container from the image,
   mount it, and check if content exists at the COPY path (`buildah mount`
   works with containers, not images directly):
   ```sh
   buildah unshare bash -c '
     ctr=$(buildah from <image-id>)
     mnt=$(buildah mount $ctr)
     ls -la $mnt/path/from/containerfile
     buildah umount $ctr
     buildah rm $ctr
   '
   ```
8. **Direct Syft scan** — if content exists, try scanning the mounted path
   directly to verify Syft can find packages:
   ```sh
   buildah unshare bash -c '
     ctr=$(buildah from <image-id>)
     mnt=$(buildah mount $ctr)
     syft dir:$mnt/path/from/containerfile
     buildah umount $ctr
     buildah rm $ctr
   '
   ```

## CI Failure Patterns

| Symptom | Cause | Fix |
|---------|-------|-----|
| Compile errors referencing BTRFS or missing C libs | Missing build tag | Add `-tags=exclude_graphdriver_btrfs` or use `mage` |
| `go test` passes with 0 test cases | Missing `//go:build unit` or `integration` tag | Use `mage test` or add `-tags=unit,exclude_graphdriver_btrfs` |
| `ErrMissingStageLabel` / `ErrUnsupportedBuildahVersion` in CI | System buildah too old | Run `mage buildCustomBuildah` first |
| `pr.yaml` gitlint job fails | Non-conventional commit message | Format: `type(scope): description`; check with `gitlint --commits origin/main..HEAD` |
| AGENTS.md CI check fails | File exceeds 60 lines | Shorten AGENTS.md (enforced in `lint.yaml`) |
| Coverage check fails | New code not covered | Codecov threshold: 1% drop; add tests |

## Common Mistakes

- Forgetting `--save-stages --stage-labels` during build — no intermediate images
- Looking for packages in content that has no package manifests (binary-only copies)
- Using `go test ./...` without tags — tests exist but don't run
- Adding a workflow without `exclude_graphdriver_btrfs` in go commands
- Forgetting `fetch-depth: 0` in checkout when gitlint needs commit history

## Additional Resources

- Repo overview: [AGENTS.md](../../AGENTS.md)
- Design rationale: [docs/design/design.md](../../docs/design/design.md)
- Writing tests: [testing](../testing/SKILL.md)

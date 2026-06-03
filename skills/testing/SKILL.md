---
name: testing
description: "Use when writing new unit or integration tests for capo, adding test cases to integration_test.go, structuring test expectations with PackageMetadataItem, or choosing between unit and integration test scope"
---

# Testing

## Overview

Capo uses build-tag-separated test suites: `//go:build unit` for fast tests
without buildah, `//go:build integration` for almost end-to-end tests that build real
container images and scan them.

## When to Use

- Adding a new test case for a Containerfile pattern
- Verifying capo output for a new COPY/mount scenario
- Unsure whether to write a unit or integration test

## Unit vs Integration

| Aspect | Unit (`//go:build unit`) | Integration (`//go:build integration`) |
|--------|--------------------------|----------------------------------------|
| Run with | `mage test` | `mage integrationTest` |
| Needs buildah | No | Yes (>= 1.44.0) |
| Tests what | Parsing, source tracing, path matching | Full build-scan-compare cycle |
| Location | `pkg/*_test.go` per package | `pkg/integration_test.go` |
| Pattern | Table-driven `map[string]struct{}` | `TestCase` with `BuildDefinition` |

**Rule of thumb:** if it needs `buildah build` or `storage.Store`, it's
integration. Everything else is unit.

## Adding an Integration Test

1. Add entry to `testCases` map in `TestIntegration`:

```go
"Description of what this test verifies": {
    TestImage: BuildDefinition{
        ContainerfileContent: `FROM localhost/my-base:latest AS builder
                               RUN something
                               FROM scratch
                               COPY --from=builder /app /app`,
        ContextDirectory: "../testdata/image_content",
    },
    BuilderImages: []BuildDefinition{
        {
            Tag: "localhost/my-base:latest",
            ContainerfileContent: `FROM scratch
                                   COPY some.file /app/some.file`,
            ContextDirectory: "../testdata/image_content",
        },
    },
    ExpectedResult: PackageMetadata{
        Packages: []PackageMetadataItem{
            {
                PackageURL: "pkg:golang/example.com/pkg@v1.0.0",
                OriginType: "builder",
                Pullspec:   "localhost/my-base@sha256:dummy",
                StageAlias: "builder",
            },
        },
    },
},
```

2. Key fields:
   - `Tag` — auto-normalized (adds `localhost/`, `:latest` if missing)
   - `ContainerfileContent` — inline only, no file paths
   - `ContextDirectory` — relative to `pkg/`, typically `../testdata/image_content`
   - `Pullspec` in expected — use `@sha256:dummy`, digests are stripped during comparison

3. Comparison uses `go-cmp` with:
   - `cmpopts.SortSlices` — order-independent matching on `PackageURL`
   - `cmpopts.EquateEmpty` — nil and empty slices are equal
   - `cmp.FilterPath` on `Pullspec` — strips digests before comparing

## Where to Add Tests

| Change type | Test location |
|-------------|---------------|
| Containerfile parsing logic | `pkg/containerfile/containerfile_test.go` |
| Source tracing behavior | `pkg/scan_test.go` |
| Content extraction logic | `pkg/integration_test.go` |
| Buildprobe behavior | `pkg/probe/probe_test.go` |
| Build arg handling | `pkg/buildargs/buildargs_test.go` |
| Bug fix | Test that reproduces the bug before the fix |

## Bug Fix Workflow

Always use test-driven approach for bug fixes:

1. **Write a failing test first** — reproduce the bug with a test case that fails on the current code
2. **Verify the test fails** — run `mage test` and confirm the failure matches the reported bug
(redirect test logs to file for better readability and audit purposes)
3. **Fix the bug** — make the minimal change to fix the issue
4. **Verify the test passes** — run `mage test` again, the previously failing test should now pass
5. **Check for regressions** — run `mage lint` and `mage integrationTest` if applicable

Never fix first and test later — writing the test first ensures you understand the bug and that the fix actually addresses it.

## Common Mistakes

- Missing `//go:build unit` or `//go:build integration` tag — tests won't run
- Forgetting `exclude_graphdriver_btrfs` — handled by mage, but breaks with raw `go test`
- Missing `BuilderImages` — builder base must exist in storage before test image build
- Using file paths in `ContainerfileContent` — only inline content works

## Additional Resources

- Repo overview: [AGENTS.md](../../AGENTS.md)
- Debugging and CI failures: [debugging](../debugging/SKILL.md)

# Integration Tests

## Prerequisites

Capo requires buildah with `--save-stages --stage-labels` support for correct
intermediate image identification. This feature will be available in buildah
\>= 1.44.0, not yet released at the time of writing. Until then, a custom
buildah binary must be built from source.

### Building Custom Buildah

```bash
mage buildCustomBuildah
```

This downloads buildah from a specific commit, builds the binary and places it
in `testdata/bin/buildah`. It does **not** modify your system buildah.

If your system buildah already supports `--save-stages`, the script will detect
this and skip the build.

## Running Tests

```bash
mage integrationTest
```

The test runner automatically uses `testdata/bin/buildah` if present, otherwise
falls back to system buildah. The buildah binary path and version are logged at
the start of each test run.

## Writing Tests

The integration test framework allows creating custom test images as well as
builder images. All details about each image are specified within the `TestCase`
struct in `pkg/integration_test.go`.

### `TestCase` Fields

| Field | Type | Description |
|---|---|---|
| `Description` | `string` | Short test name, used in logs and summary |
| `LongDescription` | `string` | Additional notes describing the testing intention (or more closely describing missing feature/present bug), logged before the test runs |
| `SkipTestReason` | `string` | If non-empty, test is skipped with this reason (for unimplemented features or unresolved bugs) |
| `SkipBuild` | `bool` | Skip image building (for testing Scan errors when images are not expected to exist) |
| `ExpectedError` | `string` | If set, test expects an error containing this substring |
| `TestImage` | `BuildDefinition` | The multi-stage image to scan |
| `BuilderImages` | `[]BuildDefinition` | Pre-built builder base images for the test |
| `ExpectedResult` | `PackageMetadata` | Expected scan output for comparison |

### `BuildDefinition` Fields

| Field | Type | Description |
|---|---|---|
| `Tag` | `string` | Image tag (e.g. `localhost/foo:latest`). Auto-normalized if registry or tag is missing. Random UUID if empty. |
| `ContainerfileContent` | `string` | Inline containerfile content. Providing a file path will not work. |
| `ContextDirectory` | `string` | Path to build context, relative to `pkg/` (e.g. `../testdata/image_content`). |

Builder images are built before the test image. Their tags can be referenced
in the test image's containerfile via `FROM` or `COPY --from`.

The test image is built with `--save-stages --stage-labels` flags to enable
intermediate image labeling for capo scanning.

### Tags

Make sure each image within a test case has a unique tag. The containerfile
must contain the full pullspec including registry and tag (e.g.
`localhost/builder:latest`), since buildah does not auto-normalize these.

### Test Summary

After all tests run, a summary is printed with pass/skip/fail counts and
individual test results. Skipped tests include their skip reason in the summary.

## Cleanup

Test images and intermediate images are cleaned up automatically after each
test case.

## File Structure

```
testdata/
├── bin/
│   └── buildah          # Custom buildah binary (gitignored)
├── build_buildah.sh     # Build script for custom buildah
└── image_content/       # Test content (go.mod files for syft scanning)
```

## Troubleshooting

### Clean rebuild of custom buildah

```bash
rm -f testdata/bin/buildah
mage BuildCustomBuildah
```

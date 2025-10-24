# Integration tests

The integration tests can be run using the following command:

```bash
mage integrationTest
```

The integration test framework allows to create custom test images
as well as builder images.

Builder images are built before the test image, its tag can (and should)
be referenced within the Containerfile of the test image.

All the details about each image and its builder images shall be specified
within the `TestCase` struct. The details of this structure will be explained
below.

## `TestImage`

This field contains the necessary information about the build of the test image.
The used type is `BuildDefinition`, refer to this type for details.

## `BuilderImages`

This field contains a slice of `BuildDefinition` structs. These images are built
before the test image and can be referenced in its Containerfile.

## `ExpectedResult`

This field contains the expected result of the scan of the test image. The
type of this field is `PackageMetadata`, which is the standard output format
for Capo.


## `BuildDefinition` type

This struct contains the following fields:

- `Tag`
- `ContainerfileContent`
- `ContextDirectory`

### `Tag`

Specifies how should the built image be tagged. Useful for referencing the
builder images as well as for cleanup. Make sure to create a different tag for
each image within a test case. (No 2 builder images should share the same
tag, neither should any builder image share the same tag with the test image.)

Example value: `localhost/foo:latest`. If you don't include
the registry URL (in this case `localhost`) or a tag (`:latest`), these
default values will be provided automatically.

If omitted, a random tag will be created with the use of UUID. Keep in mind
that an image with an UUID tag cannot easily be referenced in another
Containerfile, so you must specify a tag for each base image.

Keep in mind that the Containerfile must contain the full pullspec, you
cannot omit the registry or tag there!

### `ContainerfileContent`

A string, holding the contents of the Containerfile specifying this image.
Providing a path to a Containerfile **will not work**.

### `ContextDirectory`

This is the **path** to a context directory used for the build of this image.
Keep in mind that the working directory for the integration tests is
`capo/pkg`.

To use the directory `capo/testdata/image_content`, you have to format it like
so: `../testdata/image_content`.

This directory is the default context directory for building
the test images and builder images. This means that if you
specify this line in your testing dockerfile:

```dockerfile
COPY ./README.md /
```

Then you also have to provide the `README.md` file in the context directory.

## Cleanup

The test images are cleaned up automatically after each run.
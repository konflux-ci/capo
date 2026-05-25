# About

Capo is a program and library to inspect the result of a
[buildah](https://github.com/containers/buildah) build and scan partial image
content for better contextualization of packages in OCI image SBOMs.

It is primarily developed to be used in the [Konflux CI](https://github.com/konflux-ci)
project, to deliver more accurate OCI image SBOMs to customers.

> [!WARNING]
> This project is a work in progress, and its API is unstable. Until version
> v1.0.0 is available, the API might change on minor version increase.


## Quickstart
To install, simply run:
```sh
go install github.com/konflux-ci/capo/cmd/capo@latest
```

After capo is installed, you can build your image using buildah and run capo.
```sh
buildah build -f Containerfile
capo --containerfile=Containerfile
```

When building an image with certain special options, these need to be passed to
capo as well. An example of these are the `--target` and `--build-arg` options:
```sh
buildah build -f Containerfile --target builder --build-arg KEY=VAL
capo --containerfile=Containerfile --target=builder --build-arg=KEY=VAL
```

For the full list of these options, consult the usage:
```sh
capo -h
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

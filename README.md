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
go install github.com/konflux-ci/capo@latest
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

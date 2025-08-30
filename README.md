# About

Capo is a program and library to inspect the result of a
[buildah](https://github.com/containers/buildah) build and scan partial image
content for better contextualization of OCI image SBOMs.

It is primarily developed to be used in the [Konflux CI](https://github.com/konflux-ci)
project, to deliver more accurate OCI image SBOMs to customers.

> [!WARNING]
> This project is a work in progress, and its API is unstable. Until version
> v1.0.0 is available, the API might change on minor version increase.

## Quickstart

TODO: after argument parsing is implemented, fill out this chapter

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

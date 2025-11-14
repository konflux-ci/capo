//go:build mage

package main

import (
	"strings"

	"github.com/magefile/mage/sh"
)

var Default = Build
var CapoPackage = "./cmd/capo"

// Runs the capo module in a buildah namespace using 'buildah unshare'.
// Passes args to capo. Capo arguments must be passed as a single string:
// $ mage run '--containerfile=Containerfile --build-arg=KEY=value'
func Run(capoArgsStr string) error {
	capoArgs := strings.Split(capoArgsStr, " ")
	bArgs := []string{
		"unshare",
		"go",
		"run",
		CapoPackage,
	}
	bArgs = append(bArgs, capoArgs...)
	return sh.RunV("buildah", bArgs...)
}

// Runs 'go mod download' and then installs the capo binary.
func Build() error {
	if err := sh.RunV("go", "mod", "download"); err != nil {
		return err
	}
	return sh.RunV("go", "install", "./...")
}

// Runs unit tests using Ginkgo.
func Test() error {
	return sh.RunV("go", "test", "-tags=unit,exclude_graphdriver_btrfs", "./...")
}

// Runs the dlv debugger in a buildah namespace using 'buildah unshare'.
func Debug() error {
	return sh.RunV("buildah", "unshare", "dlv", "debug", CapoPackage)
}

// Formats using 'go fmt .'
func Format() error {
	return sh.Run("go", "fmt", "./...")
}

// Runs 'go mod tidy' to remove unneeded dependencies.
func Tidy() error {
	return sh.Run("go", "mod", "tidy")
}

// Runs golangci-lint to perform static code analysis and linting.
// golangci-lint uses the config in .golangci.yaml
func Lint() error {
	return sh.RunV("golangci-lint", "run")
}

// Builds all test images and runs the integration test.
func IntegrationTest() error {
	return sh.RunV("buildah", "unshare", "go", "test", "-v", "-tags=integration", "./pkg")
}

func Release(version string) error {
	sh.RunV("git", "tag", version)
	sh.RunV("git", "push", "origin", version)
	return nil
}

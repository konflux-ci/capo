//go:build mage

package main

import (
	"github.com/magefile/mage/sh"
)

var Default = Build
var CapoPackage = "./cmd/capo"

// Runs the capo module in a buildah namespace using 'buildah unshare'.
func Run() error {
	return sh.RunV("buildah", "unshare", "go", "run", CapoPackage)
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
	return sh.RunV("ginkgo", "run", "-r", "-v")
}

// Runs the dlv debugger in a buildah namespace using 'buildah unshare'.
func Debug() error {
	return sh.RunV("buildah", "unshare", "dlv", "debug", CapoPackage)
}

// Formats using 'go fmt .'
func Format() error {
	return sh.Run("go", "fmt", "./...")
}

// Runs 'go vet'
func Vet() error {
	return sh.Run("go", "vet", "./...")
}

// Runs 'go mod tidy' to remove unneeded dependencies.
func Tidy() error {
	return sh.Run("go", "mod", "tidy")
}

// Runs checks on the code and creates the specified git tag.
//func Release(ctx context.Context, version string) error {
//	mg.Deps(Build, Vet, Tidy, Test)
//
//	// TODO: restrict to 'main' branch
//
//	sh.Run("git", "commit")
//}

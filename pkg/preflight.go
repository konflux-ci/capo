// Functions for checking if a containerfile contains any unsupported feature
// for builder content resolution.

package capo

import (
	"errors"
	"fmt"
	"strconv"

	"github.com/konflux-ci/capo/pkg/containerfile"
)

var ErrUnsupportedFeature = errors.New(
	"[ERR_UNSUPPORTED_FEATURES] some features of the containerfile are not supported for builder-content resolution",
)
var ErrBuilderIsFinalBase = errors.New("[ERR_BUILDER_FINAL_BASE] builder stage used as final base")
var ErrMountTypeBind = errors.New("[ERR_MOUNT_TYPE_BIND] RUN --mount with bind type in containerfile")

// ErrDuplicateAlias is returned when two stages in a Containerfile share
// the same alias. Buildah behavior for duplicate aliases is undefined
// (see https://github.com/containers/buildah/issues/6731), so capo skips
// builder content identification to avoid producing incorrect results.
var ErrDuplicateAlias = errors.New("[ERR_DUPLICATE_ALIAS] duplicate stage alias")

// Check containerfile for unsupported features for builder content resolution.
func preflightCheck(cf containerfile.Containerfile) error {
	joined := errors.Join(
		checkBuilderIsFinalBase(cf),
		checkDuplicateAlias(cf),
		checkRunMountTypeBind(cf),
	)
	if joined == nil {
		return nil
	}

	return fmt.Errorf("%w: %w", ErrUnsupportedFeature, joined)
}

// Check if any builder stage alias is used in the final stage as the base
// image.
func checkBuilderIsFinalBase(cf containerfile.Containerfile) error {
	if len(cf.Stages) < 2 {
		return nil
	}

	final := cf.Stages[len(cf.Stages)-1]

	for i, stage := range cf.BuilderStages() {
		if final.BaseRef == stage.Alias || final.BaseRef == strconv.Itoa(i) {
			return fmt.Errorf(
				"builder stage %q is used as base image of the final stage: %w", stage.Alias, ErrBuilderIsFinalBase,
			)
		}
	}

	return nil
}

// Check if the containerfile contains two stages with duplicate aliases.
func checkDuplicateAlias(cf containerfile.Containerfile) error {
	seenAliases := make(map[string]bool)
	for _, stage := range cf.Stages {
		if stage.Index != -1 && seenAliases[stage.Alias] {
			return fmt.Errorf(
				"stage alias %q is used more than once: %w",
				stage.Alias, ErrDuplicateAlias,
			)
		}
		seenAliases[stage.Alias] = true
	}

	return nil
}

// Check if the containerfile utilizes a RUN --mount with the bind type.
func checkRunMountTypeBind(cf containerfile.Containerfile) error {
	for _, st := range cf.Stages {
		for _, mount := range st.Mounts {
			if mount.MountType == containerfile.MountTypeBind && mount.FromRaw != "" {
				return ErrMountTypeBind
			}
		}
	}

	return nil
}

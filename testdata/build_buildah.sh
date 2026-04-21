#!/usr/bin/env bash
set -euo pipefail

# Includes https://github.com/containers/buildah/pull/6556
BUILDAH_COMMIT="${BUILDAH_COMMIT:-c9b944cbe5b325990e749352461d4af2245bc7e6}"
BUILDAH_REPO="https://github.com/containers/buildah.git"
BUILD_DIR="/tmp/capo-buildah-build"
OUTPUT_DIR="$(pwd)/testdata/bin"

mkdir -p "${OUTPUT_DIR}"

# Check if system buildah already supports --save-stages --stage-labels
if command -v buildah &>/dev/null; then
    if buildah build --help 2>&1 | grep -q -- "--save-stages"; then
        SYSTEM_VERSION=$(buildah --version | awk '{print $3}')
        echo "System buildah ${SYSTEM_VERSION} already supports --save-stages --stage-labels."
        echo "Infrastructure for downloading, building and execution of the custom buildah"
        echo "is not needed. Your system buildah is up to date, supporting saving and labeling"
        echo "intermediate images with --save-stages --stage-labels."
        echo "Once buildah >= 1.44.0 is widely available, this custom build infrastructure"
        echo "can be removed and buildah can be declared as a system dependency. See <ticket placeholder>."
        exit 0
    fi
fi

# Skip if binary already exists
if [[ -f "${OUTPUT_DIR}/buildah" ]]; then
    echo "Custom buildah already exists at ${OUTPUT_DIR}/buildah"
    exit 0
fi

echo "Building custom buildah from commit ${BUILDAH_COMMIT}..."

rm -rf "${BUILD_DIR}"
git clone "${BUILDAH_REPO}" "${BUILD_DIR}" >/dev/null 2>&1

cd "${BUILD_DIR}"

git fetch origin "${BUILDAH_COMMIT}" >/dev/null 2>&1 || true
git checkout "${BUILDAH_COMMIT}" >/dev/null 2>&1

make bin/buildah

cp bin/buildah "${OUTPUT_DIR}/buildah"
chmod +x "${OUTPUT_DIR}/buildah"

rm -rf "${BUILD_DIR}"

echo "Custom buildah built successfully: ${OUTPUT_DIR}/buildah"

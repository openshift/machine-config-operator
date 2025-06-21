#!/usr/bin/env bash

set -eu

REPO=github.com/openshift/machine-config-operator
WHAT=${WHAT:-machine-config-operator}
WHAT_PATH="${WHAT_PATH:-cmd/${WHAT}}"
GOTAGS="${GOTAGS:-} ${TAGS:-}"
GLDFLAGS=${GLDFLAGS:-}

eval $(go env | grep -e "GOHOSTOS" -e "GOHOSTARCH")
if [ -z "${GOHOSTOS:-}" ] || [ -z "${GOHOSTARCH:-}" ]; then
  go env
  echo 'Failed to find expected variables in `go env` output above' 1>&2
  exit 1
fi

: "${GOOS:=${GOHOSTOS}}"
: "${GOARCH:=${GOHOSTARCH}}"

# Go to the root of the repo
cdup="$(git rev-parse --show-cdup)" && test -n "$cdup" && cd "$cdup"

if [ -z ${VERSION_OVERRIDE+a} ]; then
	echo "Using version from git..."
	VERSION_OVERRIDE=$(git describe --abbrev=8 --dirty --always)
fi

HASH=${SOURCE_GIT_COMMIT:-$(git rev-parse --verify 'HEAD^{commit}')}
BUILD_DATE=$(date -u +'%Y-%m-%dT%H:%M:%SZ')

GLDFLAGS+="-X ${REPO}/pkg/version.Raw=${VERSION_OVERRIDE} -X ${REPO}/pkg/version.Hash=${HASH} -X ${REPO}/pkg/version.Date=${BUILD_DATE}"

if [ -z ${BIN_PATH+a} ]; then
	export BIN_PATH=_output/${GOOS}/${GOARCH}
fi

mkdir -p ${BIN_PATH}

if [[ $WHAT == "machine-config-controller" ]]; then
    GOTAGS="containers_image_openpgp exclude_graphdriver_devicemapper exclude_graphdriver_btrfs containers_image_ostree_stub"
fi

echo "Building ${REPO}/${WHAT_PATH} (${VERSION_OVERRIDE}, ${HASH}) for $GOOS/$GOARCH"
GOOS=${GOOS} GOARCH=${GOARCH} go build -mod=vendor -tags="${GOTAGS}" -ldflags "${GLDFLAGS} -s -w" -o ${BIN_PATH}/${WHAT} ${REPO}/${WHAT_PATH}

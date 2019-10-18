#!/usr/bin/env bash

set -eu

REPO=github.com/openshift/machine-config-operator
WHAT=${WHAT:-machine-config-operator}
GOFLAGS=${GOFLAGS:-}
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

GLDFLAGS+="-X ${REPO}/pkg/version.Raw=${VERSION_OVERRIDE} -X ${REPO}/pkg/version.Hash=${HASH}"

eval $(go env)

if [ -z ${BIN_PATH+a} ]; then
	export BIN_PATH=_output/${GOOS}/${GOARCH}
fi

mkdir -p ${BIN_PATH}

# Use the containers_image_openpgp flag to avoid the default CGO implementation of signatures dragged in by
# containers/image/signature, which we use only to edit the /etc/containers/policy.json file without doing any cryptography
CGO_ENABLED=0

if [[ $WHAT == "machine-config-controller" ]]; then
    GOFLAGS="containers_image_openpgp exclude_graphdriver_devicemapper exclude_graphdriver_btrfs containers_image_ostree_stub"
fi

echo "Building ${REPO}/cmd/${WHAT} (${VERSION_OVERRIDE}, ${HASH})"
CGO_ENABLED=${CGO_ENABLED} GOOS=${GOOS} GOARCH=${GOARCH} go build -tags="${GOFLAGS}" -ldflags "${GLDFLAGS} -s -w" -o ${BIN_PATH}/${WHAT} ${REPO}/cmd/${WHAT}

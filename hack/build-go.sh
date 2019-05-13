#!/usr/bin/env bash

set -eu

REPO=github.com/openshift/machine-config-operator
WHAT=${WHAT:-machine-config-operator}
GOFLAGS=${GOFLAGS:-}
GLDFLAGS=${GLDFLAGS:-}

eval $(go env | grep -e "GOHOSTOS" -e "GOHOSTARCH")

: "${GOOS:=${GOHOSTOS}}"
: "${GOARCH:=${GOHOSTARCH}}"

# Go to the root of the repo
cdup="$(git rev-parse --show-cdup)" && test -n "$cdup" && cd "$cdup"

if [ -z ${VERSION_OVERRIDE+a} ]; then
	echo "Using version from git..."
	VERSION_OVERRIDE=$(git describe --abbrev=8 --dirty --always)
fi

HASH=${OS_GIT_COMMIT:-$(git rev-parse --verify 'HEAD^{commit}')}

GLDFLAGS+="-X ${REPO}/pkg/version.Raw=${VERSION_OVERRIDE} -X ${REPO}/pkg/version.Hash=${HASH}"

eval $(go env)

if [ -z ${BIN_PATH+a} ]; then
	export BIN_PATH=_output/${GOOS}/${GOARCH}
fi

mkdir -p ${BIN_PATH}

CGO_ENABLED=0

echo "Building ${REPO}/cmd/${WHAT} (${VERSION_OVERRIDE}, ${HASH})"
CGO_ENABLED=${CGO_ENABLED} GOOS=${GOOS} GOARCH=${GOARCH} go build ${GOFLAGS} -ldflags "${GLDFLAGS} -s -w" -o ${BIN_PATH}/${WHAT} ${REPO}/cmd/${WHAT}

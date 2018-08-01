#!/usr/bin/env bash

set -eu

REPO=github.com/openshift/machine-config-operator
WHAT=${WHAT:-...}
GOFLAGS=${GOFLAGS:-}
GLDFLAGS=${GLDFLAGS:-}

eval $(go env | grep -e "GOHOSTOS" -e "GOHOSTARCH")

GOOS=${GOOS:-${GOHOSTOS}}
GOARCH=${GOACH:-${GOHOSTARCH}}

# Go to the root of the repo
cd "$(git rev-parse --show-cdup)"

if [ -z ${VERSION+a} ]; then
	echo "Using version from git..."
	VERSION=$(git describe --abbrev=8 --dirty --always)
fi

GLDFLAGS+="-X ${REPO}/version.Raw=${VERSION}"

if [ -z ${BIN_PATH+a} ]; then
	export BIN_PATH=_output/bin/${GOOS}/${GOARCH}
fi

mkdir -p ${BIN_PATH}

echo "Building ${WHAT}..."
GOOS=${GOOS} GOARCH=${GOARCH} go build ${GOFLAGS} -ldflags "${GLDFLAGS}" -o ${BIN_PATH}/${WHAT} ${REPO}/cmd/${WHAT}
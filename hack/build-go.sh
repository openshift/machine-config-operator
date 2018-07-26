#!/usr/bin/env bash

set -eu

REPO=github.com/openshift/machine-config-operator
WHAT=${WHAT:-}
GOFLAGS=${GOFLAGS:-}
GLDFLAGS=${GLDFLAGS:-}

# Go to the root of the repo
cd "$(git rev-parse --show-cdup)"

if [ -z ${VERSION+a} ]; then
	echo "Using version from git..."
	VERSION=$(git describe --abbrev=8 --dirty --always)
fi

GLDFLAGS+="-X ${REPO}/version.Raw=${VERSION}"

eval $(go env)

if [ -z ${BIN_PATH+a} ]; then
	export BIN_PATH=_output/bin/${GOOS}/${GOARCH}
fi

mkdir -p ${BIN_PATH}

echo "Building ${WHAT}..."
GOOS=${GOOS} GOARCH=${GOARCH} go build ${GOFLAGS} -ldflags "${GLDFLAGS}" -o ${WHAT} ${REPO}/cmd/${WHAT}
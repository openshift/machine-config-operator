#!/usr/bin/env bash

set -o errexit
set -o nounset
set -o pipefail

SCRIPT_ROOT=$(dirname ${BASH_SOURCE})/..

if [[ ! $(which go-bindata) ]]; then
  echo "golint not found on PATH. To install:"
  echo "go get -u github.com/jteeuwen/go-bindata/..."
  echo "Commit: a0ff2567cfb70903282db057e799fd826784d41d"
  exit 1
fi

set -x
go-bindata \
    -nocompress \
    -nometadata \
    -pkg "assets" \
    -prefix "${SCRIPT_ROOT}" \
    -o "${SCRIPT_ROOT}/pkg/operator/assets/bindata.go" \
    ${SCRIPT_ROOT}/manifests/...

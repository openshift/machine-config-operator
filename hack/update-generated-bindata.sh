#!/usr/bin/env bash

set -o errexit
set -o nounset
set -o pipefail

SCRIPT_ROOT=$(dirname ${BASH_SOURCE})/..

set -x
go-bindata \
    -nocompress \
    -nometadata \
    -pkg "assets" \
    -prefix "${SCRIPT_ROOT}" \
    -o "${SCRIPT_ROOT}/pkg/operator/assets/bindata.go" \
    ${SCRIPT_ROOT}/manifests/...

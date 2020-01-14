#!/usr/bin/env bash

set -o errexit
set -o nounset
set -o pipefail

SCRIPT_ROOT=$(dirname ${BASH_SOURCE})/..

bash ${SCRIPT_ROOT}/vendor/k8s.io/code-generator/generate-groups.sh all \
  github.com/openshift/machine-config-operator/pkg/generated github.com/openshift/machine-config-operator/pkg/apis \
  "machineconfiguration.openshift.io:v1" \
  --go-header-file ${SCRIPT_ROOT}/hack/custom-boilerplate.go.txt

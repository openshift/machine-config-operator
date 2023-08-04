#!/usr/bin/env bash

set -euo pipefail

# map names of CRD files between the vendored openshift/api repository and the ./install directory
CRDs=( "0000_80_containerruntimeconfig.crd.yaml:0000_80_machine-config-operator_01_containerruntimeconfig.crd.yaml"
               "0000_80_kubeletconfig.crd.yaml:0000_80_machine-config-operator_01_kubeletconfig.crd.yaml"
               "0000_80_machineconfig.crd.yaml:0000_80_machine-config-operator_01_machineconfig.crd.yaml"
               "0000_80_machineconfigpool.crd.yaml:0000_80_machine-config-operator_01_machineconfigpool.crd.yaml"
)

for crd in "${CRDs[@]}" ; do
    SRC="${crd%%:*}"
    DES="${crd##*:}"
    cp "vendor/github.com/openshift/api/machineconfiguration/v1/$SRC" "install/$DES"
done
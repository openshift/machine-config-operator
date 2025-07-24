#!/usr/bin/env bash

set -euo pipefail

# map names of CRD files between the vendored openshift/api repository and the ./install directory
CRDS_MAPPING=( "v1/0000_80_containerruntimeconfig.crd.yaml:0000_80_machine-config-operator_01_containerruntimeconfig.crd.yaml"
               "v1/0000_80_kubeletconfig.crd.yaml:0000_80_machine-config-operator_01_kubeletconfig.crd.yaml"
               "v1/0000_80_machineconfig.crd.yaml:0000_80_machine-config-operator_01_machineconfig.crd.yaml"
               "v1/0000_80_machineconfigpool.crd.yaml:0000_80_machine-config-operator_01_machineconfigpool.crd.yaml"
               "v1alpha1/0000_80_machineconfignode-TechPreviewNoUpgrade.crd.yaml:0000_80_machine-config-operator_01_machineconfignode-TechPreviewNoUpgrade.crd.yaml" ) 
                #TODO(jkyros): 0000_80_machine-config-operator_02_containerruntimeconfig.crd.yaml)

for crd in "${CRDS_MAPPING[@]}" ; do
    SRC="${crd%%:*}"
    DES="${crd##*:}"
    cp "vendor/github.com/openshift/api/machineconfiguration/$SRC" "install/$DES"
done

#this one goes in manifests rather than install, but should it? 
cp "vendor/github.com/openshift/api/machineconfiguration/v1/0000_80_controllerconfig.crd.yaml" "manifests/controllerconfig.crd.yaml"
cp "vendor/github.com/openshift/api/operator/v1/0000_80_machine-config-operator_01_config.crd.yaml" "install/0000_80_machine-config-operator_01_config.crd.yaml"

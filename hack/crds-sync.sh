#!/usr/bin/env bash

set -euo pipefail

# map names of CRD files between the vendored openshift/api repository and the ./install directory
CRDS_MAPPING=(
    "v1/zz_generated.crd-manifests/0000_80_machine-config_01_containerruntimeconfigs.crd.yaml:0000_80_machine-config_01_containerruntimeconfigs.crd.yaml"
    "v1/zz_generated.crd-manifests/0000_80_machine-config_01_kubeletconfigs.crd.yaml:0000_80_machine-config_01_kubeletconfig.crd.yaml"
    "v1/zz_generated.crd-manifests/0000_80_machine-config_01_machineconfigs.crd.yaml:0000_80_machine-config_01_machineconfig.crd.yaml"
    "v1/zz_generated.crd-manifests/0000_80_machine-config_01_machineconfigpools-Default.crd.yaml:0000_80_machine-config_01_machineconfigpool-Default.crd.yaml"
    "v1/zz_generated.crd-manifests/0000_80_machine-config_01_machineconfigpools-TechPreviewNoUpgrade.crd.yaml:0000_80_machine-config_01_machineconfigpool-TechPreviewNoUpgrade.crd.yaml"
    "v1alpha1/zz_generated.crd-manifests/0000_80_machine-config_01_pinnedimagesets-TechPreviewNoUpgrade.crd.yaml:0000_80_machine-config_01_pinnedimagesets-TechPreviewNoUpgrade.crd.yaml"
    "v1alpha1/zz_generated.crd-manifests/0000_80_machine-config_01_machineconfignodes-TechPreviewNoUpgrade.crd.yaml:0000_80_machine-config_01_machineconfignode-TechPreviewNoUpgrade.crd.yaml")
    #TODO(jkyros): 0000_80_machine-config_01_machine-config-operator_02_containerruntimeconfig.crd.yaml)

for crd in "${CRDS_MAPPING[@]}" ; do
    SRC="${crd%%:*}"
    DES="${crd##*:}"
    cp "vendor/github.com/openshift/api/machineconfiguration/$SRC" "install/$DES"
done

#this one goes in manifests rather than install, but should it? 
cp "vendor/github.com/openshift/api/machineconfiguration/v1/zz_generated.crd-manifests/0000_80_machine-config_01_controllerconfigs-Default.crd.yaml" "manifests/controllerconfig.crd.yaml"
cp "vendor/github.com/openshift/api/machineconfiguration/v1alpha1/zz_generated.crd-manifests/0000_80_machine-config_01_machineconfignodes-TechPreviewNoUpgrade.crd.yaml" "manifests/0000_80_machine-config_01_machineconfignode-TechPreviewNoUpgrade.crd.yaml"
cp "vendor/github.com/openshift/api/machineconfiguration/v1alpha1/zz_generated.crd-manifests/0000_80_machine-config_01_pinnedimagesets-TechPreviewNoUpgrade.crd.yaml" "manifests/0000_80_machine-config_01_pinnedimagesets-TechPreviewNoUpgrade.crd.yaml"
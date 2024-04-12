#!/usr/bin/env bash

set -euo pipefail

# This is the MCO's API directory in openshift/api, every CRD living here can be directly be copied over.
cp vendor/github.com/openshift/api/machineconfiguration/v1/zz_generated.crd-manifests/*.crd.yaml install/.
cp vendor/github.com/openshift/api/machineconfiguration/v1alpha1/zz_generated.crd-manifests/*.crd.yaml install/.

#  These are MCO CRDs that live in other parts of the openshift/api, so the copies need to be more specific
CRDS_MAPPING=( 
   "operator/v1/zz_generated.crd-manifests/0000_80_machine-config_01_machineconfigurations-Default.crd.yaml:0000_80_machine-config_01_machineconfigurations-Default.crd.yaml"
   "operator/v1/zz_generated.crd-manifests/0000_80_machine-config_01_machineconfigurations-TechPreviewNoUpgrade.crd.yaml:0000_80_machine-config_01_machineconfigurations-TechPreviewNoUpgrade.crd.yaml"
   "config/v1alpha1/zz_generated.crd-manifests/0000_10_config-operator_01_clusterimagepolicies-TechPreviewNoUpgrade.crd.yaml:0000_10_config-operator_01_clusterimagepolicies-TechPreviewNoUpgrade.crd.yaml"
)

for crd in "${CRDS_MAPPING[@]}" ; do
    SRC="${crd%%:*}"
    DES="${crd##*:}"
    cp "vendor/github.com/openshift/api/$SRC" "install/$DES"
done
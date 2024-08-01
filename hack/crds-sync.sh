#!/usr/bin/env bash

set -euo pipefail

# This is the MCO's API directory in openshift/api, every CRD living here can be directly be copied over.
cp vendor/github.com/openshift/api/machineconfiguration/v1/zz_generated.crd-manifests/*.crd.yaml install/.
cp vendor/github.com/openshift/api/machineconfiguration/v1alpha1/zz_generated.crd-manifests/*.crd.yaml install/.

#  These are MCO CRDs that live in other parts of the openshift/api, so the copies need to be more specific
cp vendor/github.com/openshift/api/operator/v1/zz_generated.crd-manifests/0000_80_machine-config_01_machineconfigurations*.crd.yaml install/.
cp vendor/github.com/openshift/api/config/v1alpha1/zz_generated.crd-manifests/0000_10_config-operator_01_clusterimagepolicies*.crd.yaml install/.

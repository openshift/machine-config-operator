#!/usr/bin/env bash
#
# This script is not meant to be directly executed. Instead, it is embedded
# within the Build Controller binary (see //go:embed) and injected into a
# custom build pod.

set -xeuo

# Injects the contents of the digestfile into a ConfigMap using the
# machine-os-builder binary. This is done to avoid needing an oc or kubectl
# binary.

machine-os-builder \
    create-digest-configmap \
    --configmap-name "${DIGEST_CONFIGMAP_NAME}" \
    --digestfile /tmp/done/digestfile \
    --labels "${DIGEST_CONFIGMAP_LABELS}"

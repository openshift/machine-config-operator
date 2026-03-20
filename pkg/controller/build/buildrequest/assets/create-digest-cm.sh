#!/usr/bin/env bash
#
# This script is not meant to be directly executed. Instead, it is embedded
# within the Build Controller binary (see //go:embed) and injected into a
# custom build pod.

set -xeuo

# Inject the contents of the digestfile into a ConfigMap.

# Create and label the digestfile ConfigMap
if ! oc create configmap \
    "$DIGEST_CONFIGMAP_NAME" \
    --namespace openshift-machine-config-operator \
    --from-file=digest=/tmp/done/digestfile \
    --dry-run=client -o yaml | \
    oc label --local -f - $DIGEST_CONFIGMAP_LABELS -o yaml | \
    oc apply -f -; then
    echo "Failed to create and label ConfigMap $DIGEST_CONFIGMAP_NAME"
    exit 1
fi

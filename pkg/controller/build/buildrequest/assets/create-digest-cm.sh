#!/usr/bin/env bash
#
# This script is not meant to be directly executed. Instead, it is embedded
# within the Build Controller binary (see //go:embed) and injected into a
# custom build pod.

set -xeuo

# Check if the oc command is available.
if command -v "oc" &> /dev/null; then
    echo "Using built-in oc command"
else
    # If the oc command is not available, check under /tmp/done/oc.
    if [[ -x /tmp/done/oc ]]; then
        # Add this to our PATH if found.
        export PATH="$PATH:/tmp/done"
    else
        # If it cannot be found, return a non-zero exit code.
        echo "oc command not found"
        exit 1
    fi
fi

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

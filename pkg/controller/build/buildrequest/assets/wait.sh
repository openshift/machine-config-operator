#!/usr/bin/env bash
#
# This script is not meant to be directly executed. Instead, it is embedded
# within the Build Controller binary (see //go:embed) and injected into a
# custom build pod.

# Wait until the digestfile file appears. The presence of this file indicates
# that the build operation is complete.
set -euo

# Wait until the done file appears.
while [ ! -f "/tmp/done/digestfile" ]
do
	sleep 1
done

# Inject the contents of the digestfile into a ConfigMap.
set -x

# Create the digestfile ConfigMap
oc create configmap \
	"$DIGEST_CONFIGMAP_NAME" \
	--namespace openshift-machine-config-operator \
	--from-file=digest=/tmp/done/digestfile

# Label the digestfile ConfigMap
# shellcheck disable=SC2086
oc label configmap \
	"$DIGEST_CONFIGMAP_NAME" \
	--namespace openshift-machine-config-operator \
	--overwrite=true \
	$DIGEST_CONFIGMAP_LABELS

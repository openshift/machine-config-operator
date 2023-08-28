#!/usr/bin/env bash
#
# This script is not meant to be directly executed. Instead, it is embedded
# within the Build Controller binary (see //go:embed) and injected into a
# custom build pod.

# Wait until the digestfile ConfigMap file appears.
while [ ! -f "$DIGESTFILE_CONFIGMAP_PATH" ]
do
	sleep 1
done

oc apply -f "$DIGESTFILE_CONFIGMAP_PATH"

#!/usr/bin/env bash
#
# This script is not meant to be directly executed. Instead, it is embedded
# within the Build Controller binary (see //go:embed) and injected into a
# custom build pod.

# Wait until the done file appears.
while [ ! -f "/tmp/done/digestfile" ]
do
	sleep 1
done

oc create configmap \
	"$DIGEST_CONFIGMAP_NAME" \
	--namespace openshift-machine-config-operator \
	--from-file=digest=/tmp/done/digestfile

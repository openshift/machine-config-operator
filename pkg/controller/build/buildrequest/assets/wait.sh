#!/usr/bin/env bash
#
# This script is not meant to be directly executed. Instead, it is embedded
# within the Build Controller binary (see //go:embed) and injected into a
# custom build pod.

# Wait until the digestfile file appears. The presence of this file indicates
# that the build operation is complete.
set -euo

# Wait for either the digestfile or an errorfile
while true; do
  if [ -f "/tmp/done/digestfile" ]; then
    # If digestfile is found, break the loop and proceed
    echo "Digest file found. Proceeding with ConfigMap creation."
    break
  elif [ -f "/tmp/done/errorfile" ]; then
    # If errorfile is found, exit the script with an error
    echo "Error file found. Exiting."
    # Delete the error file so that when a build pod is scheduled again
    # on the same node, it doesn't error out immediately
    # /tmp should be automatically cleared up on reboot but let's manually clean up as well
    rm /tmp/done/errorfile
    exit 1
  fi
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

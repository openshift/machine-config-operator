#!/usr/bin/env bash
#
# This script is not meant to be directly executed. Instead, it is embedded
# within the Build Controller binary (see //go:embed) and injected into a
# custom build pod.

# Wait until the done file appears.
while [ ! -f "/tmp/done/done" ]
do
	sleep 1
done

# Inspect the image to get the digest from the registry. This produces JSON
# output which we then scrape the pod logs for. This is why we're not using set
# -x for this script.
skopeo inspect \
	--authfile "$FINAL_IMAGE_PUSH_CREDS" \
	--cert-dir /var/run/secrets/kubernetes.io/serviceaccount \
	"docker://$TAG"

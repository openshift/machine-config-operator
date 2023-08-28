#!/usr/bin/env bash
#
# This script is not meant to be directly executed. Instead, it is embedded
# within the Build Controller binary (see //go:embed) and injected into a
# custom build pod.
set -xeuo

build_context="$HOME/context"

# Create a directory to hold our build context.
mkdir -p "$build_context/machineconfig"

# Copy the Dockerfile and Machineconfigs from configmaps into our build context.
cp /tmp/dockerfile/Dockerfile "$build_context"
cp /tmp/machineconfig/machineconfig.json.gz "$build_context/machineconfig/"

digestfile_path="/tmp/done/digestfile"

# Build our image using Buildah.
buildah bud \
	--storage-driver vfs \
	--authfile="$BASE_IMAGE_PULL_CREDS" \
	--tag "$TAG" \
	--file="$build_context/Dockerfile" "$build_context"

# Push our built image.
buildah push \
	--storage-driver vfs \
	--authfile="$FINAL_IMAGE_PUSH_CREDS" \
	--digestfile="$digestfile_path" \
	--cert-dir /var/run/secrets/kubernetes.io/serviceaccount "$TAG"

# Write the ConfigMap spec to the shared emptyDir path. This will trigger the
# wait-for-done container to apply the ConfigMap. We create the ConfigMap this
# way so that the Build Pod (the pod that this script is running in) will be
# the owner of the ConfigMap. This simplifies the garbage collection process
# since the ConfigMap will be deleted when the build pod is. This cannot be
# templatized with Go templates because the UID of the pod is required and that
# isn't known until runtime.
#
# Also, the YAML below is indented using spaces. Using tabs breaks YAML parsing
# in this particular situation.
cat > "$DIGESTFILE_CONFIGMAP_PATH" <<-EOF
---
apiVersion: v1
data:
  digest: "$(cat "$digestfile_path")"
kind: ConfigMap
metadata:
  name: "$DIGESTFILE_CONFIGMAP_NAME"
  namespace: "openshift-machine-config-operator"
  ownerReferences:
  - apiVersion: v1
    blockOwnerDeletion: true
    controller: true
    kind: Pod
    name: "$BUILD_POD_NAME"
    uid: "$BUILD_POD_UID"
EOF

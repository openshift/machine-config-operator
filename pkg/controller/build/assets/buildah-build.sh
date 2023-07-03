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
	--digestfile="/tmp/done/digestfile" \
	--cert-dir /var/run/secrets/kubernetes.io/serviceaccount "$TAG"

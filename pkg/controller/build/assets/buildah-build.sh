#!/usr/bin/env bash
#
# This script is not meant to be directly executed. Instead, it is embedded
# within the Build Controller binary (see //go:embed) and injected into a
# custom build pod.
set -xeuo

ETC_PKI_ENTITLEMENT_MOUNTPOINT="${ETC_PKI_ENTITLEMENT_MOUNTPOINT:-}"
ETC_PKI_RPM_GPG_MOUNTPOINT="${ETC_PKI_RPM_GPG_MOUNTPOINT:-}"
ETC_YUM_REPOS_D_MOUNTPOINT="${ETC_YUM_REPOS_D_MOUNTPOINT:-}"

build_context="$HOME/context"

# Create a directory to hold our build context.
mkdir -p "$build_context/machineconfig"

# Copy the Dockerfile and Machineconfigs from configmaps into our build context.
cp /tmp/dockerfile/Dockerfile "$build_context"
cp /tmp/machineconfig/machineconfig.json.gz "$build_context/machineconfig/"

build_args=(
	--log-level=DEBUG
	--storage-driver vfs
	--authfile="$BASE_IMAGE_PULL_CREDS"
	--tag "$TAG"
	--file="$build_context/Dockerfile"
)

mount_opts="z,rw"

# If we have RHSM certs, copy them into a tempdir to avoid SELinux issues, and
# tell Buildah about them.
rhsm_path="/var/run/secrets/rhsm"
if [[ -d "$rhsm_path" ]]; then
	rhsm_certs="$(mktemp -d)"
	cp -r -v "$rhsm_path/." "$rhsm_certs"
	chmod -R 0755 "$rhsm_certs"
	build_args+=("--volume=$rhsm_certs:/run/secrets/rhsm:$mount_opts")
fi

# If we have /etc/pki/entitlement certificates, commonly used with RHEL
# entitlements, copy them into a tempdir to avoid SELinux issues, and tell
# Buildah about them.
if [[ -n "$ETC_PKI_ENTITLEMENT_MOUNTPOINT" ]] && [[ -d "$ETC_PKI_ENTITLEMENT_MOUNTPOINT" ]]; then
	configs="$(mktemp -d)"
	cp -r -v "$ETC_PKI_ENTITLEMENT_MOUNTPOINT/." "$configs"
	chmod -R 0755 "$configs"
	build_args+=("--volume=$configs:$ETC_PKI_ENTITLEMENT_MOUNTPOINT:$mount_opts")
fi

# If we have /etc/yum.repos.d configs, commonly used with Red Hat Satellite
# subscriptions, copy them into a tempdir to avoid SELinux issues, and tell
# Buildah about them.
if [[ -n "$ETC_YUM_REPOS_D_MOUNTPOINT" ]] && [[ -d "$ETC_YUM_REPOS_D_MOUNTPOINT" ]]; then
	configs="$(mktemp -d)"
	cp -r -v "$ETC_YUM_REPOS_D_MOUNTPOINT/." "$configs"
	chmod -R 0755 "$configs"
	build_args+=("--volume=$configs:$ETC_YUM_REPOS_D_MOUNTPOINT:$mount_opts")
fi

# If we have /etc/pki/rpm-gpg configs, commonly used with Red Hat Satellite
# subscriptions, copy them into a tempdir to avoid SELinux issues, and tell
# Buildah about them.
if [[ -n "$ETC_PKI_RPM_GPG_MOUNTPOINT" ]] && [[ -d "$ETC_PKI_RPM_GPG_MOUNTPOINT" ]]; then
	configs="$(mktemp -d)"
	cp -r -v "$ETC_PKI_RPM_GPG_MOUNTPOINT/." "$configs"
	chmod -R 0755 "$configs"
	build_args+=("--volume=$configs:$ETC_PKI_RPM_GPG_MOUNTPOINT:$mount_opts")
fi

# Build our image.
buildah bud "${build_args[@]}" "$build_context"

# Push our built image.
buildah push \
	--storage-driver vfs \
	--authfile="$FINAL_IMAGE_PUSH_CREDS" \
	--digestfile="/tmp/done/digestfile" \
	--cert-dir /var/run/secrets/kubernetes.io/serviceaccount "$TAG"

#!/usr/bin/env bash
#
# This script is not meant to be directly executed. Instead, it is embedded
# within the Build Controller binary (see //go:embed) and injected into a
# custom build pod.
set -xeuo

DEST=/etc/pki/ca-trust/extracted

# Prevent p11-kit from reading user configuration files.
export P11_KIT_NO_USER_CONFIG=1

# OpenSSL PEM bundle that includes trust flags
/usr/bin/p11-kit extract --format=openssl-bundle --filter=certificates --overwrite --comment $DEST/openssl/ca-bundle.trust.crt
/usr/bin/p11-kit extract --format=pem-bundle --filter=ca-anchors --overwrite --comment --purpose server-auth $DEST/pem/tls-ca-bundle.pem

su -m build << 'EOF'
set -xeuo

build_context="$HOME/context"

# Create a directory to hold our build context.
mkdir -p "$build_context/machineconfig"

ETC_PKI_ENTITLEMENT_MOUNTPOINT="${ETC_PKI_ENTITLEMENT_MOUNTPOINT:-}"
ETC_PKI_RPM_GPG_MOUNTPOINT="${ETC_PKI_RPM_GPG_MOUNTPOINT:-}"
ETC_YUM_REPOS_D_MOUNTPOINT="${ETC_YUM_REPOS_D_MOUNTPOINT:-}"
MAX_RETRIES="${MAX_RETRIES:-3}"

export HTTP_PROXY="${HTTP_PROXY:-}"
export HTTPS_PROXY="${HTTPS_PROXY:-}"
export NO_PROXY="${NO_PROXY:-}"

# Copy the Containerfile, Machineconfigs and Additional Trust Bundle from configmaps into our build context.
cp /tmp/containerfile/Containerfile "$build_context"
cp /tmp/machineconfig/machineconfig.json.gz "$build_context/machineconfig/"
cp /etc/pki/ca-trust/source/anchors/openshift-config-user-ca-bundle.crt "$build_context"

build_args=(
	--log-level=DEBUG
	--storage-driver vfs
	--authfile="$BASE_IMAGE_PULL_CREDS"
	--tag "$TAG"
	--file="$build_context/Containerfile"
	--build-arg HTTP_PROXY="$HTTP_PROXY"
	--build-arg HTTPS_PROXY="$HTTPS_PROXY"
	--build-arg NO_PROXY="$NO_PROXY"
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
EOF

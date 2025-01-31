#!/usr/bin/env bash
set -xeuo

DEST=/etc/pki/ca-trust/extracted

echo $(params.additionalTrustBundle) > /etc/pki/ca-trust/source/anchors/openshift-config-user-ca-bundle.crt

# Prevent p11-kit from reading user configuration files.
export P11_KIT_NO_USER_CONFIG=1

# OpenSSL PEM bundle that includes trust flags
/usr/bin/p11-kit extract --format=openssl-bundle --filter=certificates --overwrite --comment $DEST/openssl/ca-bundle.trust.crt
/usr/bin/p11-kit extract --format=pem-bundle --filter=ca-anchors --overwrite --comment --purpose server-auth $DEST/pem/tls-ca-bundle.pem

build_context=$(params.buildContext)

# Create a directory to hold our build context.
mkdir -p "$build_context/machineconfig"

# Copy the Containerfile, Machineconfigs and Additional Trust Bundle from configmaps into our build context.
echo $(params.containerFile) > "$build_context/Containerfile"
echo $(params.machineConfig) > "$build_context/machineconfig/machineconfig.json.gz"
cp /etc/pki/ca-trust/source/anchors/openshift-config-user-ca-bundle.crt "$build_context"

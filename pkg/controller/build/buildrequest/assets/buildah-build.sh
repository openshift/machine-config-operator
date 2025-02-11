#!/usr/bin/env bash

# This script is not meant to be directly executed. Instead, it is embedded
# within the Build Controller binary (see //go:embed) and injected into a
# custom build pod.
set -xeuo

DEST=/etc/pki/ca-trust/extracted

# Prevent p11-kit from reading user configuration files.
export P11_KIT_NO_USER_CONFIG=1

# OpenSSL PEM bundle that includes trust flags
/usr/bin/p11-kit extract --format=openssl-bundle --filter=certificates --overwrite --comment "$DEST/openssl/ca-bundle.trust.crt"
/usr/bin/p11-kit extract --format=pem-bundle --filter=ca-anchors --overwrite --comment --purpose server-auth "$DEST/pem/tls-ca-bundle.pem"

su -m build "/tmp/containerfile/inner-build-script.sh"

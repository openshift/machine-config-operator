#!/usr/bin/env bash

# This script will rename the OS images within the install (/manifests)
# directory in response to the TAGS env var being set to either fcos or scos.
# If TAGS is empty or set to any other value, this script will no-op.

set -euo

target_dir="${1:-"/manifests"}"
TAGS="${TAGS:-}"

imagerefs="$target_dir/image-references"
osimageurls="$target_dir/0000_80_machine-config_05_osimageurl.yaml"

if [ "${TAGS}" = "fcos" ]; then
    # Remove non-base/extensions image-references entirely for fcos. This regex
    # will match rhel-coreos-extensions, rhel-coreos-10, and
    # rhel-coreos-10-extensions. rhel-coreos is left alone since we need to
    # replace it instead of removing it.
    sed -i "/- name: rhel-coreos-.*$/,+3d" "$imagerefs"

    # Remove extensions from the osimageurl configmap (if we don't, oc won't
    # rewrite it, and the placeholder value will survive and get used)
    sed -i "/baseOSExtensionsContainerImage:/d" "$osimageurls"

    # Rename rhel-coreos to fedora-coreos for fcos
    sed -i 's/rhel-coreos/fedora-coreos/g' "$imagerefs" "$osimageurls"
elif [ "${TAGS}" = "scos" ]; then
    # Remove RHEL10 entirely for scos
    sed -i "/- name: rhel-coreos-10.*$/,+3d" "$imagerefs"

    # Rename rhel-coreos to stream-coreos for scos
    sed -i 's/rhel-coreos/stream-coreos/g' "$imagerefs" "$osimageurls"
else
    echo "'fcos' or 'scos' tags not set, skipping OS image renames"
fi

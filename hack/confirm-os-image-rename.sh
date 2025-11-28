#!/usr/bin/env bash

# This script acts as a guard for OS image renames in the install (/manifects)
# directory. Based upon the given tags, it searches the files within that
# directory in order to determine whether the correct OS image names are
# present or not. The script will return a non-zero exit code whenever an
# incorrect name is found or an expected name is not found.

target_dir="$1"

should_exist=()
should_not_exist=()

if [ "$TAGS" = "fcos" ]; then
    should_not_exist=('rhel-coreos' 'rhel-coreos-extensions' 'rhel-coreos-10' 'rhel-coreos-10-extensions' 'baseOSExtensionsContainerImage')
    should_exist+=('fedora-coreos')
fi

if [ "$TAGS" = "scos" ]; then
    should_not_exist=('rhel-coreos' 'rhel-coreos-extensions' 'rhel-coreos-10' 'rhel-coreos-10-extensions' 'fedora-coreos')
    should_exist=('stream-coreos' 'stream-coreos-extensions')
fi

if [ "$TAGS" != "scos" ] && [ "$TAGS" != "fcos" ]; then
    should_exist=('rhel-coreos' 'rhel-coreos-extensions' 'rhel-coreos-10' 'rhel-coreos-10-extensions')
    should_not_exist=('stream-coreos' 'stream-coreos-extensions' 'fedora-coreos')
fi

for item in "${should_not_exist[@]}"; do
    output="$(grep -F -r "$item" "$target_dir")"
    retval="$?"
    if [ $retval -eq 0 ]; then
        echo "Expected not to find OS image name '$item' in $target_dir:"
        echo "$output"
        exit "$retval"
    fi
done

for item in "${should_exist[@]}"; do
    output="$(grep -F -r "$item" "$target_dir")"
    retval="$?"
    if [ $retval -ne 0 ]; then
        echo "Expected to find OS image name '$item' in $target_dir"
        exit "$retval"
    fi
done

echo "All OS image names are correct"

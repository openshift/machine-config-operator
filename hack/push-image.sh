#!/usr/bin/env bash

set -eu

# Print errors to stderr
function print_error {
	echo "ERROR: $1" >&2
}

function print_info {
	echo "INFO: $1" >&2
}

# Warn when unprivileged
if [ `id --user` -ne 0 ]; then
	print_error "Note: Running unprivileged may fail due to permissions"
fi

# Record all images files
ALL_IMAGES=Dockerfile.*

# To push will hold a list of all image images to push
TOPUSH=""

REPO=${REPO:-"openshift"}

# WHAT should be defined. If not, give a list and exit
if [ -z ${WHAT+a} ]; then
	print_error "WHAT must be set to one of the following:"
	print_error "- all"
	for x in $ALL_IMAGES ; do
		print_error "- ${x#Dockerfile.}"
	done

	exit 1
fi


# If all is the WHAT target then set TOPUSH to all the images found
if [ ${WHAT} == "all" ]; then
	TOPUSH=$ALL_IMAGES
	print_info "Pushing all images"
else
	# Otherwise WHAT should be valid at this point
	TOPUSH="${WHAT}"
fi


if [ -z ${VERSION+a} ]; then
        print_info "Using version from git..."
        VERSION=$(git describe --abbrev=8 --dirty --always)
fi

# Push all images requested
for IMAGE_TO_PUSH in $TOPUSH; do
	NAME="${IMAGE_TO_PUSH#Dockerfile.}"
	set -x
	podman push "localhost/${NAME}:latest" "${REPO}/origin-${NAME}:latest"
	set +x
done
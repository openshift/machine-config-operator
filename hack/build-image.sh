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
	print_error "Note: Building unprivileged may fail due to permissions"
fi

# Record all images files
ALL_IMAGES=Dockerfile.*

# To build will hold a list of all image files to build
TOBUILD=""

# WHAT should be defined. If not, give a list and exit
if [ -z ${WHAT+a} ]; then
	print_error "WHAT must be set to one of the following:"
	print_error "- all"
	for x in $ALL_IMAGES ; do
		print_error "- ${x#Dockerfile.}"
	done

	exit 1
fi


# If all is the WHAT target then set TOBUILD to all the images found
if [ ${WHAT} == "all" ]; then
	TOBUILD=$ALL_IMAGES
	print_info "Building all images"
else
	# Otherwise WHAT should be valid at this point
	TOBUILD="Dockerfile.${WHAT}"
fi

# Check that the target is valid
for IMAGE_TO_BUILD in $TOBUILD; do
  if ! test -f "${IMAGE_TO_BUILD}"
  then
    print_error "no build file named '${IMAGE_TO_BUILD}'"
    print_error "WHAT may be invalid: '${WHAT}'"
    exit 1
  fi
done


if [ -z ${VERSION+a} ]; then
        print_info "Using version from git..."
        VERSION=$(git describe --abbrev=8 --dirty --always)
fi

# Build all images requested
for IMAGE_TO_BUILD in $TOBUILD; do
	NAME="${IMAGE_TO_BUILD#Dockerfile.}"
	set -x
	podman build -t "${NAME}:${VERSION}" -f "${IMAGE_TO_BUILD}" --no-cache
done
#!/usr/bin/env bash

set -eu

podman=${podman:-podman}

# Print errors to stderr
function print_error {
	echo "ERROR: $1" >&2
}

function print_info {
	echo "INFO: $1" >&2
}

# Warn when unprivileged
if [ `id --user` -ne 0 ]; then
	print_info "Building unprivileged may fail due to permissions"
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

# Our change detection uses the git sha1
# if ! git diff --quiet --exit-code; then
#     print_error "Outstanding uncommitted changes found (git diff)"
#     exit 1
# fi

VERSION=$(git describe --tags --abbrev=12 --always)

# If all is the WHAT target then set TOBUILD to all the images found
if [ ${WHAT} == "all" ]; then
	TOBUILD=$ALL_IMAGES
	print_info "Building all images"
else
	# Otherwise WHAT should be valid at this point
	TOBUILD="Dockerfile.${WHAT}"
  # See Dockerfile.machine-config-daemon.upstream for why this exists
  if [ -f "$TOBUILD.upstream" ]; then
      TOBUILD="${TOBUILD}.upstream"
  fi
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

# Build all images requested
for IMAGE_TO_BUILD in $TOBUILD; do
	NAME="${IMAGE_TO_BUILD#Dockerfile.}"
  NAME="${NAME//.upstream}"
	set -x
  imgname="localhost/${NAME}:${VERSION}"
  if $podman inspect ${imgname} &>/dev/null; then
      echo "Already built ${imgname}"
  else
	    $podman build -t "${imgname}" -f "${IMAGE_TO_BUILD}" --no-cache
  fi
done

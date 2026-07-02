#!/usr/bin/env bash

set -xeuo

# This is an installation script for skopeo. skopeo is needed by the
# e2e-gcp-op-ocl test suite since it pushes and pulls images around.
#
# Using a build-root image for this is the preferred approach (see:
# https://docs.ci.openshift.org/docs/architecture/ci-operator/#build-root-image)
# since skopeo can be installed via dnf at build-time. However, that process is
# a bit more involved. While it would ultimately pay off, it is not in-scope
# with the bug that is being resolved.
#
# Because of limitations within the builder container image and the context it
# runs in, certain adaptations must be made:
# - The builder image does not run as a privileged user and is denied privilege
# escalation. This means that running dnf install -y skopeo cannot be done.
# - The builder image does not have jq installed. This means we need to use
# Python for any JSON parsing we need to perform instead.
# - Because this runs as a non-privileged user, we cannot run the make install
# step for skopeo. Instead, we need to append /tmp/skopeo/bin to the PATH. This
# is done in the Makefile and only for the go test invocation.

OPENSHIFT_CI="${OPENSHIFT_CI:-""}"

install_skopeo() {
  # If we've already built skopeo once, check if it works and then return.
  if [ -f /tmp/skopeo/bin/skopeo ]; then
    echo "Prebuilt skopeo found at /tmp/skopeo/bin/skopeo, skipping installation"
    /tmp/skopeo/bin/skopeo --version
    return 0
  fi

  # Pin to a specific version compatible with Go 1.23.x
  # v1.20.0 is the last release before Go 1.24 requirement
  skopeo_version="v1.20.0"

  echo "Installing skopeo $skopeo_version from source"

  skopeo_clone_dir="/tmp/skopeo"

  mkdir -p "$skopeo_clone_dir"

  # Shallow-clone the skopeo repo to the local repo dir.
  git clone --branch "$skopeo_version" --depth 1 https://github.com/containers/skopeo.git "$skopeo_clone_dir"

  cd "$skopeo_clone_dir"

  # Build skopeo
  make bin/skopeo
}

# Check if we have skopeo installed first.
if command -v skopeo >/dev/null 2>&1  ; then
  # If we do, just output its version.
  skopeo --version
else
  # Check if we're running in CI.
  if [ "$OPENSHIFT_CI" == "true" ]; then
    # Only when we're in CI should we install skopeo.
    install_skopeo
  else
    # Otherwise, we should exit with a clear error.
    echo "Missing required binary 'skopeo'"
    exit 1
  fi
fi

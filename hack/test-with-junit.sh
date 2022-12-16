#!/usr/bin/env bash

set -euo pipefail

# This script exists to process Go test output into junit-compatible output
# whenever we are running in CI. Otherwise, it will just output all given input
# to stdout as-is.

REPO_ROOT=$(dirname "${BASH_SOURCE[0]}")/..

MAKEFILE_TARGET="${1:-""}"
OPENSHIFT_CI="${OPENSHIFT_CI:-""}"
ARTIFACT_DIR="${ARTIFACT_DIR:-""}"

if [ ! -n "$MAKEFILE_TARGET" ]; then
  echo "No Makefile target provided"
  exit 1
fi

if [ ! -p /dev/stdin ]; then
  echo "This script expects Go test output to be piped into it"
  exit 1
fi

function generate_junit_report() {
  echo "CI env detected, run tests with junit report extraction"
  if [ -n "$ARTIFACT_DIR" ] && [ -d "$ARTIFACT_DIR" ]; then
    JUNIT_LOCATION="$ARTIFACT_DIR/junit-$MAKEFILE_TARGET.xml"
    echo "junit report location: $JUNIT_LOCATION"
    cat | tee >(go-junit-report > "$JUNIT_LOCATION")
  else
    echo "\$ARTIFACT_DIR not set or does not exists, no junit will be published"
    cat
  fi
}

cd "$REPO_ROOT"

if [ "$OPENSHIFT_CI" == "true" ]; then # detect ci environment there
  generate_junit_report
else
  cat
fi

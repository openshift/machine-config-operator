#!/usr/bin/env bash

set -euo pipefail

# This script exists so that we can ensure that the report files produced by
# golangci-lint are still copied to the ARTIFACT_DIR (if it exists) even when
# golangci-lint exits with a non-zero exit code, such as whenever it finds
# issues.

REPO_ROOT=$(dirname "${BASH_SOURCE[0]}")/..

GOTAGS="${1:-""}"
ARTIFACT_DIR="${ARTIFACT_DIR:-""}"
TIMEOUT="${2:-"10m"}" # Default timeout to 10 minutes if not provided

cd "$REPO_ROOT"

retval=0
golangci-lint run --timeout="$TIMEOUT" --build-tags="$GOTAGS" || retval="$?";

if [ -n "$ARTIFACT_DIR" ] && [ -d "$ARTIFACT_DIR" ]; then
  if [ "$ARTIFACT_DIR" != "$PWD" ]; then
    mv checkstyle-golangci-lint.xml junit-golangci-lint.xml "$ARTIFACT_DIR"
  fi
fi

exit "$retval"

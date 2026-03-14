#!/usr/bin/env bash

set -euo pipefail

# This script exists to run Go unit tests on both a developers local machine as
# well as in CI. In the presence of the ARTIFACT_DIR environment variable, it
# will perform test coverage analysis. In all cases, it will propagate the exit
# code from go test to the caller. This allows the CI job to be marked as
# "FAILED" whenever a test failure occurs.

REPO_ROOT=$(dirname "${BASH_SOURCE[0]}")/..

MAKEFILE_TARGET="${1:-""}"
GOTAGS="${2:-""}"
ARTIFACT_DIR="${ARTIFACT_DIR:-""}"
COVERAGE_REPORT="mco-unit-test-coverage.out"

function run_tests() {
  test_opts=("$@")
  go test "${test_opts[@]}" -tags="${GOTAGS}" './devex/...' './cmd/...' './pkg/...' './lib/...' './test/helpers/...' | ./hack/test-with-junit.sh "$MAKEFILE_TARGET"
}

function run_tests_with_coverage() {
  test_opts=("$@")
  test_opts+=("-coverprofile=$COVERAGE_REPORT")
  run_tests "${test_opts[@]}"
  test_retval="$?"

  html_coverage_report="${COVERAGE_REPORT/.out/.html}"
  if [[ "${PWD}" != "${ARTIFACT_DIR}" ]]; then
    echo "Preparing coverage analysis report"
    go tool cover -html="$COVERAGE_REPORT" -o "$html_coverage_report"
    mv "$COVERAGE_REPORT" "$html_coverage_report" "$ARTIFACT_DIR";
  fi

  exit "$test_retval"
}

if [ ! -n "$MAKEFILE_TARGET" ]; then
  echo "No Makefile target provided"
  exit 1
fi

# Common options to pass to go test
test_opts=( "-v" "-count=1" )

cd "$REPO_ROOT"

if [ -n "$ARTIFACT_DIR" ]; then
  run_tests_with_coverage "${test_opts[@]}"
else
  echo "ARTIFACT_DIR not set; skipping coverage profile collection"
  run_tests "${test_opts[@]}"
  exit "$?"
fi

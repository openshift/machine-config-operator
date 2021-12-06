#!/usr/bin/env bash

set -o errexit
set -o nounset
set -o pipefail

REPO_ROOT=$(dirname "${BASH_SOURCE}")/..

OPENSHIFT_CI=${OPENSHIFT_CI:-""}
ARTIFACT_DIR=${ARTIFACT_DIR:-""}

function runTestsCI() {
  echo "CI env detected, run tests with jUnit report extraction"
  if [ -n "$ARTIFACT_DIR" ] && [ -d "$ARTIFACT_DIR" ]; then
    JUNIT_LOCATION="$ARTIFACT_DIR"/junit_machine_config_operator_bootstrap_e2e.xml
    echo "jUnit location: $JUNIT_LOCATION"
    go install -mod= github.com/jstemmer/go-junit-report@latest
    make bootstrap-e2e-local | tee >(go-junit-report > "$JUNIT_LOCATION")
  else
    echo "\$ARTIFACT_DIR not set or does not exists, no jUnit will be published"
    make bootstrap-e2e-local
  fi
}


cd $REPO_ROOT && \
  source ./hack/fetch-ext-bins.sh && \
  fetch_tools && \
  setup_envs && \
if [ "$OPENSHIFT_CI" == "true" ]; then # detect ci environment there
  runTestsCI
else
  make bootstrap-e2e-local
fi

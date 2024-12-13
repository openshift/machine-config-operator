#!/usr/bin/env bash

set -euo pipefail

# A small script that determines whether we are running in CI to determine
# whether to do a test compilation of the MCO helpers.

REPO_ROOT=$(dirname "${BASH_SOURCE[0]}")/..

if [[ -v OPENSHIFT_CI ]]; then
	cd "$REPO_ROOT";
	make helpers;
else
	echo "OPENSHIFT_CI not set, skipping build check for helpers"
fi

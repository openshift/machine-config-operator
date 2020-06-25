#!/usr/bin/sh
set -xeuo pipefail
# This script just exists for "CLI compatibility" for admins
# to use interactively via ssh/oc debug node.
exec /run/bin/machine-config-daemon pivot "$@"

#!/usr/bin/env bash

set -eu

REPO=${REPO:-"openshift"}

# Push mco image to REPO requested
exec podman push "localhost/machine-config-operator:latest" "${REPO}/origin-machine-config-operator:latest"

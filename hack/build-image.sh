#!/usr/bin/env bash
set -eu
podman=${podman:-podman}
exec $podman build -t "localhost/machine-config-operator:latest" -f Dockerfile --no-cache

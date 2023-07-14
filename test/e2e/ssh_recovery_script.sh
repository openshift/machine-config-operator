#!/usr/bin/env bash

mco_image_pullspec="$1"

oc \
    image extract \
    --registry-config=/var/lib/kubelet/config.json \
    --confirm \
    "$mco_image_pullspec" \
    --path "/usr/bin/machine-config-daemon:$HOME"

cp /etc/machine-config-daemon/currentconfig /etc/ignition-machine-config-encapsulated.json
chmod +x "$HOME/machine-config-daemon"
"$HOME/machine-config-daemon" firstboot-complete-machineconfig

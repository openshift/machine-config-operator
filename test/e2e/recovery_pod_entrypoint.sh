#!/usr/bin/env bash

set -xeuo

mkdir -p -m 700 "$HOME/.ssh"
cp /tmp/key/id_ed25519 "$HOME/.ssh/id_ed25519"
chmod 600 "$HOME/.ssh/id_ed25519"
cp /tmp/scripts/config "$HOME/.ssh/config"
chmod 644 "$HOME/.ssh/config"

scp /tmp/scripts/recover.sh "core@$TARGET_HOST:/tmp/recover.sh"
ssh "core@$TARGET_HOST" "chmod +x /tmp/recover.sh && sudo /tmp/recover.sh '$MCO_IMAGE_PULLSPEC'"

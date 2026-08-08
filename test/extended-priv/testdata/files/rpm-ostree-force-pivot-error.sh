#!/bin/bash

# Fake rpm-ostree that forces a failure on "rebase" subcommand.
# All other commands are forwarded to the real rpm-ostree backup.
if [ "$1" == "rebase" ]; then
  exit 255
fi
exec /var/tmp/rpm-ostree "$@"

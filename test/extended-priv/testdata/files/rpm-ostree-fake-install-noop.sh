#!/bin/bash

# Fake rpm-ostree that silently succeeds on install/override commands.
# All other commands are forwarded to the real rpm-ostree backup.
if [[ "$@" == *"install"* ]] || [[ "$@" == *"override"* ]]; then
  echo "Installing package (fake)"
  exit 0
fi
exec /var/tmp/rpm-ostree "$@"

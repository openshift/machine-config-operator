#!/bin/bash

# Fake rpm that reports usbguard as not installed.
# All other queries are forwarded to the real rpm backup.
if [ "$1" = "-q" ] && [ "$2" = "usbguard" ]; then
  exit 1
fi
/var/tmp/rpm-real "$@"

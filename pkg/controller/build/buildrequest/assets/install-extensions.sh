#!/usr/bin/env bash

set -xeuo

rpm-ostree install "$@"

for pkg in "$@"; do
	if [[ "$pkg" == "usbguard" ]]; then
		echo "d /var/log/usbguard 0755 root root -" > /usr/lib/tmpfiles.d/usbguard.conf
		echo "Created $pkg tmpfiles.d config"
	else
		echo "No post-installation action needed for $pkg"
	fi
done

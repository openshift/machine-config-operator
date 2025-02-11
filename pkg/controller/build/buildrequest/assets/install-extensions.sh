#!/usr/bin/env bash

set -xeuo

rpm-ostree install "$@"

for pkg in "$@"; do
	echo "Post-processing action for $pkg"
done

#!/bin/bash -xe

cat /etc/redhat-release || echo "No /etc/redhat-release"

if [[ -f /etc/hummingbird-release ]] && command -v node >/dev/null 2>&1; then
	echo "In Hummingbird nodejs image, skipping nodejs installation"
else
	dnf -y install nodejs
fi

npm install -g markdownlint@v0.25.1 markdownlint-cli2@v0.4.0

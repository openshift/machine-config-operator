#!/bin/bash -xe
# Install the Markdown toolchain and repair global Node module lookup paths.

cat /etc/redhat-release || echo "No /etc/redhat-release"

if [[ -f /etc/hummingbird-release ]] && command -v node >/dev/null 2>&1; then
  echo "In Hummingbird nodejs image, skipping nodejs installation"
else
  dnf -y install nodejs
fi

npm install -g \
  @googleworkspace/cli@0.22.5 \
  bats@1.13.0 \
  markdownlint-cli2@v0.4.0 \
  markdownlint@v0.25.1 \
  prettier@3.9.9 \
  remark-cli@12.0.1 \
  remark-frontmatter@5.0.0 \
  remark-gfm@4.0.1 \
  remark-parse@11.0.0 \
  remark-preset-lint-recommended@7.0.1 \
  unified@11.0.5 \
  unist-util-visit@5.1.0 \
  yaml@2.8.1

# RHEL images expose the global Node module lookup path under /usr/lib.
if [[ -f /etc/redhat-release && -d /usr/local/lib/node_modules ]]; then
  rm -rf /usr/lib/node_modules
  ln -s /usr/local/lib/node_modules /usr/lib/node_modules
fi

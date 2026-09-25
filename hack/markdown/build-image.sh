#!/usr/bin/env bash
# Build or reuse the Markdown tooling image based on the source-directory hash.

set -euo pipefail

if [[ $# -ne 3 ]]; then
  printf 'usage: %s <image> <containerfile> <build-context>\n' "$0" >&2
  exit 2
fi

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(git -C "$script_dir" rev-parse --show-toplevel)"
runtime="${RUNTIME:-podman}"
image="$1"
containerfile="$2"
build_context="$3"
label_key="io.machine-config-operator.markdown-hash"

# Include both file contents and paths so any Markdown tooling change, addition,
# or removal invalidates the cached image.
markdown_hash="$({
  cd "$repo_root/hack/markdown"
  find . -type f -print0 | sort -z | xargs -0 sha256sum
} | sha256sum | cut -d' ' -f1)"

# Read the prior build's hash from the image instead of rebuilding unchanged
# tooling layers on every Make invocation.
existing_hash="$($runtime image inspect "$image" --format "{{ index .Config.Labels \"$label_key\" }}" 2>/dev/null || true)"
if [[ "$existing_hash" == "$markdown_hash" ]]; then
  printf 'Markdown image is up to date: %s\n' "$image"
  exit 0
fi

printf 'Building Markdown image: %s\n' "$image"
# Store the source hash on the image for the next invocation to compare.
"$runtime" build \
  --label "$label_key=$markdown_hash" \
  -f "$containerfile" \
  --tag "$image" \
  "$build_context"

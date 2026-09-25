#!/usr/bin/env bash
# Download a Google Drive document, preserve frontmatter, and format the result.

set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(git -C "$script_dir" rev-parse --show-toplevel)"

if [[ $# -ne 2 ]]; then
  printf 'usage: %s <document-url-or-id> <relative-markdown-path-under-workspace>\n' "$0" >&2
  exit 2
fi

source "$script_dir/lib.sh"

gws_config_dir="${GWS_CONFIG_DIR:-/root/.config/gws}"
if [[ ! -d "$gws_config_dir" || ! -r "$gws_config_dir" ]]; then
  printf 'Google Workspace credentials are required at %s\n' "$gws_config_dir" >&2
  exit 1
fi

document_reference="$1"
repo_file_path="$2"
validate_markdown_path "$repo_file_path"
repo_file="$repo_root/$repo_file_path"
if [[ ! -f "$repo_file" ]]; then
  printf 'document not found under /workdir: %s\n' "$repo_file_path" >&2
  exit 1
fi

document_id="$(parse_document_reference "$document_reference")"

temp_dir="$(mktemp -d)"
trap 'rm -rf "$temp_dir"' EXIT
frontmatter_file="$temp_dir/frontmatter.md"
export_file="$temp_dir/google-drive.md"

# Preserve an optional frontmatter block without adding a YAML parser dependency.
extract_frontmatter "$repo_file" >"$frontmatter_file"

(cd "$temp_dir" && gws drive files export \
  --params "{\"fileId\": \"$document_id\", \"mimeType\": \"text/markdown\"}" \
  -o "$(basename "$export_file")")

cat "$frontmatter_file" "$export_file" >"$repo_file"
"$script_dir/format-document.sh" "$repo_file_path"

#!/usr/bin/env bash
# Upload a Markdown document to Google Drive and set it to pageless mode.

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
stripped_file="$temp_dir/stripped.md"
frontmatter_file="$temp_dir/frontmatter.md"

# Validate the candidate block before removing it so malformed YAML remains content.
strip_frontmatter "$repo_file" "$frontmatter_file" "$stripped_file"

if [[ -s "$frontmatter_file" ]] && ! validate_frontmatter "$frontmatter_file"; then
  cp "$repo_file" "$stripped_file"
fi

(cd "$temp_dir" && gws drive files update \
  --params "{\"fileId\": \"$document_id\"}" \
  --upload "$(basename "$stripped_file")" \
  --upload-content-type "text/markdown")

gws docs documents batchUpdate \
  --params "{\"documentId\": \"$document_id\"}" \
  --json '{
    "requests": [
      {
        "updateDocumentStyle": {
          "documentStyle": {
            "documentFormat": {
              "documentMode": "PAGELESS"
            }
          },
          "fields": "documentFormat.documentMode"
        }
      }
    ]
  }'

#!/usr/bin/env bash
# Validate a repository-relative path and format its Markdown document.

set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(git -C "$script_dir" rev-parse --show-toplevel)"

if [[ $# -ne 1 ]]; then
  printf 'usage: %s <relative-markdown-path-under-workspace>\n' "$0" >&2
  exit 2
fi

source "$script_dir/lib.sh"
repo_file_path="$1"
validate_markdown_path "$repo_file_path"
document_path="$repo_root/$repo_file_path"
if [[ ! -f "$document_path" ]]; then
	printf 'document not found under %s\n' "$repo_root/$repo_file_path" >&2
	exit 1
fi

format_markdown_document "$document_path"

#!/usr/bin/env bash
# Provide shared formatting, path, frontmatter, and Google document helpers.

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

format_markdown_document() {
  # Format any existing Markdown file with the configured Prettier and Remark passes.
  if [[ $# -ne 1 || ! -f "$1" ]]; then
    printf 'Markdown document not found: %s\n' "${1:-}" >&2
    return 1
  fi

  local document_path="$1"
  prettier \
    --config "$script_dir/config/.prettierrc.json" \
    --write "$document_path"

  remark "$document_path" \
    --ext md \
    --rc-path "$script_dir/config/.remarkrc.json" \
    --use "$script_dir/config/remark-fix-mermaid.mjs" \
    --use "$script_dir/config/remark-fix-quotes.mjs" \
    --output
}

parse_document_reference() {
  # Normalize a Google document URL or ID to the document ID used by the API.
  local document_reference="$1"
  local document_id="$document_reference"

  if [[ "$document_reference" == */d/* ]]; then
    document_id="${document_reference#*/d/}"
    document_id="${document_id%%/*}"
    document_id="${document_id%%\?*}"
    document_id="${document_id%%\#*}"
  fi
  if [[ ! "$document_id" =~ ^[A-Za-z0-9_-]+$ ]]; then
    printf 'invalid Google document URL or ID: %s\n' "$document_reference" >&2
    return 2
  fi

  printf '%s\n' "$document_id"
}

validate_markdown_path() {
  # Reject absolute, traversing, or non-Markdown paths before use.
  local markdown_file="$1"
  local location="${2:-workspace}"

  if [[ "$markdown_file" = /* || "$markdown_file" == .. || "$markdown_file" == ../* || "$markdown_file" == */../* || "$markdown_file" != *.md ]]; then
    printf 'Markdown file must be a relative .md path under the %s: %s\n' "$location" "$markdown_file" >&2
    return 2
  fi
}

validate_frontmatter() {
  # Validate a document's YAML frontmatter with the shared Node parser.
  node "$script_dir/config/validate-frontmatter.mjs" "$1"
}

strip_frontmatter() {
  # Separate a leading closed frontmatter block from the document body.
  local repo_file="$1"
  local frontmatter_file="$2"
  local stripped_file="$3"

  # Preserve the whole file when a leading frontmatter block is not closed.
  awk -v frontmatter_file="$frontmatter_file" -v stripped_file="$stripped_file" '
    { lines[NR] = $0 }
    NR == 1 && $0 == "---" { in_frontmatter = 1; next }
    in_frontmatter && $0 == "---" { closing_delimiter = NR; in_frontmatter = 0 }
    END {
      if (closing_delimiter) {
        for (line = 1; line <= closing_delimiter; line++) {
          print lines[line] > frontmatter_file
        }
        for (line = closing_delimiter + 1; line <= NR; line++) {
          print lines[line] > stripped_file
        }
      } else {
        for (line = 1; line <= NR; line++) {
          print lines[line] > stripped_file
        }
      }
    }
  ' "$repo_file"
}

extract_frontmatter() {
  # Print a leading frontmatter block only when its closing delimiter exists.
  local repo_file="$1"

  # Emit frontmatter only when its closing delimiter is present.
  awk '
    { lines[NR] = $0 }
    NR == 1 && $0 == "---" { in_frontmatter = 1; next }
    in_frontmatter && $0 == "---" { closing_delimiter = NR; in_frontmatter = 0 }
    END {
      if (closing_delimiter) {
        for (line = 1; line <= closing_delimiter; line++) {
          print lines[line]
        }
      }
    }
  ' "$repo_file"
}

#!/usr/bin/env bats
# Test shared formatter and Google document helper functions.

repo_root="$(git rev-parse --show-toplevel)"
load "$repo_root/hack/markdown/formatter/lib.sh"
fixture_dir="$repo_root/hack/markdown/formatter/test/fixtures"

setup() {
  # Create an isolated directory for helper function file operations.
  test_dir="$(mktemp -d)"
}

teardown() {
  # Remove helper test artifacts.
  rm -rf "$test_dir"
}

@test "parses a Google document ID and URL" {
  # Verify both supported Google document reference forms normalize correctly.
  [ "$(parse_document_reference 'document-id')" = 'document-id' ]
  [ "$(parse_document_reference 'https://docs.google.com/document/d/document-id/edit')" = 'document-id' ]
}

@test "rejects an invalid Google document reference" {
  # Verify malformed document references return an error.
  run parse_document_reference 'not a document ID'

  [ "$status" -eq 2 ]
}

@test "validates relative Markdown paths" {
  # Verify a safe repository-relative Markdown path is accepted.
  validate_markdown_path 'docs/example.md'
}

@test "rejects unsafe or non-Markdown paths" {
  # Verify traversal and non-Markdown paths are rejected.
  run validate_markdown_path '../example.md'
  [ "$status" -eq 2 ]

  run validate_markdown_path 'docs/example.txt'
  [ "$status" -eq 2 ]
}

@test "strips closed frontmatter and preserves it separately" {
  # Verify a closed frontmatter block is separated from the document body.
  input_file="$test_dir/input.md"
  frontmatter_file="$test_dir/frontmatter.md"
  stripped_file="$test_dir/stripped.md"
  # This fixture has a complete frontmatter block followed by document content.
  cp "$fixture_dir/frontmatter-closed.md" "$input_file"

  strip_frontmatter "$input_file" "$frontmatter_file" "$stripped_file"

  run cat "$frontmatter_file"
  [ "$output" = $'---\ntitle: Example\n---' ]
  run cat "$stripped_file"
  [ "$output" = $'\n# Body' ]
}

@test "preserves an unclosed block as document content" {
  # Verify an unclosed frontmatter block remains document content.
  input_file="$test_dir/input.md"
  frontmatter_file="$test_dir/frontmatter.md"
  stripped_file="$test_dir/stripped.md"
  # This fixture starts frontmatter but omits its closing delimiter.
  cp "$fixture_dir/frontmatter-unclosed.md" "$input_file"

  strip_frontmatter "$input_file" "$frontmatter_file" "$stripped_file"

  [ ! -s "$frontmatter_file" ]
  run cat "$stripped_file"
  [ "$output" = $'---\ntitle: Example\n# Body' ]
}

@test "extracts only a closed frontmatter block" {
  # Verify extraction emits only a complete leading frontmatter block.
  input_file="$test_dir/input.md"
  # This fixture contains closed frontmatter followed by a body heading.
  cp "$fixture_dir/frontmatter-closed.md" "$input_file"

  run extract_frontmatter "$input_file"

  [ "$output" = $'---\ntitle: Example\n---' ]
}

@test "does not extract an unclosed frontmatter block" {
  # Verify extraction emits nothing for an unclosed block.
  input_file="$test_dir/input.md"
  # This fixture contains frontmatter content without a closing delimiter.
  cp "$fixture_dir/frontmatter-unclosed.md" "$input_file"

  run extract_frontmatter "$input_file"

  [ -z "$output" ]
}

@test "validates frontmatter YAML" {
  # Verify valid YAML passes and malformed YAML fails validation.
  valid_file="$test_dir/valid.md"
  invalid_file="$test_dir/invalid.md"
  # These fixtures distinguish valid frontmatter from malformed YAML.
  cp "$fixture_dir/frontmatter-closed.md" "$valid_file"
  cp "$fixture_dir/frontmatter-invalid.md" "$invalid_file"

  validate_frontmatter "$valid_file"
  run validate_frontmatter "$invalid_file"
  [ "$status" -ne 0 ]
}

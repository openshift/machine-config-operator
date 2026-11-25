#!/usr/bin/env bats
# Test reusable formatting and the repository-relative formatting wrapper.

repo_root="$(git rev-parse --show-toplevel)"
load "$repo_root/hack/markdown/formatter/lib.sh"
fixture_dir="$repo_root/hack/markdown/formatter/test/fixtures"

setup() {
  # Copy the fixture combining frontmatter, prose artifacts, lists, and code
  # blocks to exercise every formatting transformation.
  test_dir="$(mktemp -d /tmp/bats-format.XXXXXX)"
  test_file="$test_dir/input.md"
  cp "$fixture_dir/format-input.md" "$test_file"
}

teardown() {
  # Remove the temporary formatting fixture.
  rm -rf "$test_dir"
}

@test "format_markdown_document applies the intended transformations" {
  # Verify the reusable formatter handles prose, frontmatter, and code blocks.
  run format_markdown_document "$test_file"
  [ "$status" -eq 0 ]

  run grep -Fq 'title: “Keep” – …' "$test_file"
  [ "$status" -eq 0 ]
  run grep -Fq 'This is "prose" - with -- ellipsis ... and space.' "$test_file"
  [ "$status" -eq 0 ]
  run grep -Fq -- '- one' "$test_file"
  [ "$status" -eq 0 ]
  run grep -Fq '```mermaid' "$test_file"
  [ "$status" -eq 0 ]
  run grep -Fq '%%{init: {"theme": "dark"}}%%' "$test_file"
  [ "$status" -eq 0 ]
  run grep -Fq '```text' "$test_file"
  [ "$status" -eq 0 ]
  run grep -Fq 'const quote = “keep” -- …;' "$test_file"
  [ "$status" -eq 0 ]
}

@test "format-document accepts repository-relative paths" {
  # Verify the command wrapper accepts a path relative to the repository root.
  wrapper_dir="$(mktemp -d "$repo_root/bats-format-wrapper.XXXXXX")"
  wrapper_file="$wrapper_dir/input.md"
  printf '%s\n' '# Wrapper test' >"$wrapper_file"

  run "$repo_root/hack/markdown/formatter/format-document.sh" \
    "${wrapper_file#"$repo_root/"}"
  [ "$status" -eq 0 ]

  rm -rf "$wrapper_dir"
}

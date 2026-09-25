#!/usr/bin/env bats
# Exercise the Markdown linter with a valid document.

repo_root="$(git rev-parse --show-toplevel)"
fixture_dir="$repo_root/hack/markdown/formatter/test/fixtures"

setup() {
  # Copy the valid fixture so the linter test remains isolated.
  test_dir="$(mktemp -d "$repo_root/bats-lint.XXXXXX")"
  cp "$fixture_dir/lint-valid.md" "$test_dir/input.md"
}

teardown() {
  # Remove the temporary lint fixture.
  rm -rf "$test_dir"
}

@test "markdownlint accepts a valid Markdown document" {
  # Verify the linter exits successfully for valid Markdown.
  run env LINT_TARGET="$test_dir" "$repo_root/hack/markdown/markdownlint.sh"

  [ "$status" -eq 0 ]
}

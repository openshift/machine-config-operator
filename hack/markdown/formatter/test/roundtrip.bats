#!/usr/bin/env bats
# Test Markdown roundtrips through Google Drive when credentials are available.

repo_root="$(git rev-parse --show-toplevel)"
load "$repo_root/hack/markdown/formatter/lib.sh"
fixture_dir="$repo_root/hack/markdown/formatter/test/fixtures"

document_url="${GWS_TEST_DOCUMENT_URL:-}"

setup_file() {
  # Skip the entire file when the roundtrip prerequisites are unavailable.
  if [[ -n "${ROUNDTRIP_SKIP_REASON:-}" ]]; then
    skip "$ROUNDTRIP_SKIP_REASON"
  fi
  if [[ -z "${GWS_TEST_DOCUMENT_URL:-}" ]]; then
    skip 'DOCUMENT_URL is not set'
  fi
  if [[ ! -d "${GWS_CONFIG_DIR:-/tmp/.config/gws}" ]]; then
    skip 'Google Workspace credentials are unavailable'
  fi
}

setup() {
  # Create a repository-local fixture because Drive helpers require relative paths.
  # Drive synchronization also requires the document path to be inside the
  # repository so it can be passed as a relative Markdown path.
  test_dir="$(mktemp -d "$repo_root/bats-roundtrip.XXXXXX")"
  document_file="${test_dir#"$repo_root/"}/input.md"
}

teardown() {
  # Remove the temporary roundtrip document.
  rm -rf "$test_dir"
}

roundtrip() {
  # Format, upload, download, and compare one document variant.
  local with_frontmatter="$1"
  local expected_file="$BATS_TEST_TMPDIR/expected.md"

  if [[ "$with_frontmatter" == true ]]; then
    # This fixture tests preserving frontmatter around a Mermaid document.
    cp "$fixture_dir/roundtrip-with-frontmatter.md" "$repo_root/$document_file"
  else
    # This fixture tests roundtripping a document with no frontmatter.
    cp "$fixture_dir/roundtrip-without-frontmatter.md" "$repo_root/$document_file"
  fi
  run "$repo_root/hack/markdown/formatter/format-document.sh" "$document_file"
  [ "$status" -eq 0 ]
  cp "$repo_root/$document_file" "$expected_file"

  run "$repo_root/hack/markdown/formatter/push-to-google-drive.sh" "$document_url" "$document_file"
  [ "$status" -eq 0 ]

  run "$repo_root/hack/markdown/formatter/pull-from-google-drive.sh" "$document_url" "$document_file"
  [ "$status" -eq 0 ]
  expected_hash="$(sha256sum "$expected_file" | awk '{print $1}')"
  actual_hash="$(sha256sum "$repo_root/$document_file" | awk '{print $1}')"
  [ "$expected_hash" = "$actual_hash" ]
}

@test "roundtrips Markdown with frontmatter through Google Drive" {
  # Verify roundtripping preserves a local frontmatter block.
  roundtrip true
}

@test "roundtrips Markdown without frontmatter through Google Drive" {
  # Verify roundtripping works without local frontmatter.
  roundtrip false
}

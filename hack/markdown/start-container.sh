#!/usr/bin/env bash
# Run a Markdown operation in the configured container runtime.

set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(git -C "$script_dir" rev-parse --show-toplevel)"
runtime="${RUNTIME:-podman}"
image="${MARKDOWN_IMAGE:-mco-markdownlint:latest}"

usage() {
  # Print the supported container operations and terminate with a usage error.
  printf 'usage: %s lint [target]\n' "$0" >&2
  printf '       %s format <markdown-file>\n' "$0" >&2
  printf '       %s pull <document-url-or-id> <markdown-file>\n' "$0" >&2
  printf '       %s push <document-url-or-id> <markdown-file>\n' "$0" >&2
  printf '       %s test\n' "$0" >&2
  exit 2
}

[[ $# -ge 1 ]] || usage
operation="$1"
shift

container_args=(
  run
  --interactive
  --tty
  --rm
  --user "$(id -u):$(id -g)"
  --read-only
  --tmpfs /tmp
  -v "$repo_root:/workdir:Z"
  --workdir /workdir
)
if command -v podman >/dev/null 2>&1 && [[ "$runtime" == podman ]]; then
  container_args+=(--userns=keep-id)
fi

credentials_dir="${HOME:-}/.config/gws"
staging_dir=""
cleanup() {
  # Remove the disposable credentials copy after the container exits.
  if [[ -n "$staging_dir" ]]; then
    rm -rf -- "$staging_dir"
  fi
}
trap cleanup EXIT

use_credentials() {
  # Stage credentials privately and add their container mount arguments.
  if [[ ! -d "$credentials_dir" ]]; then
    return 1
  fi
  staging_dir="$(mktemp -d)"
  cp -a "$credentials_dir/." "$staging_dir/"
  container_args+=(
    -v "$staging_dir:/tmp/.config/gws:Z"
    --env GWS_CONFIG_DIR=/tmp/.config/gws
  )
}

case "$operation" in
lint)
  [[ $# -le 1 ]] || usage
  container_args+=(
    --env "LINT_TARGET=${1:-}"
    --entrypoint=/workdir/hack/markdown/markdownlint.sh
  )
  ;;
format)
  [[ $# -eq 1 ]] || usage
  container_args+=(--entrypoint=/workdir/hack/markdown/formatter/format-document.sh)
  ;;
pull | push)
  [[ $# -eq 2 ]] || usage
  [[ -n "${DOCUMENT_URL:-$1}" ]] || {
    printf 'DOCUMENT_URL is required\n' >&2
    exit 2
  }
  if ! use_credentials; then
    printf 'No GWS creds found in %s\n' "$credentials_dir" >&2
    exit 2
  fi
  if [[ "$operation" == pull ]]; then
    container_args+=(--entrypoint=/workdir/hack/markdown/formatter/pull-from-google-drive.sh)
  else
    container_args+=(--entrypoint=/workdir/hack/markdown/formatter/push-to-google-drive.sh)
  fi
  ;;
test)
  [[ $# -eq 0 ]] || usage
  roundtrip_skip_reason=""
  if [[ -z "${DOCUMENT_URL:-}" ]]; then
    roundtrip_skip_reason="DOCUMENT_URL is not set"
  elif [[ ! -d "$credentials_dir" ]]; then
    roundtrip_skip_reason="Google Workspace credentials are unavailable at $credentials_dir"
  else
    use_credentials
  fi
  container_args+=(
    --env GWS_CONFIG_DIR=/tmp/.config/gws
    --env "GWS_TEST_DOCUMENT_URL=${DOCUMENT_URL:-}"
    --env "ROUNDTRIP_SKIP_REASON=$roundtrip_skip_reason"
    --entrypoint bats
    "$image"
    /workdir/hack/markdown/formatter/test
  )
  "$runtime" "${container_args[@]}"
  exit $?
  ;;
*)
  usage
  ;;
esac

case "$operation" in
lint)
  container_args+=("$image")
  ;;
format)
  container_args+=("$image" "$1")
  ;;
pull | push)
  container_args+=("$image" "${DOCUMENT_URL:-$1}" "$2")
  ;;
esac

"$runtime" "${container_args[@]}"

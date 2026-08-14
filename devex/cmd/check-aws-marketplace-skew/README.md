# check-aws-marketplace-skew

A `make` target an engineer runs occasionally to check whether published AWS Marketplace RHCOS
AMIs have drifted out of MCO's boot-image skew band, so a stale or mismatched Marketplace image
can be caught before customers hit it.

For each known Marketplace product code (OCP/OKE/OPP × x86_64/arm64, plus their EMEA x86_64
variants — ROSA Classic is excluded, as it's being sunset), it fetches every published AMI and
checks whether **at least one** falls within an acceptable version band:

- **Floor**: `RHCOSVersionBootImageSkewLimit`/`OCPVersionBootImageSkewLimit`
  (`pkg/controller/common/constants.go`) as they stood ~4 months ago, reconstructed from
  `openshift/machine-config-operator`'s GitHub history for `--branch` — not persisted state — so
  Marketplace gets a grace period to catch up after MCO bumps its skew limit.
- **Ceiling**: the pinned RHCOS version for `--branch`, fetched live from `openshift/installer`'s
  `data/data/coreos/coreos-rhel-10.json` on the matching branch.

The check is existential, not singular: Marketplace can keep multiple AMI versions live at once, so
the current/default one being out of band doesn't necessarily mean there's no compliant option.

## Usage

```console
$ make check-aws-marketplace-skew
Skew-limit floor: RHCOS 9.2 (OCP 4.13.0)
Installer ceiling (x86_64): 10.2
Installer ceiling (arm64): 10.2

PRODUCT           PRODUCT ID                            RESULT  MATCHED AMI            DETAIL
OCP x86_64        59ead7de-2540-4653-a8b0-fa7926d5c845   PASS    ami-0123456789abcdef0  9.6.20260210-0
OKE x86_64        963b36c3-de6f-48ed-b802-2b38b2a2cdeb   PASS    ami-0fedcba9876543210  9.6.20260210-0
...
```

Pass flags through with `ARGS`, e.g. `make check-aws-marketplace-skew ARGS="--json"`.

Flags:

- `--region` (default `us-east-1`): AWS region to query `DescribeImages` in. A single region is
  sufficient — the version signal lives in the AMI `Name`/`Description` text, which is consistent
  across every region a Marketplace listing replicates to.
- `--profile`: named AWS profile to use. Defaults to whatever the standard credential chain
  resolves — if `aws` CLI commands already work for you, this tool will too.
- `--branch` (default `main`): the release branch to check, applied on both sides — the MCO
  skew-limit floor is reconstructed from `openshift/machine-config-operator`'s history for this
  branch, and the RHCOS ceiling is fetched from `openshift/installer`'s copy of the same branch
  name. Both are fetched live from GitHub; no local checkout of either repo is needed, so this
  works the same regardless of what `origin` points at locally (e.g. a personal fork that doesn't
  mirror release branches).
- `--json`: emit a structured JSON report instead of a human-readable table.

Exit code is non-zero if any product code fails its band check.

## Credentials

Requires the `aws` CLI to be installed and on `PATH` — this tool shells out to
`aws ec2 describe-images` rather than using the AWS Go SDK, so it has no AWS SDK dependency of its
own and just inherits whatever credentials already make `aws` CLI commands work for you
(environment variables, shared config/credentials file, SSO sessions). Use `--profile` to select a
named profile, same as `aws --profile`.

The skew-limit floor also makes one `api.github.com` call per run (listing commits touching
`pkg/controller/common/constants.go`), which is unauthenticated by default and subject to GitHub's
60-requests/hour-per-IP limit — easy to hit on a shared NAT. Set `GITHUB_TOKEN` (the standard env
var honored by `gh`, GitHub Actions, etc.) to raise that to 5000/hour; no new flag needed. The
`raw.githubusercontent.com` fetches (installer ceiling, and the floor's file-at-commit lookup)
aren't subject to this same limit and don't need a token.

## Known gaps

- There are no AWS credentials in this repo's own CI, so `DescribeImages`/`CheckProduct` aren't
  exercised end-to-end by `go test` — those tests use fixtures instead. Verified manually against
  real Marketplace AMIs.
- Similarly, calls against the *real* GitHub endpoints aren't exercised by `go test` — this
  package's tests run as part of the whole repo's presubmit suite, and a real network dependency
  there would trade one devex tool's coverage for flakiness across every PR. `githubCommitsForPath`
  itself (pagination, stop-on-short-page, 404 handling) *is* unit-tested against a local
  `httptest.Server`, as are the pure parsing/selection functions (`parseGitHubCommitsJSON`,
  `selectHistoricalCommit`, `parseSkewLimitConstants`, `parseStreamCeiling`) — only the thin
  HTTP-call wrappers (`fetchGitHubCommitsPage`'s live request, `fetchRawGitHubFile`) are verified
  manually against the real API instead.
- The skew-limit floor reconstruction fetches at most 500 commits (5 pages of 100 — GitHub's own
  per-page cap) touching `pkg/controller/common/constants.go` on `--branch`, stopping early once a
  page comes back short. Real history has 63 commits touching that file today, so this is
  comfortable headroom, but if that count ever passes 500 for a branch, the "no commit predates the
  grace window" fallback would pick the oldest of the *fetched* commits rather than the file's true
  oldest revision.
- `openshift/installer`'s stream metadata filename is RHEL-major-version-specific
  (`coreos-rhel-10.json`) — will need a manual update when RHEL 11 lands. Not building dynamic
  discovery for this now.

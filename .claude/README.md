# MCO QE Tooling

Automation scripts for Machine Config Operator quality engineering workflows: fetch, create, and update Polarion test cases from multiple sources.

**Quick start**: New user? Run `./scripts/setup.sh --verify` and see § Setup below.

---

## Table of Contents

1. [Setup](#setup)
2. [Commands](#commands)
3. [Workflows](#workflows)
4. [TC Format](#tc-format-good-vs-bad)
5. [Scripts Reference](#scripts-reference)
6. [Troubleshooting](#troubleshooting)
7. [Claude Code Examples](#claude-code-examples)

---

## Setup

### Prerequisites

- Python 3.9+
- `requests` library: `pip install requests`
- Polarion API token
- Optional: GitHub token, Jira token

### Initial Setup

**1. Run setup script**:
```bash
cd .claude
./scripts/setup.sh
```

Creates `/tmp/test-specs/` directory structure.

**2. Configure tokens**:

Create `.env` at **repo root** (gitignored):
```bash
# Required for Polarion
POLARION_URL=https://polarion.engineering.redhat.com
POLARION_TOKEN=your_personal_access_token_here

# Optional: For updating test steps on existing TCs (SOAP API)
POLARION_USERNAME=your_username
POLARION_PASSWORD=your_password

# Optional: For GitHub (public repos work without token)
GITHUB_TOKEN=your_github_token_here

# Optional: For Jira integration
JIRA_URL=https://redhat.atlassian.net
JIRA_EMAIL=you@redhat.com
JIRA_TOKEN=your_jira_api_token_here
```

**Get tokens**:
- **Polarion**: https://polarion.engineering.redhat.com/polarion/#/user/me → Personal Access Tokens
- **GitHub**: https://github.com/settings/tokens → Generate new token (public_repo scope)
- **Jira**: https://id.atlassian.com/manage-profile/security/api-tokens → Create API token

**3. Verify setup**:
```bash
./scripts/setup.sh --verify
```

Expected output:
```
✓ Python 3.12.1 found
✓ requests already installed
✓ POLARION_TOKEN configured
✓ Polarion connection successful (HTTP 200)
  Project: OpenShift (OSE)
```

---

## Commands

Active commands (use via Claude Code or call scripts directly):

### /mco-fetch

Fetch test cases from Polarion, GitHub PRs, or Jira.

```bash
# Fetch Polarion TC
python3 scripts/fetch_polarion.py OCP-88122

# Fetch GitHub PR comment
python3 scripts/fetch_github_pr.py \
  --repo openshift/machine-config-operator \
  --pr 5691 \
  --comment 4054832254

# Fetch Jira issue
python3 scripts/fetch_jira.py OCPBUGS-74223
```

**Output**: JSON draft in `/tmp/test-specs/drafts/` or `/tmp/test-specs/jira/`

### /mco-create-tc-from-pr

Create Polarion test case from a GitHub PR QE comment or review.

```bash
# From PR comment (specific anchor)
/mco-create-tc-from-pr https://github.com/openshift/machine-config-operator/pull/5691#issuecomment-4054832254

# From PR (scans all comments/reviews for QE content)
/mco-create-tc-from-pr https://github.com/openshift/machine-config-operator/pull/5691
```

**Output**: OCP-xxxxx URL

### /mco-create-tc-from-jira

Create Polarion test case from a Jira issue or QE comment.

```bash
# From Jira issue (scans comments for QE verification)
/mco-create-tc-from-jira https://redhat.atlassian.net/browse/OCPBUGS-86695

# From specific Jira comment
/mco-create-tc-from-jira https://redhat.atlassian.net/browse/OCPBUGS-86695?focusedCommentId=17167775
```

**Output**: OCP-xxxxx URL

### /automate-test

Generate Go e2e test from Polarion test case.

```bash
# Fetch TC and generate test
/automate-test OCP-88122 "mco osstream" disruptive
```

**Workflow**:
1. Fetches OCP-88122 from Polarion → `/tmp/test-specs/OCP-88122.txt`
2. Reads test steps from .txt file
3. Generates `test/extended-priv/mco_osstream.go`
4. Builds with `make machine-config-tests-ext`

---

## Workflows

### Workflow 1: Create TC from GitHub PR

**Use when**: PR has QE "Pre-merge testing" comment with test steps.

```bash
# 1. Fetch PR comment
python3 scripts/fetch_github_pr.py \
  --repo openshift/machine-config-operator \
  --pr 5691 \
  --comment 4054832254

# 2. Validate
python3 scripts/validate_tc.py /tmp/test-specs/drafts/pr-5691.json

# 3. Review draft
cat /tmp/test-specs/drafts/pr-5691.json | jq .

# 4. Create (dry run first)
python3 scripts/create_polarion_tc.py \
  /tmp/test-specs/drafts/pr-5691.json \
  --dry-run

# 5. Create for real
python3 scripts/create_polarion_tc.py \
  /tmp/test-specs/drafts/pr-5691.json
```

**Result**: OCP-xxxxx created with title, metadata (6 fields), and test steps.

### Workflow 2: Create TC from Jira

**Use when**: Jira issue has acceptance criteria, no PR test steps yet.

```bash
# 1. Fetch Jira
python3 scripts/fetch_jira.py OCPBUGS-74223

# 2. Validate (will warn about empty test_steps)
python3 scripts/validate_tc.py /tmp/test-specs/jira/OCPBUGS-74223.json

# 3. Create TC with metadata only
python3 scripts/create_polarion_tc.py \
  /tmp/test-specs/jira/OCPBUGS-74223.json
```

**Result**: TC created with metadata from Jira, empty test steps (add later manually or via update).

**Jira fields extracted**:
- Summary → title (with `[MCO][JIRA-ID]` prefix)
- Fix Versions → version (extracts `4.18` from `4.18.0`)
- Description + Acceptance Criteria → description
- Issue Type, Labels → metadata

### Workflow 3: Hybrid (Jira + GitHub PR) - **RECOMMENDED**

**Use when**: Jira has metadata, GitHub PR has test steps. Best of both worlds.

```bash
# One command - fetches both, merges, validates, creates
python3 scripts/create_tc_hybrid.py OCPBUGS-74223 \
  --from-pr 5691 \
  --comment 4054832254
```

**What it does**:
1. Fetches Jira OCPBUGS-74223 → version, description, acceptance criteria
2. Fetches GitHub PR 5691 comment → test steps with commands
3. Merges: Jira metadata + PR steps
4. Validates merged draft
5. Shows summary and confirmation prompt
6. Creates TC in Polarion

**Result**: Complete TC with accurate metadata and executable test steps.

**Merge logic**:
- Title: `[MCO][JIRA-ID] <Jira summary>`
- Version: From Jira fix versions
- Description: Jira description + acceptance criteria
- Test steps: From GitHub PR QE comment
- Jira ID: From Jira issue key

### Workflow 4: Fix Existing TC

**Use when**: TC has empty required fields (Component, Sub Team, etc.).

```bash
# Fix metadata
python3 scripts/update_polarion_tc.py OCP-88941 \
  --fix-metadata \
  --version 4.18 \
  --trello-jira OCPBUGS-83830

# Verify changes
python3 scripts/fetch_polarion.py OCP-88941
```

**`--fix-metadata` sets defaults**:
- Component: Machine Config Operator
- Sub Team: MCO
- Products: OCP
- Test Type: Functional
- Version: (from --version flag)
- Trello/Jira: (from --trello-jira flag)

### Workflow 5: Polarion TC → Go Test

**Use when**: Polarion TC exists, need to generate automated Go test.

```bash
# 1. Fetch TC from Polarion
python3 scripts/fetch_polarion.py OCP-88122
# Creates /tmp/test-specs/OCP-88122.txt

# 2. Generate Go test (via Claude Code)
/automate-test OCP-88122 "mco osstream" disruptive

# 3. Build
make machine-config-tests-ext

# 4. Verify test appears
./_output/linux/amd64/machine-config-tests-ext list | grep OCP-88122
```

**Result**: `test/extended-priv/mco_osstream.go` with test implementation.

---

## TC Format (Good vs Bad)

### Good Format: OCP-88122.pdf

See `OCP-88122.pdf` in repo root.

**Characteristics**:
- ✓ Title: `[MCO][MCO-2136] Validate osImageStream inheritance...`
- ✓ Component: Machine Config Operator
- ✓ Sub Team: MCO
- ✓ Products: OCP
- ✓ Test Type: Functional
- ✓ Version: 4.22
- ✓ Trello/Jira: MCO-2136
- ✓ Test Steps: Commands in Expected Result column (not narrative)

**Example step**:
```
Step: Check the osstream
Expected Result:
$ oc get mcp rhel9 -ojsonpath='{.status.osImageStream.name}'
rhel9
```

### Bad Format: wrong.pdf (OCP-88941)

See `wrong.pdf` in repo root.

**Problems**:
- ❌ Component: (empty)
- ❌ Sub Team: (empty)
- ❌ Products: (empty)
- ❌ Test Type: (empty)
- ❌ Version: (empty)
- ❌ Trello/Jira: (empty)
- ❌ Test Steps: Narrative only, no commands

**How this happened**: Created without validation, or metadata not set during creation.

**How to fix**: Use `update_polarion_tc.py --fix-metadata`

### Validation Rules

Scripts enforce these rules:

1. **Title format**: Must match `^\[MCO\]\[.+\] .+`
   - Good: `[MCO][OCPBUGS-74223] Add feature X`
   - Bad: `Add feature X` (missing prefix)

2. **Required fields** (all 6 must be non-empty):
   - component
   - sub_team
   - products
   - test_type
   - version
   - trello_jira

3. **Test steps quality**:
   - Each step must have non-empty `step` field
   - Expected results should have commands (not narrative-only)
   - Warning if expected result has no `oc`, `kubectl`, code syntax

Run validation before creating:
```bash
python3 scripts/validate_tc.py /tmp/test-specs/drafts/pr-5691.json
```

---

## Scripts Reference

| Script | Purpose | Input | Output |
|---|---|---|---|
| `setup.sh` | Environment setup + verification | - | Status messages |
| `validate_tc.py` | Validate draft JSON | Draft JSON path or `-` (stdin) | Validation result |
| `fetch_polarion.py` | Fetch Polarion TC | OCP-88122 | JSON (stdout) + `/tmp/test-specs/OCP-88122.txt` |
| `fetch_github_pr.py` | Parse GitHub PR QE comment | `--repo org/repo --pr NUM --comment ID` | `/tmp/test-specs/drafts/pr-NUM.json` |
| `fetch_jira.py` | Fetch Jira issue | OCPBUGS-12345 | `/tmp/test-specs/jira/OCPBUGS-12345.json` |
| `create_polarion_tc.py` | Create TC from draft | Draft JSON path | OCP-xxxxx URL |
| `create_tc_hybrid.py` | Merge Jira + PR, create TC | `JIRA-KEY --from-pr NUM` | OCP-xxxxx URL |
| `update_polarion_tc.py` | Update existing TC | OCP-xxxxx + flags | Updated field count |
| `polarion_client.py` | REST/SOAP API client | (library) | - |

### Common Flags

**validate_tc.py**:
- `--strict`: Fail on warnings
- `--sample`: Show validation examples

**create_polarion_tc.py**:
- `--dry-run`: Show summary, don't create
- `--project OSE`: Override project (default: OSE)

**update_polarion_tc.py**:
- `--fix-metadata`: Set all required fields to defaults
- `--title TEXT`: Update title
- `--status STATUS`: Update status (draft, approved, etc.)
- `--version VERSION`: Update version
- `--trello-jira KEY`: Update Jira ID
- `--steps-file PATH`: Update test steps (requires SOAP API)
- `--dry-run`: Preview changes

**fetch_github_pr.py**:
- `--repo ORG/REPO`: Repository (required)
- `--pr NUM`: PR number (required)
- `--comment ID`: Comment ID (optional - finds QE comment if omitted)
- `--output PATH`: Custom output path

**create_tc_hybrid.py**:
- `--from-pr NUM`: GitHub PR number (required)
- `--comment ID`: Comment ID (optional)
- `--repo ORG/REPO`: Repository (default: openshift/machine-config-operator)
- `--dry-run`: Preview without creating

---

## Troubleshooting

### "POLARION_TOKEN not configured"

**Cause**: Token missing or placeholder value in `.env`

**Fix**:
1. Generate token: https://polarion.engineering.redhat.com/polarion/#/user/me
2. Add to `.env` at repo root
3. Verify: `./scripts/setup.sh --verify`

### "Validation failed: Required field 'version' is empty"

**Cause**: Draft missing required field

**Fix**:
```bash
# Edit draft manually
vim /tmp/test-specs/drafts/pr-5691.json

# Or set during creation
python3 scripts/create_tc_hybrid.py JIRA-KEY --from-pr NUM
# (hybrid mode auto-fills from Jira)
```

### "SOAP API requires username and password"

**Cause**: Trying to update test steps on existing TC without SOAP credentials

**Context**: Polarion REST API can only POST test steps to NEW TCs. For existing TCs, must use SOAP API.

**Fix**:
```bash
# Add to .env
POLARION_USERNAME=your_username
POLARION_PASSWORD=your_password

# Then retry
python3 scripts/update_polarion_tc.py OCP-88122 \
  --steps-file /tmp/test-specs/steps/new-steps.json
```

### "GitHub API error: 401 Unauthorized"

**Cause**: Invalid or missing GitHub token

**Fix**:
- For public repos: Script auto-retries without token
- For private repos or to avoid rate limits: Add GITHUB_TOKEN to `.env`
- Generate token: https://github.com/settings/tokens (public_repo scope)

### "Jira authentication failed"

**Cause**: Invalid token or missing email

**Fix**:
```bash
# Jira (Atlassian Cloud) requires email + token
JIRA_EMAIL=you@redhat.com
JIRA_TOKEN=your_token
```

Generate token: https://id.atlassian.com/manage-profile/security/api-tokens

### Test steps narrative-only (no commands)

**Symptom**: Validation warns "Expected result appears to be narrative-only"

**Fix**: Edit draft to add commands:
```json
{
  "step": "Verify feature applied",
  "expected_result": "$ oc get mc | grep feature\nfeature   3.2.0   15s"
}
```

---

## Claude Code Examples

If using Claude Code assistant:

### Fetch and create from PR
```
/mco-create-tc-from-pr https://github.com/openshift/machine-config-operator/pull/5691#issuecomment-4054832254
```

### Create TC from Jira
```
/mco-create-tc-from-jira https://redhat.atlassian.net/browse/OCPBUGS-74223
```

### Fix bad TC
```bash
python3 .claude/scripts/update_polarion_tc.py OCP-88941 --fix-metadata --version 4.18 --trello-jira OCPBUGS-83830
```

### Fetch TC and generate test
```
/mco-fetch tc OCP-88122
/automate-test OCP-88122 "mco osstream" disruptive
```

### Migrate test from OTP3
```
/migrate-tests mco_ocb.go --dry-run
```

---

## Output Locations

- **Drafts**: `/tmp/test-specs/drafts/pr-*.json`
- **Jira**: `/tmp/test-specs/jira/*.json`
- **Polarion TCs** (txt format): `/tmp/test-specs/OCP-*.txt`
- **Test steps**: `/tmp/test-specs/steps/*.json`

All output goes to `/tmp/` (not in repo).

---

## Architecture Notes

### REST vs SOAP API

**REST API** (Bearer token):
- Fetch test cases
- Create new test cases
- Update fields (title, description, status, custom fields)
- **Limitation**: Can only POST test steps to NEW TCs

**SOAP API** (username/password):
- Update test steps on EXISTING TCs
- Script auto-falls back from REST to SOAP when updating steps

**Why both?** Polarion REST API doesn't support updating test steps on existing TCs. This is a known Polarion limitation.

### Test Step Format for SOAP API

When updating test steps via SOAP, use this JSON format:
```json
[
  {
    "step": "Do something",
    "expected_result": "$ oc get nodes\nNAME   STATUS\nnode1  Ready"
  }
]
```

### Jira Integration

Uses Atlassian MCP tools when available, with fallback to direct REST API v3 (Basic Auth with email:token).

---

## Documentation Files

- **README.md** (this file) - Complete guide
- **TESTING.md** - Test plan for reviewers

---

## Help & Support

**For script help**:
```bash
python3 scripts/<script>.py --help
```

**For Claude Code help**:
```
/help
```

**Issues**: Report at https://github.com/openshift/machine-config-operator/issues

---

## Cleanup

### Remove Obsolete /tmp Files

Scripts output to `/tmp/test-specs/`. To clean up old files from previous runs:

```bash
# Remove old draft files
rm -f /tmp/ocp88122.json /tmp/test.json

# Remove other temp files from early testing
rm -f /tmp/pr5691_comment.txt /tmp/parsing_gaps.md /tmp/ocp88122_analysis.md
```

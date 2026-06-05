# Jira Integration Guide

## Overview

Fetch Jira issues from redhat.atlassian.net and use for Polarion TC creation.

**No MCP required** - uses direct Atlassian REST API.

## Setup

### Get Jira API Token

1. Go to: https://id.atlassian.com/manage-profile/security/api-tokens
2. Create API token
3. Copy token

### Configure .env

```bash
# Edit .env at repo root
JIRA_URL=https://redhat.atlassian.net
JIRA_EMAIL=you@redhat.com
JIRA_TOKEN=your_api_token_here
```

**Note**: Atlassian Cloud requires Basic authentication with email:token credentials.

### Verify

```bash
python3 .claude/scripts/fetch_jira.py MCO-2221
```

## Fetch Jira Issue

### Basic Usage

```bash
python3 .claude/scripts/fetch_jira.py OCPBUGS-74223
```

**Output**:
- `/tmp/test-specs/jira/OCPBUGS-74223.json` (draft format)
- JSON to stdout

### What Gets Fetched

| Field | Jira Source | Draft Field |
|---|---|---|
| Summary | issue.fields.summary | title (with [MCO][KEY] prefix) |
| Description | issue.fields.description | description |
| Fix Versions | issue.fields.fixVersions[0] | version (X.Y from X.Y.Z) |
| Acceptance Criteria | customfield_12311140 | metadata.acceptance_criteria |
| Issue Type | issue.fields.issuetype.name | metadata.jira_type |
| Labels | issue.fields.labels | metadata.labels |

### Draft Format

```json
{
  "title": "[MCO][OCPBUGS-74223] Add support for X feature",
  "description": "This feature adds support for...",
  "component": "Machine Config Operator",
  "sub_team": "MCO",
  "products": "OCP",
  "test_type": "Functional",
  "version": "4.18",
  "trello_jira": "OCPBUGS-74223",
  "test_steps": [],
  "metadata": {
    "source_jira": "OCPBUGS-74223",
    "jira_type": "Bug",
    "labels": ["qe-test-coverage"],
    "acceptance_criteria": "Verify that feature X works correctly..."
  }
}
```

## Use Cases

### Use Case 1: Jira Only

Create TC from Jira metadata, add steps manually later.

```bash
# Fetch
python3 scripts/fetch_jira.py OCPBUGS-74223

# Validate (will warn about empty test_steps)
python3 scripts/validate_tc.py /tmp/test-specs/jira/OCPBUGS-74223.json

# Create (empty steps - add later)
python3 scripts/create_polarion_tc.py /tmp/test-specs/jira/OCPBUGS-74223.json
```

**When to use**:
- Jira has good description and acceptance criteria
- Test steps not yet defined
- Want to create TC placeholder early

### Use Case 2: Jira + GitHub PR (Hybrid)

**Best approach** - Jira metadata + GitHub test steps.

```bash
# Hybrid creation
python3 scripts/create_tc_hybrid.py OCPBUGS-74223 --from-pr 5691 --comment 4054832254
```

**Workflow**:
1. Fetches Jira issue → metadata
2. Fetches GitHub PR comment → test steps
3. Merges drafts
4. Validates
5. Creates complete TC

**When to use**:
- Jira has fix versions and acceptance criteria
- GitHub PR has QE test steps
- Want accurate metadata + executable steps

### Use Case 3: Manual Draft with Jira Reference

```bash
# Fetch Jira for reference
python3 scripts/fetch_jira.py OCPBUGS-74223

# Copy values to manual draft
cat /tmp/test-specs/jira/OCPBUGS-74223.json | jq '.version, .metadata.acceptance_criteria'

# Create manual draft with copied values
vim /tmp/test-specs/drafts/my_tc.json
```

## Hybrid Mode (Jira + PR)

### Why Hybrid?

**Jira strengths**:
- ✓ Accurate fix versions
- ✓ Acceptance criteria
- ✓ Issue type (Bug, Feature, etc.)
- ✓ Labels

**GitHub PR strengths**:
- ✓ Executable test steps
- ✓ Commands with expected output
- ✓ Environment details

**Hybrid = Best of both**

### Hybrid Workflow

```bash
python3 scripts/create_tc_hybrid.py OCPBUGS-74223 --from-pr 5691
```

**Merge logic**:
- Title: `[MCO][OCPBUGS-74223] <Jira summary>`
- Version: From Jira fix versions
- Description: Jira description + acceptance criteria
- Test steps: From GitHub PR comment
- Jira ID: From Jira (obviously)

**Output**: `/tmp/test-specs/drafts/OCPBUGS-74223-pr5691.json`

### Example

**Jira OCPBUGS-74223**:
- Summary: "Add support for feature X"
- Fix Version: 4.18.0
- Acceptance Criteria: "Verify X works in Y scenarios"
- Description: "Feature X allows..."

**GitHub PR 5691 QE Comment**:
```markdown
## Test Steps

- Create MachineConfig with feature X
  ```bash
  oc create -f mc.yaml
  ```

- Verify feature X applied
  ```bash
  $ oc get mc | grep feature-x
  feature-x   3.2.0   15s
  ```
```

**Merged Draft**:
```json
{
  "title": "[MCO][OCPBUGS-74223] Add support for feature X",
  "description": "Feature X allows...\n\nAcceptance Criteria:\nVerify X works in Y scenarios",
  "version": "4.18",
  "trello_jira": "OCPBUGS-74223",
  "test_steps": [
    {
      "step": "Create MachineConfig with feature X",
      "expected_result": "oc create -f mc.yaml"
    },
    {
      "step": "Verify feature X applied",
      "expected_result": "$ oc get mc | grep feature-x\nfeature-x   3.2.0   15s"
    }
  ]
}
```

## Jira API Details

### Authentication

Uses Atlassian REST API with Basic auth (email:token):
```
Authorization: Basic base64(JIRA_EMAIL:JIRA_TOKEN)
```

**Not** Red Hat SSO - this is Atlassian's API token system. Falls back to Bearer token if email is not configured.

### Endpoint

```
GET https://redhat.atlassian.net/rest/api/3/issue/{issue-key}
```

### ADF Format

Jira returns description in ADF (Atlassian Document Format):

```json
{
  "type": "doc",
  "content": [
    {
      "type": "paragraph",
      "content": [
        {"type": "text", "text": "This is a description"}
      ]
    }
  ]
}
```

Script extracts plain text automatically.

### Custom Fields

Acceptance Criteria field name varies by project:
- Try: `customfield_12311140`
- Try: `customfield_12315140`
- Try: `acceptanceCriteria`

Script attempts common field names.

### Fix Versions

Extract X.Y from X.Y.Z:
- Jira: "4.18.0" → Draft: "4.18"
- Jira: "4.22" → Draft: "4.22"

## Integration with Other Tools

### With validate_tc.py

```bash
python3 scripts/fetch_jira.py OCPBUGS-74223
python3 scripts/validate_tc.py /tmp/test-specs/jira/OCPBUGS-74223.json
```

**Expected warnings**:
- "No test steps defined" (unless using hybrid mode)

### With create_polarion_tc.py

```bash
python3 scripts/fetch_jira.py OCPBUGS-74223
python3 scripts/create_polarion_tc.py /tmp/test-specs/jira/OCPBUGS-74223.json
```

Creates TC with metadata, empty test steps.

### With update_polarion_tc.py

```bash
# Create from Jira (empty steps)
python3 scripts/create_polarion_tc.py /tmp/test-specs/jira/OCPBUGS-74223.json
# Returns: OCP-88999

# Add test steps later
python3 scripts/update_polarion_tc.py OCP-88999 \
  --steps-file /tmp/test-specs/steps/feature-x.json
```

## Troubleshooting

### Error: "Authentication failed"

**Fix**:
```bash
# Regenerate token at:
# https://id.atlassian.com/manage-profile/security/api-tokens

# Update .env
JIRA_TOKEN=new_token_here
```

### Error: "Jira issue not found"

**Fix**:
- Verify issue exists: https://redhat.atlassian.net/browse/OCPBUGS-74223
- Check issue key format: `OCPBUGS-74223` (not `ocpbugs-74223`)
- Ensure you have access to the issue

### Warning: "Acceptance Criteria: Not Found"

**Not critical** - means the custom field wasn't found.

**Fix**:
- Check Jira issue has acceptance criteria field
- Field name may vary by project
- Can add manually to draft if needed

### Version not found

**Symptom**: `version` field is empty

**Fix**:
```bash
# Set manually in draft or during creation
python3 scripts/update_polarion_tc.py OCP-88999 --version 4.18
```

## Command Reference

```bash
# Fetch Jira only
python3 scripts/fetch_jira.py OCPBUGS-74223

# Hybrid (Jira + PR)
python3 scripts/create_tc_hybrid.py OCPBUGS-74223 --from-pr 5691

# Via Claude Code
/mco-create-tc-from-jira https://redhat.atlassian.net/browse/OCPBUGS-74223
/mco-create-tc-from-pr https://github.com/openshift/machine-config-operator/pull/5691
```

## Comparison: Sources

| Source | Title/Version | Description | Test Steps |
|---|---|---|---|
| GitHub PR only | From PR (extract Jira) | From PR | ✓ From QE comment |
| Jira only | ✓ From Jira | ✓ From Jira | ✗ Empty |
| Hybrid (Jira+PR) | ✓ From Jira | ✓ From Jira | ✓ From PR |

**Recommendation**: Use hybrid mode for best results.

## Example Workflow

```bash
# 1. Find Jira issue
# https://redhat.atlassian.net/browse/OCPBUGS-74223

# 2. Find PR with QE test
# https://github.com/openshift/machine-config-operator/pull/5691

# 3. Create hybrid TC
python3 .claude/scripts/create_tc_hybrid.py OCPBUGS-74223 \
  --from-pr 5691 \
  --comment 4054832254

# Output:
# [1/5] Fetching Jira issue: OCPBUGS-74223...
#       ✓ Title: [MCO][OCPBUGS-74223] Add support for feature X
#       ✓ Version: 4.18
#
# [2/5] Fetching GitHub PR #5691...
#       ✓ Test Steps: 5
#
# [3/5] Merging Jira + PR data...
#       ✓ Merged draft: /tmp/test-specs/drafts/OCPBUGS-74223-pr5691.json
#
# [4/5] Validating draft...
#       ✓ Validation passed
#
# [5/5] Creating Polarion TC...
#       ✓ Created: OCP-88999
#       URL: https://polarion.engineering.redhat.com/polarion/#/project/OSE/workitem?id=OCP-88999
```

## Limitations

1. **Acceptance Criteria field**: Custom field name varies by project
2. **ADF parsing**: Complex formatting may not convert perfectly
3. **Authentication**: Requires Atlassian API token (not Red Hat SSO)
4. **Access**: Must have access to Jira issue

## Next Steps

After fetching Jira:
1. Review draft metadata
2. If missing test steps, use hybrid mode or add manually
3. Validate and create TC
4. Verify in Polarion

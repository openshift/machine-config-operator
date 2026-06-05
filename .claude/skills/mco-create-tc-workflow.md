---
name: mco-create-tc-workflow
description: Create Polarion test case with validation and confirmation
---

# MCO Create TC Workflow

**CRITICAL RULE**: Always validate → show summary → wait for confirmation before creating.

Never create malformed test cases like `wrong.pdf` (OCP-88941).

## Instructions

When invoked via `/mco-create-tc-from-pr` or `/mco-create-tc-from-jira`:

### Step 1: Parse Source

**From GitHub PR comment** (`#issuecomment-<id>`):
```bash
python3 .claude/scripts/fetch_github_pr.py --repo openshift/machine-config-operator --pr <number> --comment <comment-id>
```
Saves to `/tmp/test-specs/drafts/pr-<number>.json`

**From GitHub PR review** (`#pullrequestreview-<id>`):

The fetch script does NOT cover PR reviews. Fetch directly via GitHub API:
```python
import urllib.request, json

def fetch(url):
    req = urllib.request.Request(url, headers={
        "Accept": "application/vnd.github.v3+json",
        "User-Agent": "mco-tc-script"
    })
    return json.loads(urllib.request.urlopen(req).read())

pr = fetch("https://api.github.com/repos/openshift/machine-config-operator/pulls/<NUMBER>")
reviews = fetch("https://api.github.com/repos/openshift/machine-config-operator/pulls/<NUMBER>/reviews")
review = next(r for r in reviews if r['id'] == <REVIEW_ID>)
```
Then build the draft manually from `pr['title']` and `review['body']`.

**From PR URL only** (no anchor):
Fetch both issue comments and PR reviews, scan for QE verification content.

**From Draft File**:
Load existing JSON file directly.

### Step 2: Extract Test Steps — STRICT RULES

**Rule 1 — Verbatim code blocks**:
Copy every code block from the PR comment EXACTLY as written. Never:
- Abbreviate log lines with `...`
- Summarize or paraphrase output
- Add comments or explanations inside expected results
- Shorten long lines (e.g. MCD log lines like `I0603 05:28:15.146853    2588 update.go:2970] "Starting update from ..."`)

**Rule 2 — No heredoc syntax**:
If original uses `cat <<EOF | oc apply -f -`, convert to file-based pattern:
```
# filename.yaml
<yaml content verbatim>

oc apply -f filename.yaml
<output line>
```
Reason: Polarion splits heredoc content out of the expected result cell and renders it above the table.

**Rule 3 — Step vs Expected Result**:
- **step**: plain-text description of the action
- **expected_result**: verbatim code block content from the comment

### Step 3: Validate Draft

```bash
python3 .claude/scripts/validate_tc.py /tmp/test-specs/drafts/pr-<number>.json
```

**Must pass validation**:
- Title: `^\[MCO\]\[.+\] .+`
- Required fields: component, sub_team, products, test_type, version, trello_jira
- Test steps: expected_result has commands (not narrative-only)

If validation fails:
1. Show errors
2. Offer to fix common issues (missing fields, title format)
3. Re-validate
4. Do NOT proceed to creation until valid

### Step 4: Show Summary

Display:
```
================================================================================
TEST CASE SUMMARY
================================================================================
Title: [MCO][OCPBUGS-86650] Verify password is applied only when hash changes
Component: Machine Config Operator
Sub Team: MCO
Products: OCP
Test Type: Functional
Version: 4.19
Jira: OCPBUGS-86650
Test Steps: 5
================================================================================

Step 1: Apply a MachineConfig with a password hash for the core user
Expected: # 99-worker-core-password-test.yaml
          apiVersion: machineconfiguration.openshift.io/v1
          ...

Step 2: Wait for MCP rollout and verify MCD logs show password configured
Expected: oc get mcp
          ...
          oc logs ... | grep -i pass
          I0603 05:28:15.567308    2588 update.go:2546] Password has been configured
...
================================================================================
```

### Step 5: Request Confirmation

**BLOCKING REQUIREMENT**: Wait for user confirmation before creating.

Ask:
```
Create this test case in Polarion? (yes/no)
```

**Do NOT create** if user says no, asks to modify, or is silent.

### Step 6: Create (Only After Confirmation)

```bash
python3 .claude/scripts/create_polarion_tc.py /tmp/test-specs/drafts/pr-<number>.json
```

Note the TC ID. The initial creation's PATCH for custom fields often fails silently — always proceed to Step 7 regardless.

### Step 7: Always Patch Fields + Steps Separately

> **Known issue**: The creation PATCH for custom fields (Component, Sub Team, Products, Test Type, Version, Trello/Jira) often fails silently. Always run this step regardless of whether creation reported success. Verify each field in the Polarion UI after patching — if any field is still empty, set it manually in Polarion.

Even if creation reports success, always run:
```bash
python3 .claude/scripts/update_polarion_tc.py OCP-XXXXX \
  --component "Machine Config Operator" \
  --sub-team "MCO" \
  --products "OCP" \
  --test-type "Functional" \
  --version "<VERSION>" \
  --trello-jira "<JIRA>" \
  --steps-file /tmp/test-specs/drafts/pr-<number>.json
```

Verify output shows:
- `✓ Updated 6 field(s)` — casecomponent, subteam2, mcoproducts, casetesttype, plannedin, linkedworkitems
- `✓ Added N test steps via REST API`

### Step 8: Show Result

```
================================================================================
✓ TEST CASE CREATED SUCCESSFULLY
================================================================================
ID: OCP-89248
URL: https://polarion.engineering.redhat.com/polarion/#/project/OSE/workitem?id=OCP-89248
================================================================================
```

## Re-uploading Steps (If Steps Already Exist)

When re-uploading steps to an existing TC, the client detects existing steps and falls back to SOAP, which fails with "Not authorized." Fix: delete all steps via REST first.

```python
import sys, time
sys.path.insert(0, '.claude/scripts')

def load_env():
    import os
    env_vars = {}
    if os.path.exists('.env'):
        with open('.env') as f:
            for line in f:
                line = line.strip()
                if line and not line.startswith('#') and '=' in line:
                    k, v = line.split('=', 1)
                    env_vars[k] = v
    return env_vars

env = load_env()
from polarion_client import PolarionClient

client = PolarionClient(
    url=env.get('POLARION_URL', 'https://polarion.engineering.redhat.com'),
    token=env.get('POLARION_TOKEN', ''),
    verify_ssl=True,
    username=env.get('POLARION_USERNAME', ''),
    password=env.get('POLARION_PASSWORD', '')
)

TC_ID = "OCP-XXXXX"

# Loop-delete: Polarion re-indexes IDs after each delete, so fetch fresh each round
for attempt in range(10):
    result = client._make_request("GET", f"projects/OSE/workitems/{TC_ID}/teststeps")
    remaining = result.get("data", [])
    if not remaining:
        break
    for s in remaining:
        sid = s.get('id', '').split('/')[-1]
        client._make_request("DELETE", f"projects/OSE/workitems/{TC_ID}/teststeps/{sid}")
    time.sleep(0.5)
```

Then re-run Step 7 to POST fresh steps via REST.

## Memory Enforcement

Load memory before creating:
- `feedback_polarion_tc.md` — "Always show summary and confirm before creating"
- `feedback_polarion_fields.md` — "Never leave Component/Test Type/Sub Team/Products/Version/Jira empty"

## Error Handling

**PR review URL (`#pullrequestreview-`)**: fetch script will report "Comment not found". Switch to GitHub reviews API manually.

**Heredoc in original comment**: convert to `cat filename.yaml` + `oc apply -f` pattern before writing to draft.

**SOAP fallback fails ("Not authorized")**: existing steps are blocking REST. Run the delete loop, then re-POST.

**Custom fields empty after creation**: run `update_polarion_tc.py` — the initial PATCH silently failed.

**POLARION_TOKEN not configured**: point to setup instructions. Do NOT proceed.

**Partial success** (TC created but steps failed): report TC ID and URL. Note steps need re-upload via delete loop + REST POST.

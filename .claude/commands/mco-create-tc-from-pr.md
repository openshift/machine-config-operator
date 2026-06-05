---
name: mco-create-tc-from-pr
description: Create Polarion test case from GitHub PR comments
---

# Create Polarion TC from PR

## Usage

```
/mco-create-tc-from-pr <PR-URL>
```

Supported URL formats:
- `https://github.com/openshift/machine-config-operator/pull/6097` — scan all comments + reviews
- `https://github.com/openshift/machine-config-operator/pull/6097#issuecomment-XXXXXXXX` — specific issue comment
- `https://github.com/openshift/machine-config-operator/pull/6097#pullrequestreview-XXXXXXXX` — specific PR review

## Instructions

### Step 1: Parse URL and fetch content

Extract PR number and optional anchor from URL.

**If anchor is `#issuecomment-<id>`** — fetch via script:
```bash
python3 .claude/scripts/fetch_github_pr.py \
  --repo openshift/machine-config-operator \
  --pr <NUMBER> --comment <ID>
```

**If anchor is `#pullrequestreview-<id>`** — the script does NOT cover reviews; fetch via GitHub API:
```python
import urllib.request, json

def fetch(url):
    req = urllib.request.Request(url, headers={
        "Accept": "application/vnd.github.v3+json",
        "User-Agent": "mco-tc-script"
    })
    return json.loads(urllib.request.urlopen(req).read())

# PR metadata
pr = fetch("https://api.github.com/repos/openshift/machine-config-operator/pulls/<NUMBER>")

# All reviews — find the one matching the anchor ID
reviews = fetch("https://api.github.com/repos/openshift/machine-config-operator/pulls/<NUMBER>/reviews")
review = next(r for r in reviews if r['id'] == <REVIEW_ID>)
print(review['body'])
```

**If no anchor** — fetch both issue comments and PR reviews, then find the QE verification comment:
```python
# Issue comments
comments = fetch("https://api.github.com/repos/openshift/machine-config-operator/issues/<NUMBER>/comments")

# PR reviews
reviews = fetch("https://api.github.com/repos/openshift/machine-config-operator/pulls/<NUMBER>/reviews")
```
Scan both for QE content (authored by ptalgulk, ptalgulk01, HarshwardhanPatil07, or containing "Pre-merge", "Post Merge", "Steps Performed", "/label qe-approved").

### Step 2: Extract metadata

- **Title**: PR title, strip `[release-X.YY]` prefix if present
- **Jira**: extract `OCPBUGS-XXXXX` or `MCO-XXXX` from title
- **Version**: from target branch (`release-4.19` → `4.19`) or cluster version in comment
- **Defaults**: Component=`Machine Config Operator`, Sub Team=`MCO`, Products=`OCP`, Test Type=`Functional`

### Step 3: Extract test steps — STRICT RULES

**Rule 1 — Verbatim code blocks**: Copy every code block EXACTLY as written in the PR comment. Do NOT:
- Abbreviate log lines with `...`
- Summarize output
- Add comments or explanations inside the expected result
- Shorten long lines

**Rule 2 — No heredoc syntax**: If the original uses `cat <<EOF | oc apply -f -`, replace with:
```
# filename.yaml
<yaml content here>

oc apply -f filename.yaml
<output>
```
Heredoc causes Polarion to split the YAML out of the expected result cell and render it above the table.

**Rule 3 — Step vs Expected Result**:
- **Step** column: plain-text description of the action (what to do)
- **Expected Result** column: the code blocks from the comment, verbatim

### Step 3b: Multiple scenarios — ask before creating

If the QE comment contains multiple distinct test scenarios (e.g. "Scenario 1: ...", "Scenario 2: ...", or clearly separated test flows), **ask the user** whether they want:
- **Separate TCs** — one Polarion TC per scenario (better traceability and granularity)
- **One combined TC** — all scenarios in a single TC

List the detected scenario titles so the user can decide. Then create the appropriate number of draft JSON files (e.g. `pr-<NUMBER>-scenario1.json`, `pr-<NUMBER>-scenario2.json`) and run Steps 4–7 for each.

### Step 4: Create draft JSON

Save to `/tmp/test-specs/drafts/pr-<NUMBER>.json`:
```json
{
  "title": "[MCO][TICKET] Description",
  "description": "...",
  "component": "Machine Config Operator",
  "sub_team": "MCO",
  "products": "OCP",
  "test_type": "Functional",
  "version": "4.19",
  "trello_jira": "OCPBUGS-XXXXX",
  "test_steps": [
    {
      "step": "Action description",
      "expected_result": "exact output from code block"
    }
  ]
}
```

### Step 5: Validate and show summary

```bash
python3 .claude/scripts/validate_tc.py /tmp/test-specs/drafts/pr-<NUMBER>.json
```

Show full summary and **wait for confirmation** before creating.

### Step 6: Create TC

```bash
python3 .claude/scripts/create_polarion_tc.py /tmp/test-specs/drafts/pr-<NUMBER>.json
```

Note the TC ID (e.g. `OCP-89248`). The initial creation's custom field PATCH often fails silently.

### Step 7: Always patch fields + steps separately

Even if creation reports success, always run:
```bash
python3 .claude/scripts/update_polarion_tc.py OCP-XXXXX \
  --component "Machine Config Operator" \
  --sub-team "MCO" \
  --products "OCP" \
  --test-type "Functional" \
  --version "<VERSION>" \
  --trello-jira "<JIRA>" \
  --steps-file /tmp/test-specs/drafts/pr-<NUMBER>.json
```

> **Known issue**: The creation PATCH for custom fields (Component, Sub Team, Products, Test Type, Version, Trello/Jira) often fails silently. Always run Step 7 to patch fields separately and verify them in the Polarion UI. If any field is still missing after patching, set it manually in Polarion.

### Step 8: Re-uploading steps (if steps already exist)

When steps already exist, the client falls back to SOAP which fails with "Not authorized." Must delete all existing steps first:

```python
import sys, time
sys.path.insert(0, '.claude/scripts')
from polarion_client import PolarionClient
# ... init client ...

# Loop-delete until empty (Polarion re-indexes after each delete)
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

### Step 9: Offer to automate

After TC creation and patching is complete, ask the user:

> "TC OCP-XXXXX created successfully. Would you like to automate this test case using `/automate-test`?"

If the user accepts, invoke `/automate-test` with the TC ID.

## Key Points

- **PR reviews** (`#pullrequestreview-`) need the `/pulls/{pr}/reviews` API, not the script
- **Heredoc → file pattern**: never use `<<EOF` in expected results
- **Verbatim logs**: full `I0603 05:28:15...` lines, no `...` abbreviation
- **Always run update_polarion_tc.py** after creation to ensure fields + steps are set
- **Step re-upload**: delete loop first, then REST POST

## Common Issues

**Fields not showing in Polarion** (Trello/Jira, Version, Sub Team empty):
- Creation PATCH often fails silently
- **Always run Step 7** to patch fields separately
- Verify fields in Polarion UI before marking TC complete

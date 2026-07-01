---
name: mco-create-tc-from-jira
description: Create Polarion test case from Jira comment
---

# Create Polarion TC from Jira

## Usage

```
/mco-create-tc-from-jira <JIRA-URL>
```

Supported URL formats:
- `https://redhat.atlassian.net/browse/OCPBUGS-86695` — scan all comments for QE verification
- `https://redhat.atlassian.net/browse/OCPBUGS-86695?focusedCommentId=17167775` — specific comment

## Instructions

### Step 1: Parse URL and fetch content

Extract issue key (e.g., `OCPBUGS-86695`) and optional comment ID from URL.

**Fetch Jira issue:**
```python
# Use Atlassian MCP tools
from mcp_tools import mcp__atlassian__getJiraIssue

issue = mcp__atlassian__getJiraIssue(
    cloudId="redhat.atlassian.net",
    issueIdOrKey="OCPBUGS-86695"
)
```

**If comment ID provided** — fetch specific comment:
```python
# Jira API doesn't have direct comment-by-id fetch via MCP
# Need to get all comments and filter
import requests
import os

jira_email = os.getenv('JIRA_EMAIL')
jira_token = os.getenv('JIRA_TOKEN')

url = f"https://redhat.atlassian.net/rest/api/3/issue/OCPBUGS-86695/comment/17167775"
response = requests.get(
    url,
    auth=(jira_email, jira_token),
    headers={"Accept": "application/json"}
)
comment = response.json()
```

**If no comment ID** — fetch all comments and find QE verification:
```python
# Look for comments containing:
# - "Pre-merge verification"
# - "Post Merge"
# - "Steps Performed"
# - Author: ptalgulk@redhat.com, hpatil@redhat.com
```

### Step 2: Extract metadata

- **Title**: From Jira summary, ensure `[MCO][TICKET-ID]` format (add if missing)
- **Jira**: Issue key from URL (e.g., `OCPBUGS-86695`)
- **Version**: From Jira "Fix Version" field or comment text
- **Description**: From Jira description or comment summary
- **Defaults**: Component=`Machine Config Operator`, Sub Team=`MCO`, Products=`OCP`, Test Type=`Functional`

### Step 3: Extract test steps — STRICT RULES

**Rule 1 — Verbatim code blocks**: Copy every code block EXACTLY as written. Do NOT:
- Abbreviate log lines with `...`
- Summarize output
- Add explanations
- Shorten long lines

**Rule 2 — No heredoc syntax**: Replace `cat <<EOF | oc apply -f -` with:
```
# filename.yaml
<yaml content>

oc apply -f filename.yaml
<output>
```

**Rule 3 — Step vs Expected Result**:
- **Step** column: plain-text description of action
- **Expected Result** column: code blocks verbatim

### Step 4: Create draft JSON

Save to `/tmp/test-specs/drafts/jira-<ISSUE-KEY>.json`:
```json
{
  "title": "[MCO][OCPBUGS-86695] Description",
  "description": "...",
  "component": "Machine Config Operator",
  "sub_team": "MCO",
  "products": "OCP",
  "test_type": "Functional",
  "version": "4.19",
  "trello_jira": "OCPBUGS-86695",
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
python3 .claude/scripts/validate_tc.py /tmp/test-specs/drafts/jira-OCPBUGS-86695.json
```

Show full summary and **wait for confirmation** before creating.

### Step 6: Create TC

```bash
python3 .claude/scripts/create_polarion_tc.py /tmp/test-specs/drafts/jira-OCPBUGS-86695.json
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
  --steps-file /tmp/test-specs/drafts/jira-<JIRA-KEY>.json
```

> **Known issue**: The creation PATCH for custom fields (Component, Sub Team, Products, Test Type, Version, Trello/Jira) often fails silently. Always run Step 7 to patch fields separately and verify them in the Polarion UI. If any field is still missing after patching, set it manually in Polarion.

### Step 8: Offer to automate

After TC creation and patching is complete, ask the user:

> "TC OCP-XXXXX created successfully. Would you like to automate this test case using `/automate-test`?"

If the user accepts, invoke `/automate-test` with the TC ID.

## Key Points

- Use Atlassian MCP tools or direct Jira REST API
- Comment format same as GitHub (code blocks, plain text)
- Same draft JSON structure
- Same create + update workflow
- **Verbatim logs**: full lines, no abbreviation
- **Heredoc → file pattern**: never use `<<EOF`
- **Always run update_polarion_tc.py** after creation — creation PATCH fails silently; fields may be empty in Polarion even if the script reported success


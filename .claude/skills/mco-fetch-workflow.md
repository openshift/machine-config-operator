---
name: mco-fetch-workflow
description: Fetch test artifacts from Polarion, GitHub PR, or Jira
---

# MCO Fetch Workflow

Thin wrapper around fetch scripts. Delegates to Python scripts in `.claude/scripts/`.

## Instructions

When invoked with `/mco-fetch <type> <id>`:

1. **Parse arguments**:
   - `tc <id>` → Polarion test case
   - `pr <number> [comment-id]` → GitHub PR comment
   - `jira <key>` → Jira issue

2. **Fetch Polarion test case**:
   ```bash
   python3 .claude/scripts/fetch_polarion.py <id>
   ```
   - Outputs JSON to stdout
   - Saves `/tmp/test-specs/<id>.txt` for /automate-test

3. **Fetch GitHub PR**:
   ```bash
   python3 .claude/scripts/fetch_github_pr.py --repo openshift/machine-config-operator --pr <number> [--comment <id>]
   ```
   - Parses QE pre-merge testing comment
   - Saves `/tmp/test-specs/drafts/pr-<number>.json`

4. **Fetch Jira issue**:
   ```bash
   python3 .claude/scripts/fetch_jira.py <issue-key>
   ```
   - Fetches metadata and acceptance criteria
   - Saves `/tmp/test-specs/jira/<issue-key>.json`

5. **Show summary**:
   - Display title, status
   - Show output file location
   - List test steps count (if TC)

## Example

```
/mco-fetch tc OCP-88122
```

**Expected output**:
```
Fetching OCP-88122 from Polarion...

✓ Test Case: OCP-88122
  Title: [MCO][MCO-2136] Validate osImageStream inheritance for custom MachineConfigPools
  Status: draft
  Component: Machine Config Operator
  Version: 4.22
  Test Steps: 6

Output saved:
- /tmp/test-specs/OCP-88122.txt (for /automate-test)
- JSON output in conversation
```

## Error Handling

- Check POLARION_TOKEN is configured
- If auth fails → point to setup instructions
- If TC not found → verify ID format

## Implementation

See `.claude/README.md` section "Workflows" for full documentation.

Script source of truth: `.claude/scripts/fetch_polarion.py`, `fetch_github_pr.py`, `fetch_jira.py`

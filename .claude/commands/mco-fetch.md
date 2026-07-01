---
name: mco-fetch
description: Fetch Polarion test case, GitHub PR, or Jira issue
skill: mco-fetch-workflow
---

# mco-fetch

Fetch test artifacts from Polarion, GitHub PR, or Jira.

## Usage

```
/mco-fetch tc OCP-88122
/mco-fetch pr 5691 [comment-id]
/mco-fetch jira OCPBUGS-74223
```

## What it does

- **Polarion TC**: Fetches test case details and saves to `/tmp/test-specs/<id>.txt` (for /automate-test)
- **GitHub PR**: Fetches PR QE pre-merge testing comment, parses test steps
- **Jira Issue**: Fetches issue metadata (summary, description, fix versions, acceptance criteria)

## Direct Script Usage

### Polarion
```bash
python3 .claude/scripts/fetch_polarion.py OCP-88122
```

### GitHub PR
```bash
python3 .claude/scripts/fetch_github_pr.py --repo openshift/machine-config-operator --pr 5691 [--comment <id>]
```

### Jira
```bash
python3 .claude/scripts/fetch_jira.py OCPBUGS-74223
```

## Output

- **Polarion**: JSON to stdout + `/tmp/test-specs/<id>.txt` (for /automate-test)
- **GitHub PR**: `/tmp/test-specs/drafts/pr-<number>.json`
- **Jira**: `/tmp/test-specs/jira/<issue-key>.json`

## See Also

- `.claude/README.md` - Full workflow documentation
- `.claude/skills/mco-fetch-workflow.md` - Implementation details

# MCO QE Tooling - Quick Start

Get started in 5 minutes.

## 1. Setup (One-time)

```bash
cd .claude
./scripts/setup.sh
```

## 2. Configure Tokens

Edit `.env` at repo root:

```bash
# Required
POLARION_TOKEN=your_token_here

# Optional (for GitHub)
GITHUB_TOKEN=your_github_token

# Optional (for Jira)
JIRA_TOKEN=your_jira_token

# Optional (for updating test steps)
POLARION_USERNAME=your_username
POLARION_PASSWORD=your_password
```

**Get tokens**:
- Polarion: https://polarion.engineering.redhat.com/polarion/#/user/me
- GitHub: https://github.com/settings/tokens
- Jira: https://id.atlassian.com/manage-profile/security/api-tokens

## 3. Verify

```bash
cd .claude
./scripts/setup.sh --verify
```

Should see:
```
✓ Python 3.14.5 found
✓ requests already installed
✓ POLARION_TOKEN configured
✓ Polarion connection successful (HTTP 200)
  Project: OpenShift (OSE)
```

## 4. Try It Out

### Create TC from GitHub PR

```bash
# Fetch PR comment
python3 scripts/fetch_github_pr.py \
  --repo openshift/machine-config-operator \
  --pr 5691 \
  --comment 4054832254

# Validate
python3 scripts/validate_tc.py /tmp/test-specs/drafts/pr-5691.json

# Create (dry run first)
python3 scripts/create_polarion_tc.py /tmp/test-specs/drafts/pr-5691.json --dry-run

# Create (for real)
python3 scripts/create_polarion_tc.py /tmp/test-specs/drafts/pr-5691.json
```

### Create TC from Jira + GitHub (Hybrid)

```bash
# One command - combines Jira metadata + PR test steps
python3 scripts/create_tc_hybrid.py OCPBUGS-74223 --from-pr 5691
```

### Fix Existing TC (wrong.pdf scenario)

```bash
# Fix empty metadata fields
python3 scripts/update_polarion_tc.py OCP-88941 --fix-metadata \
  --version 4.18 \
  --trello-jira OCPBUGS-83830

# Fix title
python3 scripts/update_polarion_tc.py OCP-88941 \
  --title "[MCO][OCPBUGS-83830] Verify MCD password behavior"
```

### Fetch Polarion TC for Automation

```bash
# Fetch TC
python3 scripts/fetch_polarion.py OCP-88122

# Use with /automate-test
/automate-test OCP-88122 "mco osstream" disruptive
```

## Common Workflows

### Workflow 1: PR → Polarion

1. Find PR with QE comment
2. Get PR number and comment ID
3. Run: `python3 scripts/create_tc_hybrid.py JIRA-KEY --from-pr PR-NUM`
4. Get OCP-xxxxx URL

### Workflow 2: Jira Only

1. Find Jira issue
2. Run: `python3 scripts/fetch_jira.py JIRA-KEY`
3. Create: `python3 scripts/create_polarion_tc.py /tmp/test-specs/jira/JIRA-KEY.json`
4. Add steps later

### Workflow 3: Fix Bad TC

1. Identify TC with empty fields
2. Run: `python3 scripts/update_polarion_tc.py OCP-ID --fix-metadata`
3. Verify in Polarion

## File Locations

- **Config**: `.env` (repo root)
- **Scripts**: `.claude/scripts/`
- **Output**: `/tmp/test-specs/`
- **Docs**: `.claude/*.md`

## Quick Reference

| Task | Command |
|---|---|
| Setup | `./scripts/setup.sh --verify` |
| Fetch TC | `python3 scripts/fetch_polarion.py OCP-88122` |
| Fetch PR | `python3 scripts/fetch_github_pr.py --repo org/repo --pr NUM` |
| Fetch Jira | `python3 scripts/fetch_jira.py JIRA-KEY` |
| Validate | `python3 scripts/validate_tc.py draft.json` |
| Create | `python3 scripts/create_polarion_tc.py draft.json` |
| Hybrid | `python3 scripts/create_tc_hybrid.py JIRA-KEY --from-pr NUM` |
| Update | `python3 scripts/update_polarion_tc.py OCP-ID --fix-metadata` |

## Next Steps

1. Read `.claude/README.md` for full workflows
2. Try pilot test: `.claude/PILOT_PR5691.md`
3. See complete guide: `.claude/COMPLETE_WORKFLOW.md`

## Troubleshooting

**"POLARION_TOKEN not configured"**
→ Add token to `.env`, run `./scripts/setup.sh --verify`

**"Validation failed"**
→ Review errors, edit draft, re-validate

**"SOAP API requires username and password"**
→ Add POLARION_USERNAME/PASSWORD to `.env` (only for test step updates)

## Help

- Full docs: `.claude/README.md`
- Fetch guide: `.claude/FETCH_USAGE.md`
- Create guide: `.claude/CREATE_TC_USAGE.md`
- Update guide: `.claude/UPDATE_TC_USAGE.md`
- Jira guide: `.claude/JIRA_INTEGRATION.md`
- Complete workflow: `.claude/COMPLETE_WORKFLOW.md`
- Final summary: `.claude/FINAL_SUMMARY.md`

# Test Plan for Reviewers

## Pre-Review Checklist

### Security & Secrets
- [ ] Verify `.env` is in `.gitignore`
- [ ] Verify `.claude/.env` is in `.gitignore`
- [ ] Verify `.claude/settings.local.json` is in `.gitignore`
- [ ] No secrets or tokens in committed code
- [ ] `.env.example` uses placeholder values only

### Documentation
- [ ] README.md is accurate (no references to non-existent scripts)
- [ ] QUICKSTART.md provides working 5-minute setup
- [ ] All script paths in docs match actual file locations
- [ ] Examples use real script names (not `polarion_cli.py`, etc.)

### Scripts Functionality
- [ ] All `.py` scripts are executable (`chmod +x`)
- [ ] All scripts have valid shebang: `#!/usr/bin/env python3`
- [ ] `--help` flag works for all scripts (except `polarion_client.py`)
- [ ] Scripts gracefully handle missing `.env` file

## Functional Testing (Optional - Requires Credentials)

**Note**: These tests require Polarion/GitHub/Jira credentials. Reviewers may skip this section.

### Setup Verification
```bash
cd .claude
./scripts/setup.sh --verify
```
**Expected**: Shows Python version, checks dependencies, verifies `/tmp/test-specs/` structure

### Validation (No Credentials Required)
```bash
# Use sample from docs
cat > /tmp/test.json << 'EOF'
{
  "title": "[MCO][OCPBUGS-12345] Test title",
  "component": "Machine Config Operator",
  "sub_team": "MCO",
  "products": "OCP",
  "test_type": "Functional",
  "version": "4.22",
  "trello_jira": "OCPBUGS-12345",
  "test_steps": [
    {"step": "Do something", "expected_result": "oc get nodes"}
  ]
}
EOF

python3 scripts/validate_tc.py /tmp/test.json
```
**Expected**: `✓ VALIDATION PASSED`

### Fetch Polarion (Requires POLARION_TOKEN)
```bash
python3 scripts/fetch_polarion.py OCP-88122
```
**Expected**:
- JSON output to stdout
- File created: `/tmp/test-specs/OCP-88122.txt`
- No errors about missing token (if configured)

### Fetch GitHub PR (No Token Required - Public Repo)
```bash
python3 scripts/fetch_github_pr.py \
  --repo openshift/machine-config-operator \
  --pr 5691 \
  --comment 4054832254
```
**Expected**:
- Draft saved: `/tmp/test-specs/drafts/pr-5691.json`
- Title extracted with `[MCO][JIRA-ID]` prefix
- Version extracted from environment section
- Test steps parsed

### Create Test Case (Requires POLARION_TOKEN)
```bash
# Dry run - no actual creation
python3 scripts/create_polarion_tc.py \
  /tmp/test-specs/drafts/pr-5691.json \
  --dry-run
```
**Expected**: Summary shown, no actual Polarion API call

## Code Structure Review

### Python Code Quality
- [ ] No hardcoded credentials
- [ ] Environment variables loaded from `.env`
- [ ] Error messages are helpful (show next steps)
- [ ] Scripts can run from any directory (use `__file__` for relative paths)

### File Organization
- [ ] Scripts in `.claude/scripts/`
- [ ] Commands in `.claude/commands/`
- [ ] Skills in `.claude/skills/`
- [ ] Docs in `.claude/` root
- [ ] No orphaned files

### Integration Points
- [ ] Does not modify MCO production code
- [ ] Does not modify existing tests
- [ ] Only adds files in `.claude/` directory
- [ ] Output goes to `/tmp/test-specs/` (not in repo)

## Documentation Accuracy

### README.md Verification
- [ ] "Setup" section has correct script paths
- [ ] "Workflows" section references real scripts
- [ ] "Command Reference" table has valid commands
- [ ] No broken internal links
- [ ] Examples are copy-pasteable

### QUICKSTART.md Verification
- [ ] Setup steps work end-to-end
- [ ] Script paths are correct
- [ ] Example commands succeed (or fail gracefully without credentials)

## Edge Cases

### Missing Dependencies
```bash
# Simulate missing requests library
python3 -c "import sys; sys.path = [p for p in sys.path if 'site-packages' not in p]; exec(open('.claude/scripts/fetch_polarion.py').read())"
```
**Expected**: Clear error about missing `requests` library

### Invalid Input
```bash
# Invalid TC ID format
python3 scripts/fetch_polarion.py INVALID-FORMAT
```
**Expected**: Error message with valid format example

### No .env File
```bash
# Run without .env
mv .env .env.bak 2>/dev/null
python3 scripts/fetch_polarion.py OCP-88122
```
**Expected**: "Error: POLARION_TOKEN not configured in .env"

## Reviewer Quick Test (2 minutes)

**Minimal verification without credentials**:

1. Check gitignore:
   ```bash
   grep -E "\.env|settings.local.json" .gitignore
   ```

2. Run setup verification:
   ```bash
   cd .claude && ./scripts/setup.sh --verify
   ```

3. Test validation:
   ```bash
   echo '{"title":"[MCO][TEST-1] Test","component":"Machine Config Operator","sub_team":"MCO","products":"OCP","test_type":"Functional","version":"4.22","trello_jira":"TEST-1","test_steps":[{"step":"test","expected_result":"oc get nodes"}]}' | python3 scripts/validate_tc.py -
   ```

4. Check --help works:
   ```bash
   python3 scripts/fetch_polarion.py --help
   python3 scripts/create_polarion_tc.py --help
   ```

**All 4 should complete without errors.**

## Success Criteria

- [ ] No secrets in committed files
- [ ] Scripts are executable and have --help
- [ ] Documentation is accurate (no phantom scripts)
- [ ] Quick test passes (4 commands above)
- [ ] Only `.claude/` directory added (no changes to MCO code)
- [ ] `.gitignore` protects secrets

## Known Limitations

- **Polarion REST API limitation**: Cannot update test steps on existing TCs via REST. Script auto-falls back to SOAP API (requires username/password).
- **GitHub rate limits**: Public repos work without token but have lower rate limits (60 req/hour).
- **Jira authentication**: Requires email + API token for Atlassian Cloud (Basic Auth).

## Future Enhancements

- Add unit tests for parsers
- Add integration tests (mock Polarion API)
- Add CI workflow to verify scripts on PR
- Add more robust HTML→text conversion
- Support Polarion rich text formatting in test steps

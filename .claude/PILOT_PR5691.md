# Pilot Test: PR 5691 → Create Polarion TC

## Objective

Test full workflow: GitHub PR QE comment → validate → create Polarion TC.

**Target PR**: openshift/machine-config-operator#5691  
**Comment ID**: 4054832254 (QE pre-merge testing)

## Prerequisites

1. POLARION_TOKEN configured in `.env`
2. GITHUB_TOKEN configured (optional, for rate limits)
3. Setup verified: `./scripts/setup.sh --verify`

## Workflow Steps

### Step 1: Fetch PR Comment

```bash
cd .claude
python3 scripts/fetch_github_pr.py \
  --repo openshift/machine-config-operator \
  --pr 5691 \
  --comment 4054832254
```

**Expected**:
- ✓ Draft saved to: `/tmp/test-specs/drafts/pr-5691.json`
- Title extracted from PR (with `[MCO][JIRA-ID]` prefix if Jira found)
- Environment parsed (version, platform)
- Test steps parsed (bullets → step, code → expected_result)

**Verify**:
```bash
cat /tmp/test-specs/drafts/pr-5691.json | jq '.title, .trello_jira, .version, (.test_steps | length)'
```

### Step 2: Validate Draft

```bash
python3 scripts/validate_tc.py /tmp/test-specs/drafts/pr-5691.json
```

**Expected**:
- ✓ VALIDATION PASSED (or specific errors to fix)
- Title matches `^\[MCO\]\[.+\] .+`
- All required fields present
- Test steps have commands (not narrative-only)

**If validation fails**:
1. Review errors
2. Edit `/tmp/test-specs/drafts/pr-5691.json`
3. Re-validate
4. Do NOT proceed until valid

### Step 3: Review Draft

```bash
cat /tmp/test-specs/drafts/pr-5691.json | jq '.'
```

**Check**:
- [ ] Title: `[MCO][JIRA-ID] ...`
- [ ] Component: `Machine Config Operator`
- [ ] Sub Team: `MCO`
- [ ] Products: `OCP`
- [ ] Test Type: `Functional` (or appropriate)
- [ ] Version: (from PR comment or set manually)
- [ ] Trello/Jira: (from PR title)
- [ ] Test Steps: Each step has:
  - Non-empty `step` field
  - `expected_result` with commands/YAML

### Step 4: Dry Run

```bash
python3 scripts/create_polarion_tc.py /tmp/test-specs/drafts/pr-5691.json --dry-run
```

**Expected**:
```
Validating draft...
✓ Validation passed

================================================================================
TEST CASE SUMMARY
================================================================================
Title: [MCO][OCPBUGS-xxxxx] ...
Component: Machine Config Operator
Sub Team: MCO
Products: OCP
Test Type: Functional
Version: 4.22
Jira: OCPBUGS-xxxxx
Test Steps: 5
================================================================================

✓ DRY RUN: Draft is valid and ready for creation
```

### Step 5: Create Test Case

```bash
python3 scripts/create_polarion_tc.py /tmp/test-specs/drafts/pr-5691.json
```

**Expected workflow**:
1. Creating test case...
   ✓ Created: OCP-xxxxx

2. Patching custom fields...
   ✓ Patched 6 custom fields

3. Adding X test steps...
   ✓ Added X test steps

**Expected output**:
```
================================================================================
✓ TEST CASE CREATED SUCCESSFULLY
================================================================================
ID: OCP-xxxxx
URL: https://polarion.engineering.redhat.com/polarion/#/project/OSE/workitem?id=OCP-xxxxx
================================================================================
```

### Step 6: Verify in Polarion

1. Open URL: `https://polarion.engineering.redhat.com/polarion/#/project/OSE/workitem?id=OCP-xxxxx`

2. **Verify metadata**:
   - [ ] Title: `[MCO][JIRA-ID] ...`
   - [ ] Status: Draft
   - [ ] Component: Machine Config Operator
   - [ ] Sub Team: MCO
   - [ ] Products: OCP
   - [ ] Test Type: Functional
   - [ ] Version: 4.22
   - [ ] Trello/Jira: OCPBUGS-xxxxx

3. **Verify test steps**:
   - [ ] All steps present
   - [ ] Step column has action descriptions
   - [ ] Expected Result column has commands/output
   - [ ] No HTML tags visible
   - [ ] YAML/code formatting preserved

4. **Compare with good format** (OCP-88122.pdf):
   - [ ] Title structure matches
   - [ ] All required fields filled
   - [ ] Test steps have real commands

## Expected Challenges

### Challenge 1: Jira ID Not Found

**Symptom**: `trello_jira` field empty

**Fix**:
```bash
# Edit draft
vim /tmp/test-specs/drafts/pr-5691.json
# Set: "trello_jira": "OCPBUGS-xxxxx"

# Re-validate
python3 scripts/validate_tc.py /tmp/test-specs/drafts/pr-5691.json
```

### Challenge 2: Version Not Parsed

**Symptom**: `version` field empty

**Fix**:
```bash
# Edit draft
vim /tmp/test-specs/drafts/pr-5691.json
# Set: "version": "4.22"

# Re-validate
```

### Challenge 3: Test Steps Narrative-Only

**Symptom**: Validation warns "Expected result appears to be narrative-only"

**Fix**:
```bash
# Edit draft expected_result to include commands
# Before: "The feature is applied"
# After: "$ oc get mc | grep feature\nfeature   3.2.0   15s"
```

### Challenge 4: API Error During Creation

**Symptom**: "Failed to create test case: ..."

**Fix**:
1. Check POLARION_TOKEN validity
2. Verify network connection
3. Check Polarion API status
4. Review error message for field issues
5. Draft file preserved - can retry after fix

## Success Criteria

- [ ] Draft fetched from PR comment
- [ ] Validation passes
- [ ] All required fields populated
- [ ] Test steps have commands
- [ ] Dry run shows correct summary
- [ ] Test case created successfully
- [ ] OCP-xxxxx URL returned
- [ ] Polarion shows complete TC
- [ ] Format matches OCP-88122.pdf (good)
- [ ] No empty fields like wrong.pdf (OCP-88941)

## Comparison Matrix

| Field | wrong.pdf (bad) | PR 5691 (expected good) |
|---|---|---|
| Title prefix | ❌ Missing | ✓ `[MCO][JIRA-ID]` |
| Component | ❌ Empty | ✓ Machine Config Operator |
| Sub Team | ❌ Empty | ✓ MCO |
| Products | ❌ Empty | ✓ OCP |
| Test Type | ❌ Empty | ✓ Functional |
| Version | ❌ Empty | ✓ 4.22 |
| Trello/Jira | ❌ Empty | ✓ OCPBUGS-xxxxx |
| Expected Results | ❌ Narrative | ✓ Commands |

## Next Steps After Pilot

If pilot succeeds:
1. Document actual PR 5691 comment format
2. Update fetch_github_pr.py parsing if needed
3. Add more test cases from different PR formats
4. Integrate into /mco-create-tc-from-pr command
5. Add to CI/automation workflow

## Notes

- Comment ID 4054832254 format may vary from typical QE comments
- Parser may need adjustment based on actual comment structure
- Jira ID extraction regex: `(OCPBUGS-\d+|MCO-\d+)`
- Environment parsing regex: flexible for variations

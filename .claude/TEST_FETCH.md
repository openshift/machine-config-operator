# Test: Fetch OCP-88122

Verify `fetch_polarion.py` against PDF saved from Polarion.

## Test Run: 2026-06-04

**Status**: ⚠️ CANNOT RUN - POLARION_TOKEN not configured  
**Action Required**: User must add real token to `.env`

### Pre-Flight Check
- [x] `.env` exists at repo root
- [x] `.env` has POLARION_TOKEN line
- [ ] **FAIL**: Token is placeholder value `your_personal_access_token_here`

**Blocker**: Cannot fetch from Polarion API without valid token.

## Parsing Gaps Identified (Code Review vs PDF)

**Reference**: OCP-88122.pdf in repo root

### ❌ Gap 1: HTML to Text Conversion
**File**: fetch_polarion.py:98-114  
**Issue**: Basic regex doesn't preserve code blocks or YAML structure  
**Impact**: Test steps with `<< EOF` heredocs will lose formatting  
**Fix Applied**: ✅ Enhanced html_to_text() to preserve `<pre>`, `<code>`, lists, paragraphs

### ❌ Gap 2: Test Steps HTML Content
**File**: fetch_polarion.py:80-93  
**Issue**: Extracted values not passed through html_to_text()  
**Impact**: HTML tags in "Expected Result" column remain raw  
**Fix Applied**: ✅ Call html_to_text() on both step and expected_result values

### ❌ Gap 3: Linked Work Items Parsing  
**File**: fetch_polarion.py:130-145 (custom fields)  
**Issue**: `linkedworkitems` may return array of objects, need to extract ID  
**Impact**: Trello/Jira field shows object instead of "MCO-2136"  
**Fix Applied**: ✅ Extract work item ID/key from object array, join multiple with comma

### ❌ Gap 4: Version Field Array Handling
**File**: fetch_polarion.py:130-145  
**Issue**: `plannedin` may be array `['4.22', '4.23']`  
**Impact**: Version field shows array instead of single value  
**Fix Applied**: ✅ Take first version from array

## Expected Results (from PDF)

### Title
Expected: `[MCO][MCO-2136] Validate osImageStream inheritance for custom MachineConfigPools`

### Metadata (6 required fields)
- Component: Machine Config Operator
- Sub Team: MCO
- Products: OCP
- Test Type: Functional
- Version: 4.22
- Trello/Jira: MCO-2136

### Test Steps Structure
**PDF shows**: Table with "Step" and "Expected Result" columns

**Case1 Expected Result contains**:
```
oc create -f - << EOF
apiVersion: machineconfiguration.openshift.io/v1
kind: MachineConfigPool
metadata:
  name: rhel9
spec:
  machineConfigSelector:
    matchExpressions:
    - {key: machineconfiguration.openshift.io/role, operator: In, values: [worker,rhel9]}
  nodeSelector:
    matchLabels:
      node-role.kubernetes.io/rhel9: ""
EOF
```

Plus commands:
- `oc get mcp rhel9 -ojsonpath='{.status.osImageStream.name}'`
- `oc debug node/ip-10-0-13-255.us-east-2.compute.internal -- chroot /host`
- `rpm-ostree status` output showing version

### Description
Multi-paragraph text with:
- Case1: Worker MCP on rhel-9, custom MCPs inherit/override
- Case2: Patch worker to rhel-10, infra pool inherits
- Case3: Worker on rhel-10, custom MCPs with different streams

## Verification Checklist (Post-Fetch)

**Once token is configured, verify**:

### Title
- [ ] Matches `[MCO][MCO-2136] Validate osImageStream...`

### Metadata (all 6 fields present)
- [ ] Component: Machine Config Operator
- [ ] Sub Team: MCO
- [ ] Products: OCP
- [ ] Test Type: Functional
- [ ] Version: 4.22 (not array, single value)
- [ ] Trello/Jira: MCO-2136 (not object, just key)

### Test Steps Quality
- [ ] Step 1 contains: "Applied below custom MCP..."
- [ ] Expected Result 1 contains: `oc create -f - << EOF`
- [ ] Expected Result 1 contains: full MachineConfigPool YAML (readable, not HTML-mangled)
- [ ] Expected Result contains: `oc get mcp rhel9 -ojsonpath='{.status.osImageStream.name}'`
- [ ] Expected Result contains: `oc debug node/...` command
- [ ] Multi-line commands preserve newlines
- [ ] YAML indentation preserved
- [ ] No HTML tags like `<br/>` or `<div>` in output

### Description
- [ ] Contains "Case1:" heading
- [ ] Contains "Case2:" section  
- [ ] Contains "Case3" section
- [ ] Multi-paragraph structure preserved (not flat wall of text)
- [ ] Bullet points converted to `- ` prefix

### Output Files
- [ ] `/tmp/ocp88122.json` has valid JSON
- [ ] `/tmp/test-specs/OCP-88122.txt` exists
- [ ] `.txt` format suitable for /automate-test command

## Run Test Commands

```bash
# From machine-config-operator repo root
cd "$(git rev-parse --show-toplevel)"

# 1. Verify setup
./.claude/scripts/setup.sh --verify

# 2. Fetch test case (requires valid token in .env)
python3 .claude/scripts/fetch_polarion.py OCP-88122 | tee /tmp/ocp88122.json

# 3. Check text output
cat /tmp/test-specs/OCP-88122.txt

# 4. Compare to PDF
# Manual review: check title, all 6 fields, test steps formatting
```

## Fixes Applied

**File**: `.claude/scripts/fetch_polarion.py`

1. ✅ **Enhanced html_to_text()** (line 98):
   - Preserve `<pre>` and `<code>` blocks
   - Convert `<p>` to double newlines
   - Convert `<li>` to bullet points
   - Clean up max 2 consecutive newlines

2. ✅ **Apply html_to_text to test steps** (line 80-93):
   - Call html_to_text() on step value
   - Call html_to_text() on expected_result value

3. ✅ **Fix linkedworkitems parsing** (line 130):
   - Extract work item ID/key from object array
   - Join multiple with comma
   - Return string, not object

4. ✅ **Fix version array handling** (line 130):
   - Take first version if array
   - Return single value "4.22" not ["4.22"]

## Status Summary

**Code Review**: ✅ COMPLETE - 4 parsing gaps identified and fixed  
**Live Test**: ⚠️ BLOCKED - waiting for valid POLARION_TOKEN

**Next Step**: User must configure real token in `.env`, then re-run test to verify fixes work with live Polarion API.

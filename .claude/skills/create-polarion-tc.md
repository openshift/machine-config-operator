---
name: Create Polarion Test Case
description: Create Polarion test cases from GitHub PR comments or Jira issues with full field support
---

# Create Polarion Test Case Skill

## 🚨 QUICK REFERENCE - READ FIRST 🚨

**Three Non-Negotiable Rules:**

1. **Title = `[MCO][JIRA-ID] Description`**
   - ✅ `[MCO][OCPBUGS-83830] MCD applies password...`
   - ❌ `Verify password updates...` ← WRONG (OCP-88942, OCP-88944 had this error)

2. **Expected Result = Code from `<pre>` blocks**
   - ✅ `cat <<EOF | oc apply -f -\napiVersion: v1...` ← Actual code from PR
   - ❌ `"MachineConfig is created successfully"` ← WRONG (OCP-88944 had this error)

3. **All 8 Required Fields Must Be Set:**
   - Component, Test Type, Sub Team, Products, Subtype 1, Subtype 2, Trello/Jira, Version
   - ❌ Empty fields ← WRONG (OCP-88942, OCP-88944 had this error)

**If you violate ANY of these three rules, the TC will be malformed.**

---

Create Polarion test cases using a standalone script - no MCP server modifications needed.

## Architecture

Uses `.claude/scripts/create_polarion_tc.py` which:
- Lives entirely in MCO repo
- Uses direct Polarion REST API
- Supports all custom fields
- No external dependencies

## ⚠️ CRITICAL REQUIREMENTS - READ THIS FIRST

**STOP! Before doing ANYTHING, validate you understand these rules:**

**1. TITLE FORMAT IS MANDATORY:**
   - ✅ CORRECT: `[MCO][OCPBUGS-83830] Description`
   - ❌ WRONG: `Description without prefix`
   - **If you're about to create a title without [MCO][JIRA-ID], STOP and fix it first**

**2. EXPECTED RESULT = ACTUAL CODE FROM `<pre>` BLOCKS:**
   - ✅ CORRECT: Extract content from `<pre>` tags in PR comment (YAML, commands, outputs)
   - ❌ WRONG: Write your own description like "MCP is created successfully"
   - **If you're about to write a description, STOP and extract the actual `<pre>` block instead**

**3. ALL FIELDS MUST BE SET:**
   - Component: "Machine Config Operator" (not empty)
   - Sub Team: "MCO" (not empty)
   - Products: "OCP" (not empty)
   - Test Type: "Functional" (not empty)
   - Subtype 1: "-" (not empty)
   - Subtype 2: "-" (not empty)
   - Trello/Jira: Jira ID (not empty)
   - Version: Version number (not empty)
   - **If ANY field will be empty, STOP and set it first**

**4. ASK ALL QUESTIONS** - Never skip required questions regardless of model or input source
**5. SHOW CONFIRMATION** - Always display complete summary and get user approval before creating TC
**6. MODEL-AGNOSTIC** - These requirements apply to ALL models (Opus/Sonnet/Haiku)

## ⚠️ CRITICAL ERROR TO AVOID

**DO NOT create narrative "Expected Results" like this:**
```
Expected Results:
1. Initial password hash is applied successfully
2. "Password has been configured" appears in MCD logs
3. Updated password hash is applied successfully
```

**This is a text description - NOT actual code!**

**INSTEAD, extract the actual `<pre>` blocks from the PR comment:**
```
Expected Result (Step 1):
<pre>cat &lt;&lt;EOF | oc apply -f -
apiVersion: machineconfiguration.openshift.io/v1
kind: MachineConfig
metadata:
  name: 99-worker-password
spec:
  config:
    passwd:
      users:
      - name: core
        passwordHash: $6$xyz...
EOF</pre>

Expected Result (Step 2):
<pre>oc logs machine-config-daemon-xxx | grep -i pass
I0504 12:18:35.475096 2595 update.go:3050] "Starting update: passwd:true"
I0504 12:18:36.373371 2595 update.go:2495] Password has been configured</pre>
```

**The Expected Result column MUST contain the actual commands, YAML, and outputs from the PR comment's `<pre>` tags.**

## Workflow

**🛑 PRE-EXECUTION SELF-CHECK - READ BEFORE STARTING 🛑**

Before you proceed, answer these questions to yourself:

1. **Do I understand that Expected Result MUST contain actual code from `<pre>` blocks?**
   - If NO → Re-read the "EXPECTED RESULT = CODE BLOCKS ONLY" section above
   
2. **Do I understand the title MUST be `[MCO][JIRA-ID] Description`?**
   - If NO → Re-read the "TITLE FORMAT IS MANDATORY" section above
   
3. **Do I know where to find the `<pre>` blocks in the PR comment?**
   - If NO → They're in HTML tags like `<pre>cat <<EOF...</pre>` or inside `<details>`
   
4. **Am I about to write "MachineConfig is created successfully" in Expected Result?**
   - If YES → STOP! That's narrative text. Extract the actual `<pre>` block instead.
   
5. **Do I have the Jira ID extracted to build the [MCO][JIRA-ID] title?**
   - If NO → Extract it from the PR title/body first before proceeding

**Only proceed if you answered correctly to ALL questions above.**

---

**CRITICAL: ALL REQUIRED QUESTIONS MUST BE ASKED**

Regardless of model (Opus/Sonnet/Haiku) or input source (PR/Jira/interactive), 
**you MUST ask ALL required questions in order**. Do not skip questions or assume defaults.

### Required Questions Checklist (MUST ASK ALL):
1. ✓ Title (format: `[MCO][JIRA-ID] Description`)
2. ✓ Test Type (Positive/Negative)
3. ✓ Importance (Critical/High/Medium/Low)
4. ✓ Automation Status (Automated/Manual/To be automated)
5. ✓ OCP Version (e.g., 4.23) - **REQUIRED, never skip**
6. ✓ Tags (optional, but MUST ask even if answer is "None")
7. ✓ Test Steps Preview (MUST show before confirmation)
8. ✓ **FINAL CONFIRMATION** (MUST display summary and get approval)
9. ✓ **POST-CREATION VERIFICATION** (MUST verify all fields are set correctly)

### 1. Detect Input Mode and Extract Data

**From GitHub PR Comment:**
- Parse PR URL to get PR number and comment ID
- Fetch PR title and body (extract Jira ID: OCPBUGS-XXXXX, MCO-XXXX)
  - **CRITICAL:** Extract Jira ID from PR title/body and store it separately
  - The Jira ID goes in BOTH the TC title AND the Trello/Jira field
  - Common mistake: putting Jira ID only in description, leaving Trello/Jira field empty
  
- **Extract and Build Title:**
  ```
  PR Title: "Bug 123456: MCD applies password only when hash changes"
  OR: "[OCPBUGS-83830] Fix password update detection"
  
  Step 1: Extract Jira ID
    - Look for patterns: OCPBUGS-\d+, MCO-\d+, Bug \d+
    - If "Bug 12345" → convert to "OCPBUGS-12345"
  
  Step 2: Extract description
    - Remove Jira ID from PR title
    - Clean up prefix like "Bug:", "[OCPBUGS-xxx]", etc.
  
  Step 3: Build TC title
    - ALWAYS construct as: f"[MCO][{jira_id}] {description}"
    - Example: "[MCO][OCPBUGS-83830] MCD applies password only when hash changes"
  ```
  
  **Never use the PR title directly - always reconstruct with [MCO][JIRA-ID] prefix**
- **Use GitHub API** (not WebFetch) to get raw comment body:
  ```bash
  curl -s "https://api.github.com/repos/{org}/{repo}/issues/comments/{comment_id}" | jq -r '.body'
  ```
- **Parse `<details>` blocks** - user uses collapsible sections:
  ```html
  <details><summary>Title</summary><pre>output</pre></details>
  ```
  - **IMPORTANT:** Expand `<details>` tags to get the inner `<pre>` content
  - The `<pre>` content inside `<details>` is the Expected Result
  
- **Extract test steps pattern:**
  ```markdown
  - Step description (action to perform)
  <pre>code/commands/output here</pre>
  
  OR with collapsible details:
  
  - Step description (action to perform)
  <details><summary>Click to expand</summary>
  <pre>code/commands/output here</pre>
  </details>
  ```
  
- Extract from comment:
  - OCP Version (from Environment section or PR labels)
  - Test steps: Bullet point text = Step, `<pre>` content = Expected Result
  - Code blocks from `<pre>` tags (both inside and outside `<details>`)
  
**From Jira Issue:**
- Fetch Jira issue details
- Extract acceptance criteria
- Get linked PRs

### 2. Default Values (Per OCP-88122 PDF Structure)

**CRITICAL: These fields are MANDATORY and must NEVER be empty:**

```
Component: Machine Config Operator
Sub Team: MCO
Products: OCP
Test Type: Functional
Subtype 1: -
Subtype 2: -
Status: draft
Arch: __ALL__
Environment: All __ALL__
Assignee: auto-detected from POLARION_TOKEN (JWT sub field)
Trello/Jira: <extracted from PR/Jira> (REQUIRED - never leave empty)
Version: <extracted from PR/Jira or ask user> (REQUIRED - never leave empty)
```

**If the script or MCP tool leaves any of these empty, the TC will be malformed.**
**You MUST verify all fields are set after TC creation and PATCH any missing fields.**

### 3. Ask User for Required Fields

Use `AskUserQuestion` to collect:

**Essential Fields:**
1. **Title** format: `[MCO][JIRA-ID] Description` (always use MCO, not MCD/MCC/etc.)
2. **Test Type**: Positive or Negative scenario
3. **Importance**: Critical, High, Medium, Low
   - Maps to Polarion `severity`: Critical→must_have, High→should_have, Medium→nice_to_have, Low→will_not_have
4. **Automation**: Automated, Manual, To be automated
5. **Tags**: Any additional tags (optional)

**Auto-set fields (no need to ask):**
- Products: OCP
- Test Type: functional
- Subtype 1: -
- Subtype 2: -
- Assignee: auto-detected from POLARION_TOKEN
- Component: Machine Config Operator
- Sub Team: MCO

### 4. Format Description (Case1, Case2, Case3 Style)

Based on OCP-88122 PDF, description should be HTML with test cases:

```html
<p><b>Case1: [First scenario]</b></p>
<p>[Detailed steps for case 1]<br/>
[Expected behavior]</p>

<p><b>Case2: [Second scenario]</b></p>
<p>[Detailed steps for case 2]<br/>
[Expected behavior]</p>

<p><b>Case3: [Negative/edge case]</b></p>
<p>[Detailed steps for case 3]<br/>
[Expected behavior]</p>

<p><b>Related PR:</b> [GitHub PR URL]</p>
```

**IMPORTANT - Description vs Test Steps:**
- **Description field**: High-level summary and test cases (narrative text)
- **Test Steps table**: Actual executable steps with code blocks (structured data)

DO NOT confuse these two! The Description is narrative, but the Test Steps MUST have actual code in Expected Result.

### 5. Parse Test Steps from PR Comment

**CRITICAL: Understanding GitHub → Polarion Mapping**

In GitHub PR comments, test validation follows this format:
```
- Step description text here
<pre>
command output or code block here
</pre>
```

This maps to Polarion as:
- **Step column**: `- Step description text here` (the bullet point text)
- **Expected Result column**: `command output or code block here` (the `<pre>` content)

**Extraction Process:**

1. **Find each bullet point** (e.g., `- Applied below MC:` or `- Check MCP status`)
   - This becomes the **Step** field (brief description of action)
   
2. **Find the associated `<pre>` block** (immediately after the bullet point)
   - May be inside `<details>` tags - expand them first
   - This becomes the **Expected Result** field (the actual output/verification)
   
3. **Copy the `<pre>` content VERBATIM** into Expected Result
   - Include all command outputs, log lines, YAML, JSON
   - Never summarize or paraphrase
   - Preserve all whitespace, line breaks, formatting

**Key Rules:**
- `"step"` field: Bullet point text only (brief action description)
  - Example: "Apply MachineConfig with passwordHash"
  - Goes to → Polarion "Step" column
  
- `"expectedResult"` field: Exact `<pre>` block content (commands, outputs, logs)
  - Example: Full YAML + `oc` commands + log output
  - Goes to → Polarion "Expected Result" column
  - **MUST contain actual code/commands/output - NOT text descriptions**
  - ❌ WRONG: "Initial password hash is applied successfully"
  - ✅ CORRECT: `cat <<EOF | oc apply -f -\napiVersion: v1\nkind: MachineConfig\n...`
  
- **VERBATIM copy-paste** of all code blocks — never summarize
- **NEVER write narrative descriptions** in Expected Result - only actual code/output
- Preserve all whitespace, line breaks, and formatting

**Example Extraction from GitHub PR Comment:**

```markdown
GitHub Comment:
--------------
- Applied below MC with passwordHash:
<pre>
cat <<EOF | oc apply -f -
apiVersion: machineconfiguration.openshift.io/v1
kind: MachineConfig
metadata:
  name: 99-worker-password
spec:
  config:
    passwd:
      users:
      - name: core
        passwordHash: $6$xyz...
EOF
</pre>

- Check MCD logs for password configuration:
<pre>
oc logs -n openshift-machine-config-operator machine-config-daemon-xxxxx | grep -i pass

I0504 12:18:35.475096 2595 update.go:3050] "Starting update: &{...passwd:true...}"
I0504 12:18:36.373371 2595 update.go:2495] Password has been configured
</pre>
```

**Maps to Polarion Test Steps:**

```json
[
  {
    "step": "Applied below MC with passwordHash:",
    "expectedResult": "<pre>cat &lt;&lt;EOF | oc apply -f -\napiVersion: machineconfiguration.openshift.io/v1\nkind: MachineConfig\nmetadata:\n  name: 99-worker-password\nspec:\n  config:\n    passwd:\n      users:\n      - name: core\n        passwordHash: $6$xyz...\nEOF</pre>"
  },
  {
    "step": "Check MCD logs for password configuration:",
    "expectedResult": "<pre>oc logs -n openshift-machine-config-operator machine-config-daemon-xxxxx | grep -i pass\n\nI0504 12:18:35.475096 2595 update.go:3050] \"Starting update: &amp;{...passwd:true...}\"\nI0504 12:18:36.373371 2595 update.go:2495] Password has been configured</pre>"
  }
]
```

**What Goes Where:**
- Bullet text (`- Applied below MC...`) → **Step column**
- `<pre>` content (YAML, commands, outputs) → **Expected Result column** (HTML-escaped, wrapped in `<pre>`)

**CRITICAL - HTML Escaping for Expected Result:**
When calling the MCP tool `add_test_steps_to_testcase`, the `expectedResult` content MUST be:
1. HTML-escaped (replace `<` with `&lt;`, `>` with `&gt;`, `&` with `&amp;`, `"` with `&quot;`)
2. Wrapped in `<pre>` tags
3. **NO line breaks or newlines** before/after `<pre>` tags - must be: `<pre>content</pre>` not `<pre>\ncontent\n</pre>`

Without this, characters like `<<EOF`, `<pre>`, `<details>` break the Polarion table rendering
and content spills outside the table.

Example: raw `cat <<EOF` becomes `<pre>cat &lt;&lt;EOF</pre>` in the expectedResult field.

**WRONG (breaks table):**
```
expectedResult: "<pre>
cat <<EOF
apiVersion: v1
</pre>"
```

**CORRECT (stays in table):**
```
expectedResult: "<pre>cat &lt;&lt;EOF\napiVersion: v1</pre>"
```

### 5b. Preview Test Steps Before Confirmation

**ALWAYS use `AskUserQuestion` with the `preview` field** to show the user the extracted test steps
before creating the TC. This gives a visual side-by-side preview of Step vs Expected Result.

**Format the preview as:**
```
STEP 1: <brief action from bullet point>

EXPECTED RESULT:
<full code block content from <pre> tag - includes commands, YAML, outputs, logs>

---
STEP 2: <brief action from bullet point>

EXPECTED RESULT:
<full code block content from <pre> tag - includes commands, YAML, outputs, logs>

---
STEP 3: <brief action from bullet point>

EXPECTED RESULT:
<full code block content from <pre> tag - includes commands, YAML, outputs, logs>
```

**Example Preview:**
```
STEP 1: Applied below MC with passwordHash

EXPECTED RESULT:
cat <<EOF | oc apply -f -
apiVersion: machineconfiguration.openshift.io/v1
kind: MachineConfig
metadata:
  name: 99-worker-password
spec:
  config:
    passwd:
      users:
      - name: core
        passwordHash: $6$xyz...
EOF

---
STEP 2: Check MCD logs for password configuration

EXPECTED RESULT:
oc logs -n openshift-machine-config-operator machine-config-daemon-xxxxx | grep -i pass

I0504 12:18:35.475096 2595 update.go:3050] "Starting update: &{...passwd:true...}"
I0504 12:18:36.373371 2595 update.go:2495] Password has been configured
```

**Offer layout options** (e.g., 4 separate steps vs 3 Case-style steps) so the user can pick
the structure that matches Polarion conventions. Each option gets its own `preview` block.

**Key Point:** Show the FULL code/output in Expected Result, not summaries.

### 5c. Debug Trace - Show What You Extracted

**MANDATORY: Before confirmation, show the user what you extracted**

Display this trace to prove you extracted correctly:

```
=== EXTRACTION DEBUG TRACE ===

Source: <PR URL or Jira ID>

Extracted Jira ID: OCPBUGS-12345
Extracted Description: MCD applies password only when hash changes

Constructed Title: [MCO][OCPBUGS-12345] MCD applies password only when hash changes
  ✓ Title Validation: Matches [MCO][JIRA-ID] pattern

Test Steps Extraction:
  Found 3 bullet points in PR comment
  Found 3 <pre> blocks with code
  
  Step 1:
    - Bullet: "Applied below MC with passwordHash"
    - <pre> content: "cat <<EOF | oc apply -f -\napiVersion:..." (327 characters)
    
  Step 2:
    - Bullet: "Check MCD logs"
    - <pre> content: "oc logs machine-config-daemon... Password configured" (215 characters)
    
  Step 3:
    - Bullet: "Add SSH key"
    - <pre> content: "sshAuthorizedKeys:\n- ssh-rsa..." (189 characters)

✓ All Expected Results contain actual code/commands (NOT descriptions)
✓ All required fields will be set (Component, Test Type, Sub Team, etc.)
```

**If any of the above shows descriptions instead of code, STOP and re-extract the `<pre>` blocks.**

### 5d. Final Confirmation Before TC Creation

**MANDATORY STEP - ALWAYS REQUIRED**

After showing the debug trace and collecting all information, **you MUST display a summary and ask for confirmation**:

```
=== POLARION TEST CASE SUMMARY ===

✅ Title: [MCO][OCPBUGS-12345] Description here
   Format Validation: PASSED ✓ (starts with [MCO][JIRA-ID])

Jira ID: OCPBUGS-12345
Version: 4.23
Component: Machine Config Operator
Sub Team: MCO
Products: OCP
Test Type: Positive/Negative
Test Category: functional
Importance: High (should_have)
Automation: To be automated
Status: draft
Arch: __ALL__
Environment: All __ALL__
Tags: <list or "None">

Description:
<formatted HTML with Case1, Case2, Case3>

Test Steps: <count>
1. <step description> → <expected result has actual code/commands>
2. <step description> → <expected result has actual code/commands>
...

Related:
  PR: <github url>
  Jira: <jira url>
```

**BEFORE showing this summary, validate:**
- Title matches `^\[MCO\]\[.+\] .+`
- If validation fails, DO NOT show summary - go back and fix title first

Then use `AskUserQuestion` with options:
- "Create Test Case" (Proceed with TC creation)
- "Edit Details" (Go back and modify collected information)
- "Cancel" (Abort TC creation)

**This confirmation step is REQUIRED regardless of model or interaction mode.**

**Handling User Choice:**
- **Create Test Case**: Proceed to step 7 (create TC in Polarion)
- **Edit Details**: Ask which field to modify (Title/Test Type/Importance/Automation/Version/Tags/Steps), 
  re-ask that specific question, update the summary, and show confirmation again
- **Cancel**: Stop execution and inform user that TC creation was cancelled

### 6. Build Title (Already Done in Step 5c Debug Trace)

**MANDATORY FORMAT:** `[MCO][JIRA-ID] Description`

**Rules:**
1. **ALWAYS starts with `[MCO]`** - Never omit this prefix
2. **Followed by `[JIRA-ID]`** - Must include the Jira ticket (e.g., `[OCPBUGS-83830]` or `[MCO-1972]`)
3. **Then the description** - Brief summary of what the test validates

**IMPORTANT:** 
- Always use `[MCO]` prefix, NOT `[MCD]`, `[MCC]`, or other variants
- Never create a title without the `[MCO][JIRA-ID]` prefix
- If user provides a title without the prefix, ADD IT automatically

**Examples:**
- ✅ CORRECT: `[MCO][OCPBUGS-83830] MCD applies password usermod only when hash changes`
- ✅ CORRECT: `[MCO][MCO-1972] Verify osImageStream inheritance for custom pools`
- ❌ WRONG: `Verify password updates only applied when passwordHash changes` (missing prefix)
- ❌ WRONG: `[MCD][OCPBUGS-83830] Password validation` (wrong component prefix)

**If extracting from PR/Jira:**
1. Extract the Jira ID from PR title or body
2. Extract the description
3. **ALWAYS construct as:** `[MCO][{jira_id}] {description}`
4. Never use the extracted title as-is without the prefix

### 7. Create Test Case and Set All Fields

**IMPORTANT: Only proceed if user confirmed "Create Test Case" in step 5c**

**Pre-Creation Validation Checklist:**

Before calling any creation tool, verify these values are NOT empty:
- ✓ **Title**: MUST match format `[MCO][JIRA-ID] Description`
  - Regex check: `^\[MCO\]\[.+\] .+`
  - Example: `[MCO][OCPBUGS-83830] MCD applies password usermod only when hash changes`
  - ❌ Reject if missing `[MCO]` or `[JIRA-ID]` prefix
- ✓ Component: "Machine Config Operator" 
- ✓ Sub Team: "MCO"
- ✓ Products: "OCP"
- ✓ Test Type: "Functional"
- ✓ Subtype 1: "-"
- ✓ Subtype 2: "-"
- ✓ Trello/Jira: Must have value (e.g., "OCPBUGS-83830" or "MCO-1972")
- ✓ Version: Must have value (e.g., "4.23", "4.19")
- ✓ Status: "draft" or "approved"

If ANY field is empty or title format is wrong, DO NOT proceed. Fix the values first.

**Title Format Validation:**
```python
import re
if not re.match(r'^\[MCO\]\[.+\] .+', title):
    raise ValueError(f"Title must start with [MCO][JIRA-ID]: {title}")
```

**Step 1: Create TC + PATCH custom fields** (standalone script handles both)

Execute `.claude/scripts/create_polarion_tc.py` with JSON config.
The script creates the TC, then PATCHes custom fields that Polarion ignores during creation.

```python
config = {
  "test_case": {
    "title": "[MCO][OCPBUGS-83830] Verify password...",
    "description": "<html formatted cases>",
    "jira_id": "OCPBUGS-83830",  # REQUIRED - never null/empty
    "version": "4.23",  # REQUIRED - never null/empty
    "component": "Machine Config Operator",  # REQUIRED - always set
    "sub_team": "MCO",  # REQUIRED - always set
    "products": "OCP",  # REQUIRED - always set
    "status": "draft",  # REQUIRED - always set
    "severity": "must_have",  # from Importance question
    "test_type": "functional",  # REQUIRED - always "functional"
    "subtype1": "-",  # REQUIRED - always "-"
    "subtype2": "-",  # REQUIRED - always "-"
    "assignee": null  # auto-detected from token, can be null
  },
  "test_steps": [...]
}
```

**CRITICAL: Validate the created TC has all fields set:**
After creation, use `mcp__polarion__get_polarion_test_case` to verify all fields.
If any required field is empty, use `mcp__polarion__update_polarion_test_case` to set it.

**Step 2: Add test steps** (script 404s, so use MCP tool)

```
mcp__polarion__add_test_steps_to_testcase(
  test_case_id="OCP-XXXXX",
  test_steps=[
    {
      "step": "Applied below MC with passwordHash",
      "expectedResult": "<pre>cat &lt;&lt;EOF | oc apply -f -\napiVersion: machineconfiguration.openshift.io/v1\nkind: MachineConfig\n...</pre>"
    },
    {
      "step": "Check MCD logs for password configuration",
      "expectedResult": "<pre>oc logs | grep -i pass\nI0504 12:18:35.475096 Password has been configured</pre>"
    }
  ]
)
```

**Result in Polarion Test Steps Table:**

| Step | Expected Result |
|------|----------------|
| Applied below MC with passwordHash | `cat <<EOF \| oc apply -f -`<br>`apiVersion: machineconfiguration.openshift.io/v1`<br>`kind: MachineConfig`<br>... |
| Check MCD logs for password configuration | `oc logs \| grep -i pass`<br>`I0504 12:18:35.475096 Password has been configured` |

**Critical:** All code/output MUST be in the "Expected Result" column, formatted with `<pre>` tags and HTML-escaped.

### 8. Post-Creation Verification

**MANDATORY: Verify the TC was created correctly**

1. Fetch the created TC: `mcp__polarion__get_polarion_test_case(test_case_id="OCP-XXXXX", include_test_steps=true)`
2. **Verify title format first:**
   - Check if title starts with `[MCO][JIRA-ID]`
   - ❌ If title is missing prefix (e.g., "Verify password updates...") → **CRITICAL ERROR**
   - Alert user and provide correct title format to update manually or via PATCH
3. Check for missing fields:
   - If Component is empty → PATCH it with "Machine Config Operator"
   - If Test Type is empty → PATCH it with "Functional"
   - If Sub Team is empty → PATCH it with "MCO"
   - If Products is empty → PATCH it with "OCP"
   - If Subtype 1 is empty → PATCH it with "-"
   - If Subtype 2 is empty → PATCH it with "-"
   - If Trello/Jira is empty → PATCH it with the Jira ID
   - If Version is empty → PATCH it with the version number
3. **CRITICAL: Verify test steps have ACTUAL CODE in Expected Result:**
   - Read the Expected Result field for each step
   - ❌ If it contains narrative text like "Password is applied successfully" → WRONG
   - ✅ If it contains `<pre>` tags with code/commands/output → CORRECT
   - If Expected Result has narrative instead of code → **FAIL the verification** and alert user

**CRITICAL: Report errors if verification fails:**

If you find ANY of these issues, report **FAILURE** to the user with details:

❌ **Common Error 1: Wrong Title (like OCP-88942, OCP-88944)**
   - Title: "Verify password updates..." (missing [MCO][JIRA-ID])
   - **This means you didn't build the title correctly**
   
❌ **Common Error 2: Empty Fields (like OCP-88942, OCP-88944)**
   - Component, Test Type, Sub Team, Products, Trello/Jira, Version are empty
   - **This means you didn't set the fields or PATCH failed**
   
❌ **Common Error 3: Narrative Expected Results (like OCP-88944)**
   - Expected Result: "MachineConfig is created successfully"
   - **This means you wrote descriptions instead of extracting `<pre>` blocks**

**Only report success if:**
- ✅ Title matches `[MCO][JIRA-ID] Description`
- ✅ All required fields are populated correctly
- ✅ Test Steps contain actual code/commands in Expected Result (not descriptions)

### 9. Report Results

Display:
- Created test case ID
- Polarion URL
- **Verification status** (all fields set correctly: YES/NO)
- Summary of added steps
- Ask if user wants to automate this test case (invoke `/automate-test` or `/fetch-and-automate`)

## Input Modes

### Mode 1: Interactive (default)
Ask all questions step-by-step using `AskUserQuestion`

### Mode 2: From GitHub PR Comment
- Parse PR URL and comment ID
- Extract test steps from comment
- Pre-fill questions with extracted data
- Confirm with user before creating

### Mode 3: From Jira Issue
- Fetch Jira issue details
- Extract acceptance criteria as test steps
- Pre-fill questions
- Confirm with user

## Question Templates

### Title Question

**CRITICAL: Pre-fill with correct format, don't let user enter from scratch**

```
question: "Confirm or edit the test case title:"
header: "Title"
Default: "[MCO][{jira_id}] {extracted_title}"
Format: MUST be [MCO][JIRA-ID] Description
```

**Process:**
1. Extract Jira ID from PR/Jira (e.g., "OCPBUGS-83830", "MCO-1972")
2. Extract description from PR title or summary
3. **Construct the title as:** `[MCO][{jira_id}] {description}`
4. Present this pre-filled title to the user
5. User can edit the description part, but the `[MCO][JIRA-ID]` prefix MUST remain

**Validation:**
- Before creating TC, verify title matches regex: `^\[MCO\]\[.+\] .+`
- If title doesn't match, reject it and ask user to fix it

### Test Type Question
```
question: "Is this a positive or negative test scenario?"
header: "Test Type"
options:
  - Positive (Tests expected/happy path behavior)
  - Negative (Tests error handling/edge cases)
```

### Importance Question  
```
question: "What is the test importance level?"
header: "Importance"
options:
  - Critical (Must pass for release - blocks deployment)
  - High (Very important - should pass before release)
  - Medium (Important but not blocking)
  - Low (Nice to have, not critical)
```

### Automation Question
```
question: "What is the automation status?"
header: "Automation"
options:
  - Automated (Already has automated test code)
  - Manual (Currently manual, will stay manual)
  - To be automated (Manual now, will automate later)
```

### Version Question
```
question: "Which OCP versions does this test apply to?"
header: "Version"
Default: "{extracted_version}" (from PR/Jira)
Examples: 4.22, 4.23, 4.24
User can specify single version or comma-separated list
```

### Tags Question (Optional)
```
question: "Any additional tags? (Leave empty if none)"
header: "Tags"
Default: "" (empty)
Examples: Platform:aws, FIPS, IPv6
Note: NOT for Serial/Disruptive - those are test execution tags
```

### Post-Creation Automation Question
```
question: "Generate automated Go test code now?"
header: "Generate Code"
options:
  - Yes (Call /fetch-and-automate {tc_id} "mco integration" longduration)
  - No (Just create Polarion TC)
```

## Example Usage

```
/create-polarion-tc
```

Or with PR comment:
```
/create-polarion-tc https://github.com/openshift/machine-config-operator/pull/5889#issuecomment-4371727843
```

Or with Jira:
```
/create-polarion-tc MCO-2025
```

## Common Mistakes to Avoid

**Issue 0: Wrong Title Format (like in OCP-88942)**
- ❌ WRONG: `Verify password updates only applied when passwordHash changes`
- ✅ CORRECT: `[MCO][OCPBUGS-83830] MCD applies password usermod only when hash changes`
- **Root cause:** Using PR title directly without adding `[MCO][JIRA-ID]` prefix
- **Solution:** Always construct title as `[MCO][{jira_id}] {description}`

**Issue 1: Empty Fields (like in wrong.pdf OCP-88941)**
- ❌ Component, Test Type, Sub Team, Products, Version, Trello/Jira all empty
- ✅ ALL fields must be filled with values shown in OCP-88122/OCP-88202

**Issue 2: Test Steps Formatting**
- ❌ YAML/code blocks floating outside the Test Steps table
- ✅ All code must be inside `<pre>` tags within "Expected Result" column
- ❌ Using `<pre>\ncat <<EOF\n</pre>` (newlines break table)
- ✅ Using `<pre>cat &lt;&lt;EOF</pre>` (inline, HTML-escaped)

**Issue 3: Wrong Step/Expected Result Mapping**
- ❌ Putting both description AND code in "Step" field
- ❌ Leaving "Expected Result" empty or with just summary text
- ❌ **CRITICAL ERROR:** Writing narrative descriptions in Expected Result
  ```
  Expected Results:
  1. Initial password hash is applied successfully
  2. "Password has been configured" appears in MCD logs
  ```
  **This is WRONG!** These are summaries, not actual code!
  
- ✅ **Step field**: Brief action description from bullet point (e.g., "Apply MachineConfig")
- ✅ **Expected Result field**: Full code blocks, commands, outputs from `<pre>` tags
  ```
  Expected Result:
  cat <<EOF | oc apply -f -
  apiVersion: machineconfiguration.openshift.io/v1
  kind: MachineConfig
  metadata:
    name: 99-worker-password
  spec:
    config:
      passwd:
        users:
        - name: core
          passwordHash: $6$xyz...
  EOF
  ```
  **This is CORRECT!** Actual executable code/commands from `<pre>` block.

**Issue 4: Missing HTML Escaping**
- ❌ Raw `<`, `>`, `&` characters break Polarion rendering
- ✅ Escape all special chars: `&lt;`, `&gt;`, `&amp;`

**Visual Mapping:**
```
GitHub PR Comment                 Polarion Test Case
================                  ==================

- Apply MC with password    →    Step: "Apply MC with password"
<pre>                              
cat <<EOF | oc apply -f -         Expected Result:
apiVersion: v1                    "<pre>cat &lt;&lt;EOF | oc apply -f -
kind: MachineConfig               apiVersion: v1
...                               kind: MachineConfig
EOF                               ...
</pre>                            EOF</pre>"

- Check logs                 →    Step: "Check logs"
<pre>
oc logs | grep pass               Expected Result:
I0504 Password configured         "<pre>oc logs | grep pass
</pre>                            I0504 Password configured</pre>"
```

## Error Handling

- If Polarion MCP server not configured, show setup instructions
- If test case creation fails, display error and let user retry
- If test steps format is unclear, ask for clarification
- Validate Jira ID format before attempting fetch
- **If TC is created with empty fields, immediately PATCH to fix them**

## Output Format

```
✅ Polarion Test Case Created and Verified!

Test Case ID: OCP-12345
URL: https://polarion.engineering.redhat.com/polarion/#/project/OSE/workitem?id=OCP-12345

✅ Title Verification: CORRECT FORMAT
  ✓ Title: [MCO][OCPBUGS-83830] MCD applies password only when hash changes
  ✓ Format: Matches [MCO][JIRA-ID] pattern

✅ Field Verification: ALL REQUIRED FIELDS SET CORRECTLY
  ✓ Component: Machine Config Operator
  ✓ Sub Team: MCO
  ✓ Products: OCP
  ✓ Test Type: Functional
  ✓ Subtype 1: -
  ✓ Subtype 2: -
  ✓ Importance: should_have (High)
  ✓ Status: draft
  ✓ Version: 4.23
  ✓ Trello/Jira: OCPBUGS-83830
  ✓ Arch: __ALL__
  
Related:
  Jira: OCPBUGS-83830
  PR: https://github.com/openshift/machine-config-operator/pull/5889

Test Steps: 3 (properly formatted in table)
  1. Apply MachineConfig with password hash
     → MCD logs show "Password has been configured"
  
  2. Update/change password hash in MC  
     → Password update detected and applied in logs
  
  3. Add SSH key without changing password
     → No password-related logs triggered (only SSH changes)

Tags: Serial, Disruptive, MCD

Next Steps:
  - Review in Polarion: https://polarion.engineering.redhat.com/polarion/#/project/OSE/workitem?id=OCP-12345
  - Automate test: /fetch-and-automate OCP-12345 "mco integration" longduration
```

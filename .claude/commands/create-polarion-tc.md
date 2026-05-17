---
name: create-polarion-tc
description: Create new Polarion test case with guided questions
skill: create-polarion-tc
---

# /create-polarion-tc

Create a new test case in Polarion through an interactive guided workflow.

## Usage

```bash
# Interactive mode - asks all questions
/create-polarion-tc

# From GitHub PR comment
/create-polarion-tc <pr-url>#<comment-id>

# From Jira issue
/create-polarion-tc <jira-id>
```

## Examples

```bash
# Create test case interactively
/create-polarion-tc

# Extract from GitHub PR comment
/create-polarion-tc https://github.com/openshift/machine-config-operator/pull/5889#issuecomment-4371727843

# Extract from Jira issue
/create-polarion-tc MCO-2025
```

## What You'll Be Asked

1. **Test title** - Clear, concise summary
2. **Component** - MCO, MCD, Integration, etc.
3. **Jira ID** (optional) - Link to related Jira issue
4. **Description** - Context and what the test verifies
5. **Preconditions** - Setup requirements
6. **Test steps** - Numbered steps to execute
7. **Expected results** - What should happen for each step
8. **Negative scenarios** - Error cases to test
9. **Severity** - must_have, should_have, nice_to_have
10. **Status** - draft or approved
11. **Tags** - Serial, Disruptive, Platform labels, etc.

## Next Steps

After creation, you can:
- Review the test case in Polarion
- Generate automated test code with `/fetch-and-automate <polarion-id>`
- Update steps with `/update-polarion-tc <polarion-id>`

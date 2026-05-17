---
name: Fetch and Automate Workflow
description: Fetch test specs from Polarion/Jira and generate automated tests
---

# Fetch and Automate Workflow

Fetch test specifications from Polarion or Jira, convert to text format, then invoke `/automate-test` to generate Go test code.

## Arguments

- `<id>`: Polarion ID (OSE-12345, OCP-12345) or Jira ID (MCO-1234, OCPBUGS-1234)
- `<suite-name>`: Target suite (e.g., "mco kernel", "mco security")
- `<type>`: Test type - `longduration` or `disruptive`

## Workflow Steps

### 1. Detect ID Type

Match `<id>` argument against patterns:

- **Polarion**: `^(OSE|OCP)-\d+$`
- **Jira**: `^(MCO|OCPBUGS)-\d+$`

If no match, show error and exit.

### 2. Fetch from Polarion (if Polarion ID)

**Check MCP availability:**
```
Look for tool: mcp__polarion__get_polarion_test_case
```

**Fetch test case:**
```
Call: mcp__polarion__get_polarion_test_case(test_case_id="<id>")
```

**Extract fields:**
- `id` -> Polarion ID
- `title` -> Test title
- `description` -> Test description
- `test_steps[]` -> Array of steps
  - Each step has: `step`, `expectedResult`
- `customFields.caseautomation` -> Tags (comma-separated: Serial, Disruptive, Platform:aws, etc.)

**Fallback:** If MCP unavailable or call fails, prompt user to provide test spec as text file instead.

### 3. Fetch from Jira (if Jira ID)

**Check MCP availability:**
```
Look for tool: mcp__atlassian__getJiraIssue
```

**Fetch issue:**
```
Call: mcp__atlassian__getJiraIssue(
  cloudId="issues.redhat.com", 
  issueIdOrKey="<id>",
  responseContentFormat="markdown"
)
```

**Extract fields:**
- `fields.summary` -> Feature summary
- `fields.description` -> Feature description
- Parse acceptance criteria from description (look for "Acceptance Criteria:" section)

**Generate test scenarios:**

Analyze the feature description and acceptance criteria to generate test steps:

1. **Positive scenario**: Happy path based on main use case
2. **Negative scenario**: Error handling from acceptance criteria
3. **Edge cases**: Boundary conditions mentioned

Prompt user to confirm/modify the generated test steps before proceeding.

**Fallback:** If MCP unavailable or call fails, prompt user to provide test spec as text file instead.

### 4. Convert to Text Format

Create a text specification in this format:

```
PolarionID: <polarion-id>
Title: <test-title>

Description:
<description>

Preconditions:
- <precondition-1>
- <precondition-2>

TestSteps:
1. <step-1>
2. <step-2>
3. <step-3>

ExpectedResults:
1. <expected-result-1>
2. <expected-result-2>
3. <expected-result-3>

Tags: <tags-comma-separated>
```

**Notes:**
- For Polarion: Use the `test_steps` array directly
- For Jira: Use the generated test scenarios
- Tags: Extract from Polarion `caseautomation` field, or infer from Jira labels
- Always include: `Serial, Disruptive` tags (required for MCO tests)

### 5. Save Spec to Temporary File

Write the formatted spec to a temporary file:

```bash
mkdir -p test-specs
cat > test-specs/<id>.txt <<EOF
<formatted-spec>
EOF
```

### 6. Invoke /automate-test

Call the automate-test skill with the spec file:

```
/automate-test test-specs/<id>.txt "<suite-name>" <type>
```

The automate-test skill will:
- Read the spec file
- Parse test steps
- Generate Go test code
- Build and verify

### 7. Cleanup (Optional)

After successful test generation, optionally delete the temporary spec file:

```bash
rm test-specs/<id>.txt
```

Or keep it for reference.

## MCP Server Setup

This skill requires MCP servers for Jira and Polarion.

### Jira/Atlassian MCP Server (Global)

Use the official Atlassian hosted MCP server:

```bash
claude mcp add --transport http atlassian https://mcp.atlassian.com/v1/mcp
```

This configures the Atlassian MCP server globally. You'll be prompted to authenticate with your Atlassian account.

**For Red Hat Jira (issues.redhat.com):**
- The hosted MCP server supports Red Hat Jira
- Authenticate with your Red Hat SSO credentials when prompted
- Make sure your account has access to MCO/OCPBUGS projects

### Polarion MCP Server (Local)

Configure in project `.mcp.json`:

```json
{
  "mcpServers": {
    "polarion": {
      "command": "python3",
      "args": ["/path/to/polarion-mcp-server/server.py"],
      "env": {
        "POLARION_TOKEN": "your-token",
        "POLARION_URL": "https://polarion.engineering.redhat.com",
        "POLARION_PROJECT": "OSE",
        "POLARION_VERIFY_SSL": "false"
      }
    }
  }
}
```

**Get Polarion token:**
1. Go to https://polarion.engineering.redhat.com
2. Profile → Personal Access Token
3. Create token (max 90 days)

**Clone Polarion MCP server:**
```bash
cd ~/VSCode
git clone https://github.com/redhat-community-ai-tools/polarion-mcp-server.git
pip install -r polarion-mcp-server/requirements.txt
```

Update the `args` path in `.mcp.json` to point to your local clone.

## Error Handling

**MCP server not configured:**
```
⚠ Polarion/Jira MCP server not configured.
Please provide test specification as a text file instead:
  /automate-test path/to/spec.txt "<suite-name>" <type>

Or configure MCP servers in .mcp.json (see skill documentation).
```

**MCP call fails:**
```
⚠ Failed to fetch from Polarion/Jira: <error-message>
Please provide test specification as a text file instead.
```

**Invalid ID format:**
```
⚠ Invalid ID format: "<id>"
Expected: OSE-12345, OCP-12345, MCO-1234, or OCPBUGS-1234
```

## Example Usage

### From Polarion

```bash
# Fetch OSE-88278 from Polarion and generate test
/fetch-and-automate OSE-88278 "mco kernel" longduration
```

This will:
1. Fetch test case OSE-88278 from Polarion
2. Convert to text spec in `test-specs/OSE-88278.txt`
3. Call `/automate-test test-specs/OSE-88278.txt "mco kernel" longduration`
4. Generate Go test in `test/extended-priv/mco_kernel.go`

### From Jira

```bash
# Fetch MCO-2222 from Jira and generate test
/fetch-and-automate MCO-2222 "mco integration" disruptive
```

This will:
1. Fetch feature MCO-2222 from Jira
2. Generate test scenarios from acceptance criteria
3. Convert to text spec in `test-specs/MCO-2222.txt`
4. Call `/automate-test test-specs/MCO-2222.txt "mco integration" disruptive`
5. Generate Go test in `test/extended-priv/mco_integration.go`

## Integration with /automate-test

This skill is a wrapper around `/automate-test` that:

1. **Fetches** test specifications from external systems (Polarion/Jira)
2. **Converts** to the text format expected by `/automate-test`
3. **Invokes** `/automate-test` with the converted spec

The actual test code generation is delegated to `/automate-test`, keeping responsibilities clear:

- `/fetch-and-automate` → Fetch and format specs
- `/automate-test` → Generate Go test code

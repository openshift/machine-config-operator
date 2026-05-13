---
description: Fetch test spec from Polarion/Jira and generate automated test
argument-hint: <polarion-or-jira-id> "<suite-name>" [longduration|disruptive]
---

Fetch test specification from Polarion or Jira, then generate automated Go test code.

Use the workflow in `.claude/skills/fetch-and-automate-workflow.md`.

**Workflow:**
1. Detect ID type (Polarion OSE-*/OCP-* or Jira MCO-*/OCPBUGS-*)
2. Fetch test specification via MCP
3. Convert to text format
4. Call /automate-test with the spec

**Setup:** Requires MCP servers for Jira and/or Polarion (see workflow file).

---
description: Automate MCO test creation from test specifications, learning from previous code reviews
argument-hint: <spec-source> "<suite-name>" [longduration|disruptive]
---

Generate new MCO e2e test cases in `test/extended-priv/` from test specifications.

Use the conventions in `.claude/skills/mco-automate-test-workflow.md` for all test generation rules.

**Input modes** (determined by `<spec-source>` format):
- **Text file**: a file path containing the test specification
- **Polarion ID** (future, MCO-2220): a numeric ID like `OCP-12345`
- **Jira ID** (future, MCO-2221): a Jira key like `MCO-1234`

**Workflow:**
1. Parse arguments. Read the spec file and extract test case ID, title, preconditions, steps, expected results, and tags. Derive target file from suite name (`"mco security"` -> `mco_security.go`).
2. Learn from previous code reviews — check memory (`review_patterns_mco.md`), fetch new patterns from merged PRs via `gh api` on `openshift/machine-config-operator`.
3. Read existing tests and utilities in `test/extended-priv/` to understand available helpers and patterns.
4. Generate the test code following all conventions from the workflow skill.
5. Build with `make machine-config-tests-ext` and verify the test appears in listing. Fix any build errors.

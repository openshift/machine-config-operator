---
name: MCO Automate Test Workflow
description: Conventions and workflow for generating new MCO e2e tests from test specifications
---

# MCO Test Automation Rules

Generate new e2e test code in `test/extended-priv/` from test specifications.

## Input Detection

- **Text file**: path exists on disk -> read and parse it for test case ID, title, preconditions, steps, expected results, tags
- **Polarion ID**: matches `OCP-\d+` or purely numeric -> future (MCO-2220)
- **Jira ID**: matches `MCO-\d+` -> future (MCO-2221)

Only text file input is supported now.

## Target File Resolution

Derive from `<suite-name>`: `"mco security"` -> `mco_security.go`
- If `test/extended-priv/<filename>` exists: add new `g.It()` block to it
- If it doesn't exist: create new file with full test structure

## Code Review Learning

Learn from previous code reviews to produce review-ready code. This is cumulative — patterns are stored in memory (`review_patterns_mco.md`) and grow with every run.

- Load existing patterns from memory, note the `Last Analyzed PR` as high-water mark
- Fetch merged PRs that modified `test/extended-priv/` via `gh`, only newer than last analyzed
- For each PR, fetch inline review comments and PR-level reviews via `gh api`
- Extract actionable coding standards (naming, resource handling, error handling, code structure, common mistakes)
- Deduplicate against existing patterns. Newer patterns win on contradiction
- Save updated patterns to `review_patterns_mco.md` memory
- Fall back to built-in conventions below if `gh` is unavailable

## Codebase Context

Before generating, read existing code to understand available utilities and patterns:

- The target file (if existing) — its Describe block, JustBeforeEach, variables, existing tests
- 2-3 similar test files based on topic
- Resource wrappers: `machineconfig.go`, `machineconfigpool.go`, `node.go`, etc.
- Helpers: `util.go`, `resource.go`, `remotefile.go`, `gomega_matchers.go`
- Testdata templates in `test/extended-priv/testdata/files/`

## Conventions

1. **Use GetCompactCompatiblePool** for MCP selection, unless the test requires the master pool
2. **Call GetCurrentTestPolarionIDNumber()** once per test
3. **Use Generic MC template** when possible
4. **Base64 encode** file configuration in MachineConfigs
5. **Use test number** in file paths, content, and resource names for uniqueness
6. **Node selection**: `GetSortedNodesOrFail()[0]`
7. **Step logging**: `exutil.By("<step>")` for each step, `logger.Infof("OK!\n")` after success
8. **Declare variables** in the `var` block when possible
9. **Error messages**: log the full resource with `%s`, not just `.GetName()`
10. **State recovery**: always recover initial state via defer (e.g., `defer resource.DeleteWithWait()`)
11. **Prefer Resource struct methods** over `oc.AsAdmin().Run`
12. **RemoteFile** with gomega checkers for verifying files inside nodes
13. **Concise comments** — one or two lines maximum

## New File Structure

```go
package extended

import (
	g "github.com/onsi/ginkgo/v2"
	o "github.com/onsi/gomega"
	exutil "github.com/openshift/machine-config-operator/test/extended-priv/util"
	logger "github.com/openshift/machine-config-operator/test/extended-priv/util/logext"
)

var _ = g.Describe("[sig-mco][Suite:openshift/machine-config-operator/<suite-type>][Serial][Disruptive] MCO <suite-name>", func() {
	defer g.GinkgoRecover()

	var oc = exutil.NewCLI("mco-<cli-name>", exutil.KubeConfigPath())

	g.JustBeforeEach(func() {
		PreChecks(oc)
	})

	g.It("[PolarionID:<id>][OTP] <description> [Disruptive]", func() {
		// test body
	})
})
```

- `[Serial][Disruptive]` is **always required** on Describe blocks
- `<cli-name>`: suite name hyphenated (e.g., `mco-security`)

## Existing File Insertion

Insert new `g.It` block inside the existing `g.Describe`, after the last test. Add new imports as needed. Do not modify existing code.

## Test Body Pattern

```go
g.It("[PolarionID:<id>][OTP] <description> [Disruptive]", g.Label("Platform:aws"), func() {
	testID := GetCurrentTestPolarionIDNumber()
	mcp := GetCompactCompatiblePool(oc.AsAdmin())
	node := mcp.GetSortedNodesOrFail()[0]

	exutil.By("<step 1 from spec>")
	// implementation
	logger.Infof("OK!\n")

	exutil.By("<step 2 from spec>")
	// implementation
	logger.Infof("OK!\n")
})
```

## Generation Rules

- Each spec step becomes an `exutil.By()` block
- Use existing helpers — prefer utility functions over raw oc commands
- Defer cleanup immediately after resource creation
- Resource names include test ID: `fmt.Sprintf("test-%s-mc", testID)`
- Node verification: `NewRemoteFile(node, path)` with gomega matchers
- Pool waiting after changes: `mcp.waitForComplete()`
- Platform labels: `g.Label("Platform:aws", "Platform:gce")` based on spec tags
- Feature gate labels: `g.Label("OCPFeatureGate:XXX")` if the spec requires it
- Skip functions for platform/architecture: `skipTestIfSupportedPlatformNotMatched(oc, AWSPlatform, GCPPlatform)`, `architecture.SkipNonAmd64SingleArch(oc)`
- If the test requires new YAML templates, create them in `test/extended-priv/testdata/files/` and reference with `SetMCOTemplate("<name>.yaml")`

## Platform Constants

| Platform | Constant | g.Label Value |
|---|---|---|
| AWS | `AWSPlatform` | `"Platform:aws"` |
| GCP | `GCPPlatform` | `"Platform:gce"` |
| Azure | `AzurePlatform` | `"Platform:azure"` |
| vSphere | `VspherePlatform` | `"Platform:vsphere"` |

## Suite Types

| Type | Describe Annotation |
|---|---|
| longduration | `[sig-mco][Suite:openshift/machine-config-operator/longduration][Serial][Disruptive]` |
| disruptive | `[sig-mco][Suite:openshift/machine-config-operator/disruptive][Serial][Disruptive]` |

## Path Mappings

| What | Path |
|---|---|
| Test files | `test/extended-priv/mco_<feature>.go` |
| Testdata/templates | `test/extended-priv/testdata/files/` |
| Utility functions | `test/extended-priv/util/` |
| Resource wrappers | `test/extended-priv/*.go` |

## Build & Verify

```bash
make machine-config-tests-ext
./_output/linux/amd64/machine-config-tests-ext list | grep <PolarionID>
```

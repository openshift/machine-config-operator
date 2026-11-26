---
name: MCO Automate Test Workflow
description: Conventions and workflow for generating new MCO e2e tests from test specifications, and for encapsulating/refactoring existing tests in test/extended-priv/. Use whenever writing, updating, or restructuring MCO extended-priv e2e tests, including requests to extract logic into helper functions or make a g.It body readable.
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

Known review patterns (from accumulated reviews) — check for these proactively before proposing any refactoring, they are recurring findings:

- **Trivial single-use wrappers**: functions of a couple of straight-line lines, called once, merely forwarding to an existing method — they add indirection without abstraction
- **Per-platform function splits**: `scaleUpAWSTestNode` / `scaleUpSingleTestNode`-style pairs with `if platform ==` left in the `g.It` — should be one intent-named function with the branch inside
- **Mechanism-named functions**: names describing how instead of what (`scaleUpAWSTestNode`) — name the intent (`scaleUpTestNode`)

## Helper Inventory (Reuse First — Before Writing Any Code)

Before generating or refactoring any test code, build an inventory of the helpers that already exist. Duplicated logic is the most common review finding on new tests: the author writes a fresh function (or inline block) for something the suite already provides. Checking first is cheap; rewriting after review is not.

1. List the available functions:

   ```bash
   grep -n "^func " test/extended-priv/*.go test/extended-priv/util/*.go
   ```

2. Read the method sets of the resource wrappers the test will touch (`MachineConfigPool`, `MachineConfig`, `Node`, `RemoteFile`, `ManagedMachineResource`, `MachineConfiguration`, `BareMetalHost`, ...) — most operations already exist as methods: `WaitForUpdatedStatus`, `WaitForPoolToBeSettled`, `ScaleTo`, `WaitUntilReady`, `DeleteWithWait`, `GetSortedNodesOrFail`, ...
3. Check the testdata templates in `test/extended-priv/testdata/files/` before creating a new YAML template.
4. If the target file already exists, its local helpers count as existing helpers too — reuse them before writing anything new.

Rules:

- A new helper function is a **last resort**: write one only when nothing existing can be used directly or composed from existing primitives
- Before finalizing, re-check every function you wrote against the inventory — if an existing helper does the same job, use it instead
- Report at the end which existing helpers were reused and which functions are new. This makes any duplication visible immediately instead of surfacing in code review

## Codebase Context

Before generating, read existing code to understand the patterns:

- The target file (if existing) — its Describe block, JustBeforeEach, variables, existing tests, and local helpers
- 2-3 similar test files based on topic
- Testdata templates in `test/extended-priv/testdata/files/`

## Conventions

These apply to **every** test you write or modify — creating a new file and inserting into an existing one follow the same rules. When inserting into an existing file they matter even more: the file already demonstrates reviewed patterns, and inconsistency is what reviewers notice first.

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
14. **Skip on SNO/Compact** when the test requires multiple nodes or a dedicated worker pool: `if IsCompactOrSNOCluster(oc.AsAdmin()) { g.Skip("...") }` or `exutil.SkipOnSingleNodeTopology(oc.AsAdmin())`

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

All Conventions, Generation Rules, and readability rules below apply to inserted tests exactly as they do to new files — an insertion is not a license to skip the helper inventory or write inline logic.

If the test requires new helper functions, append them **after all existing functions** at the end of the file — never insert between existing functions. Helper functions belong outside the `g.Describe` block, after its closing `})`.

## Test Body Pattern

```go
g.It("[PolarionID:<id>][OTP] <description> [Disruptive]", g.Label("Platform:aws"), func() {
 testID := GetCurrentTestPolarionIDNumber()
 mcp := GetCompactCompatiblePool(oc.AsAdmin())
 node := mcp.GetSortedNodesOrFail()[0]

 exutil.By("<step 1 from spec>")
 mc := createTestMachineConfig(oc, testID, mcp) // readability function: params in, resource out
 defer mc.DeleteWithWait()                       // defer stays in the body, over the local
 logger.Infof("OK!\n")

 exutil.By("<step 2 from spec>")
 verifyFileInNode(node, testID) // readability function
 logger.Infof("OK!\n")
})
```

## Test Readability: Local Variables + Readability Functions

The `g.It` body should describe the **nature of what is being tested**. A reader understands WHAT is verified without being bothered by HOW — platform mechanics, API plumbing, retry and wait loops. The body reads as step markers plus intent-named function calls, not implementation.

The unit of readability is the **readability function**: a private (lowercase), file-local function that exists only to make one test readable. It is *not* a helper and is *not* meant for reuse.

Rules:

- **The `g.It` owns the test's variables.** Declare them in the `var` block or as body locals. Data flow stays visible in the test body: the reader sees what is created, what is passed where, and what is cleaned up
- **Readability functions take explicit parameters and return values** — what they need comes in, what the body needs next comes out. No shared state
- **No context structs, no interfaces, no method receivers.** Do not bundle the test's variables into a struct to thread them through functions, and do not attach functions to a test type. Both hide data flow and invite accidental reuse
- **Do not design readability functions for generality.** They implement one step of one test. If another test wants similar logic, that is a conversation for a shared helper — not a reason to generalize
- **Defers stay in the `g.It` body**, registered over the returned resources. This keeps cleanup timing and LIFO order exactly where the reviewer can see it, and avoids rewriting plain `defer` into a different cleanup mechanism during refactoring
- Place readability functions after all existing functions at the end of the file, outside the `g.Describe` block

### When to extract — and when not to

A function must earn its indirection. Extract a phase when it hides real logic the reader does not need inline: branching, loops, waits, multi-step setup, verification sequences.

Do not create a function that is called exactly once and merely forwards to an existing method in a couple of straight-line lines — the reader jumps to it and learns nothing new. For example, wrapping `host.Pin(newMS)` plus its error check in a `pinBareMetalHostToMachineSet` function adds indirection without adding abstraction. Trivial wrappers do not break readability, they just do not help it — when in doubt, leave the logic inline.

### One function per intent — platform variation lives inside

When a step's implementation varies by platform (or any environment dimension), write ONE function named for the intent — `scaleUpTestNode(newMS, platform, probeNode)` — and put the branching inside it. The reader reads "scaleUpTestNode" and is not bothered by how each platform does it.

Do not split per-platform functions (`scaleUpAWSTestNode` / `scaleUpSingleTestNode`) with the `if platform ==` left in the `g.It` — that leaks mechanism into the orchestration.

Exception: when the platform branch changes the test's *flow* — a step that exists only on one platform and has its own `exutil.By` marker (e.g. an AWS-only probe step) — keep the branch visible in the `g.It`. It is part of what is being tested, not how.

Rough threshold: if a `g.It` body exceeds ~50 lines, or a single phase needs more than a few lines of real logic, extract that phase into a readability function.

Distinguish from shared helpers (see Helper Inventory): shared helpers are repo-wide reusable utilities with stable behavior; readability functions belong to a single test and carry its step names.

Reference examples in this repo: `cloneMachineSet` / `removeClonedMachineSet` / `SimpleScaleUPTest` in `mco_scale.go`, and `scaleDownAndDeleteTestMachineSet` in `mco_irreconcilablechanges.go` (teardown function taking everything it needs as parameters).

## Refactoring Existing Tests (Human-Gated)

When asked to encapsulate, restructure, or refactor an existing test, remember: the code already works and has been reviewed. A behavior change introduced by a "readability" refactor is the worst possible outcome — it silently breaks a passing test. The human stays in control on both sides of the work.

### Phase 1: Propose — do not touch code

1. Analyze the target test: steps (`exutil.By` markers), expectations, defers/cleanups, skips, and the variables shared between phases
2. Propose a refactoring plan following the **local variables + readability functions** pattern from the Test Readability section:
   - Every variable stays local to the `g.It` (`var` block or body locals)
   - Each extracted function is private and file-local, takes what it needs as explicit parameters, and returns what the body needs next
   - **No context structs, no test structs with methods, no interfaces, no method receivers** — do not bundle or encapsulate the test's state
   - Defers stay registered in the `g.It` body, over the returned locals — same mechanism, same order, same timing as before
   - State the granularity decisions explicitly: which phases get a function and which stay inline (trivial single-use logic stays inline), and where platform branching will live (inside the intent-named function, except marker-guarded steps). These are the choices the user is approving — make them visible
3. Present the plan and **stop. Do not modify any code until the user explicitly agrees to the plan.** A request like "encapsulate test X" is a request for a proposal, not pre-approval to change the code.

### Phase 2: Execute — only after explicit agreement

1. Perform the refactoring with behavior preserved 1:1: every step marker, expectation, defer, `SafeCleanup`, and skip stays, in the same order. Keep the original cleanup mechanism — do not rewrite plain `defer` into `g.DeferCleanup` or anything else
2. Follow the placement rules: readability functions appended after all existing functions at the end of the file, outside the `g.Describe` block; never insert between existing functions
3. Do not modify other tests in the file — keep the diff minimal and scoped
4. Verify: the code compiles (`make machine-config-tests-ext`) and all step markers are present in order

### Phase 3: Validate — human signs off

1. Present the diff and a function-by-function mapping (which lines/steps moved into which helper)
2. State explicitly what was verified (compilation, step markers, defers, skips) so the user knows what was checked and what still needs their eyes
3. The refactoring is done only when the user validates it. Do not commit or take any follow-on action before that.

## Addressing Review Comments

When asked to address code review comments on a test, the review-driven change follows the same rules as any other change — a review comment never overrides the conventions in this skill, it rides on top of them:

1. Parse each comment into a concrete change request. If a request conflicts with the skill's rules (e.g. a reviewer asking for a context struct, a per-platform split, or a trivial wrapper), flag the conflict to the user with your recommendation instead of silently complying
2. Apply the full rulebook: local variables in the `g.It`, readability functions with intent names, platform branching inside them, defers in the body, behavior preserved 1:1
3. The change is human-gated exactly like a refactoring: propose what you will change (including granularity decisions) and stop for approval; after executing, present the diff and mapping for validation
4. Check the known review patterns (see Code Review Learning) proactively — if the review reveals one in nearby code, mention it, but do not fix unrequested code without approval

## Generation Rules

- Each spec step becomes an `exutil.By()` block
- Use existing helpers — prefer utility functions over raw oc commands
- Defer cleanup immediately after resource creation
- Resource names include test ID: `fmt.Sprintf("test-%s-mc", testID)`
- Node verification: `NewRemoteFile(node, path)` with gomega matchers
- Pool waiting after config changes: `mcp.waitForComplete()`
- Pool waiting after scale operations: see **Scale Operations** section below
- Platform labels: `g.Label("Platform:aws", "Platform:gce")` based on spec tags
- Feature gate labels: `g.Label("OCPFeatureGate:XXX")` if the spec requires it
- Skip functions for platform/architecture: `skipTestIfSupportedPlatformNotMatched(oc, AWSPlatform, GCPPlatform)`, `architecture.SkipNonAmd64SingleArch(oc)`
- Skip on SNO/Compact: if the test needs more than one node or a dedicated worker pool, add `if IsCompactOrSNOCluster(oc.AsAdmin()) { g.Skip("This test requires multiple nodes and cannot run on SNO/Compact clusters") }` at the start of the test body
- If the test requires new YAML templates, create them in `test/extended-priv/testdata/files/` and reference with `SetMCOTemplate("<name>.yaml")`

## Post-Generation Verification

After generating the test code, verify completeness:

1. Extract every numbered step, precondition check, and expected result from the specification
2. Map each to an `exutil.By()` block in the generated code
3. Confirm 1:1 coverage — every spec step must have a corresponding `exutil.By()` block
4. Report any missing steps and add the missing `exutil.By()` blocks before finalizing

## Platform Constants

| Platform | Constant | g.Label Value |
|---|---|---|
| AWS | `AWSPlatform` | `"Platform:aws"` |
| GCP | `GCPPlatform` | `"Platform:gce"` |
| Azure | `AzurePlatform` | `"Platform:azure"` |
| vSphere | `VspherePlatform` | `"Platform:vsphere"` |
| BareMetal | `BaremetalPlatform` | `"Platform:baremetal"` |

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

## Scale Operations

When a test scales nodes up or down, you must capture the initial node count and use `WaitForPoolToBeSettled` to verify that the pool has fully settled (all machine counts match and conditions are correct).

1. **Capture the initial node count** before any scaling, in the `var` section when possible:

   ```go
   initialNodeCount := len(mcp.GetNodesOrFail())
   ```

2. **After scale-up**, wait for the pool to settle with the new expected count:

   ```go
   o.Expect(mcp.WaitForPoolToBeSettled(initialNodeCount + numNewNodes)).To(o.Succeed(),
       "MCP counts did not settle after scale-up.\n%s", mcp.PrettyString())
   ```

3. **In defer cleanups** for scale-down, use `SafeCleanup` and wait for the pool to settle back to the initial count:

   ```go
   defer SafeCleanup(func() {
       o.Expect(ms.ScaleTo(initialNumMsNodes)).NotTo(o.HaveOccurred(),
           "Error scaling down MachineSet %s", ms.GetName())
       o.Expect(mcp.WaitForPoolToBeSettled(initialNodeCount)).To(o.Succeed(),
           "MCP counts did not settle after scale-down.\n%s", mcp.PrettyString())
   })
   ```

## Build & Verify

```bash
make machine-config-tests-ext
./_output/linux/amd64/machine-config-tests-ext list | grep <PolarionID>
```

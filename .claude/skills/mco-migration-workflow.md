---
name: MCO Migration Workflow
description: Automated workflow for migrating MCO tests from openshift-tests-private to machine-config-operator
---

# MCO Migration Workflow Skill

This skill provides step-by-step implementation guidance for the complete MCO test migration workflow. It automates the process of porting test cases from `openshift-tests-private/test/extended/mco/` to `machine-config-operator/test/extended-priv/`.

## When to Use This Skill

Use this skill when executing the `/migrate-tests` command to automate the migration of MCO test cases between repositories.

## Prerequisites

- Go toolchain installed
- Git installed and configured
- Local clone of `openshift-tests-private` repository
- Local clone of `machine-config-operator` repository
- (Optional) Local clone of `origin` repository for compat_otp source code

## Overview

The migration is a **4-phase workflow** that collects configuration, analyzes source and destination code, executes the migration transformations, and verifies the result.

**ALL 4 PHASES ARE MANDATORY - EXECUTE EACH PHASE IN ORDER:**

1. User Input Collection (5 inputs)
2. Analysis (source/destination code analysis, dependency mapping)
3. Migration Execution (code transformation, file creation, template copying)
4. Verification (build, test listing, optional test run)

**Key Design Principles:**
- **Code preservation**: Do NOT simplify, refactor, or modify function logic - only change references
- **Function order**: Write functions in the same order as the original source file
- **File naming**: Use the same file names as the original for compat_otp utility functions
- **Duplicate detection**: Skip tests and functions already present in destination, and warn about tests being migrated in open PRs
- **Accurate name transformation**: Follow the precise test naming algorithm documented below

## Migration Phases

### Phase 1: User Input Collection (5 inputs)

Collect all necessary information from the user before starting the migration.

**CRITICAL INSTRUCTIONS:**
- Ask each input explicitly using AskUserQuestion tool or direct prompts
- WAIT for user response before proceeding to the next input
- Validate paths exist before accepting them

#### Input 1: Source Repository Path

Ask: "What is the path to your `openshift-tests-private` repository?"

**Validation:**
```bash
# Verify the MCO test directory exists
if [ ! -d "$SOURCE_REPO/test/extended/mco" ]; then
    echo "ERROR: Cannot find test/extended/mco/ in the provided path"
    echo "Please provide the root path of the openshift-tests-private repository"
    exit 1
fi
```

**Store in variable:** `<source-repo>`

**Set derived paths:**
```bash
SOURCE_TEST_DIR="$SOURCE_REPO/test/extended/mco"
SOURCE_TESTDATA_DIR="$SOURCE_REPO/test/extended/testdata/mco"
```

#### Input 2: Destination Repository Path

Ask: "What is the path to your `machine-config-operator` repository?"

**Validation:**
```bash
# Verify the extended-priv test directory exists
if [ ! -d "$DEST_REPO/test/extended-priv" ]; then
    echo "ERROR: Cannot find test/extended-priv/ in the provided path"
    echo "Please provide the root path of the machine-config-operator repository"
    exit 1
fi
```

**Store in variable:** `<dest-repo>`

**Set derived paths:**
```bash
DEST_TEST_DIR="$DEST_REPO/test/extended-priv"
DEST_TESTDATA_DIR="$DEST_REPO/test/extended-priv/testdata/files"
DEST_UTIL_DIR="$DEST_REPO/test/extended-priv/util"
```

#### Input 3: compat_otp Library Path (Optional)

Ask: "What is the path to the compat_otp library? (press Enter to skip)
This is typically at: `<origin-repo>/test/extended/util/compat_otp/`
It is needed if the source tests use compat_otp sub-package functions (architecture, clusterinfra, logext, bootstrap) that don't already exist in the destination."

**Store in variable:** `<compat-otp-path>` (empty if skipped)

#### Input 4: Migration Target

First, list available source test files:

```bash
echo "Available test files in $SOURCE_TEST_DIR:"
ls -1 "$SOURCE_TEST_DIR"/*.go | while read f; do
    filename=$(basename "$f")
    lines=$(wc -l < "$f")
    echo "  $filename ($lines lines)"
done
```

Then ask: "Choose migration mode:
a) **Whole file** - enter filename (e.g., `mco_configdrift.go`)
b) **Suite extraction from mco.go** - enter keyword to filter tests (e.g., `kernel`, `ssh`, `fips`)"

**Mode A: Whole File Migration**

If user provides a filename:
```bash
SOURCE_FILE="$SOURCE_TEST_DIR/$FILENAME"
if [ ! -f "$SOURCE_FILE" ]; then
    echo "ERROR: File not found: $SOURCE_FILE"
    exit 1
fi
MIGRATION_MODE="whole-file"
DEST_FILENAME="$FILENAME"
```

**Store in variables:**
- `<migration-mode>` = "whole-file"
- `<source-file>` = full path to source file
- `<dest-filename>` = same filename for destination

**Mode B: Suite Extraction from mco.go**

If user provides a keyword:

1. Search for matching tests in mco.go:
```bash
echo "Tests matching '$KEYWORD' in mco.go:"
grep -n 'g\.It("' "$SOURCE_TEST_DIR/mco.go" | grep -i "$KEYWORD" | nl -w2 -s'. '
```

2. Display the matching tests and ask user to confirm
3. The extracted tests will be placed in a new file named `mco_<keyword>.go`

**Store in variables:**
- `<migration-mode>` = "suite-extraction"
- `<source-file>` = path to mco.go
- `<extraction-keyword>` = the keyword used
- `<dest-filename>` = `mco_<keyword>.go`
- `<selected-tests>` = list of test names/line numbers to extract

#### Input 4b: Check for In-Flight Migration PRs

After the user selects the migration target, immediately check GitHub for open (unmerged) PRs that may already be migrating the same tests. This runs before the confirmation summary so the user can see conflicts before deciding to proceed.

```bash
# Extract PolarionIDs from the selected source tests
SOURCE_IDS=$(grep 'g\.It("' "$SOURCE_FILE" | grep -oP '\d{5,}' | sort -u)

# For each source PolarionID, search open PRs for references
for id in $SOURCE_IDS; do
    MATCHING_PRS=$(gh pr list --repo openshift/machine-config-operator --state open --search "PolarionID:$id" --json number,title,url,author --jq '.[] | "#\(.number) by @\(.author.login): \(.title) (\(.url))"')
    if [ -n "$MATCHING_PRS" ]; then
        echo "WARNING: PolarionID $id found in open PR(s):"
        echo "$MATCHING_PRS"
    fi
done
```

If `gh` is not available or the search fails (e.g., no network), log a warning and continue — this check is best-effort.

**Note:** The `gh pr list --search` query searches PR titles and bodies. For more thorough detection, also check PR diffs if the initial search returns no results but you suspect overlap:

```bash
# Fallback: search PR diffs directly (slower, use only if needed)
for pr_number in $(gh pr list --repo openshift/machine-config-operator --state open --json number --jq '.[].number'); do
    if gh pr diff --repo openshift/machine-config-operator "$pr_number" 2>/dev/null | grep -q "PolarionID:$id"; then
        echo "WARNING: PolarionID $id found in diff of PR #$pr_number"
    fi
done
```

Include any warnings in the confirmation summary (Input 5) so the user can decide whether to skip those tests or continue.

#### Input 5: Configuration Summary and Confirmation

Display all collected inputs for user review:

```text
========================================
MCO Migration Configuration Summary
========================================
Source Repository:   <source-repo>
Destination Repo:    <dest-repo>
compat_otp Path:     <compat-otp-path or "skipped">
Migration Mode:      <migration-mode>
Source File:         <source-file>
Destination File:    <dest-filename>
Tests to Migrate:    <count> test cases

⚠ Tests found in open PRs:
  - PolarionID:NNNNN → PR #XX by @author (title)
========================================
```

If there are tests found in open PRs, ask the user whether to:
- **Skip** those tests (recommended to avoid duplicate work)
- **Continue anyway** (e.g., if the existing PR is stale or will be closed)

Ask: "Proceed with migration? [Y/n]:"

### Phase 2: Analysis

Analyze source and destination code to prepare for migration.

#### Step 1: Read and Parse Source Test File

Read the source file and extract:

1. **Package declaration**: Confirm it is `package mco`
2. **Imports**: List all import statements, noting which use `compat_otp`
3. **Describe blocks**: Extract `g.Describe(...)` block signatures including tags
4. **It blocks**: Extract all `g.It(...)` test case names with line numbers
5. **Helper functions**: List all non-test functions defined in the file
6. **Template references**: Find all calls to `generateTemplateAbsolutePath()` or other testdata references

For suite extraction mode, also extract:
- The `g.JustBeforeEach` block (shared setup code)
- Variable declarations at the top of the Describe block
- Only the `g.It()` blocks matching the extraction keyword

#### Step 2: Read Destination Patterns

Read existing files in `<dest-repo>/test/extended-priv/` to understand:

1. **Import patterns**: Confirm the standard imports used:
   ```go
   exutil "github.com/openshift/machine-config-operator/test/extended-priv/util"
   logger "github.com/openshift/machine-config-operator/test/extended-priv/util/logext"
   ```

2. **Describe block format**: Confirm the standard format:
   ```go
   g.Describe("[sig-mco][Suite:openshift/machine-config-operator/longduration][Serial][Disruptive] MCO <SuiteName>", func() {
   ```

3. **It block format**: Confirm the standard format:
   ```go
   g.It("[PolarionID:NNNNN][OTP] Description", func() {
   ```

4. **Existing PolarionIDs**: Scan all destination test files for existing PolarionIDs to detect already-migrated tests:
   ```bash
   grep -roh '\[PolarionID:[0-9]*\]' "$DEST_TEST_DIR"/*.go | sort -u
   ```

#### Step 3: Check for Already-Migrated Tests

For each test in the source file, extract the numeric ID and check if it already exists in the destination:

```bash
# Extract IDs from source test names
SOURCE_IDS=$(grep 'g\.It("' "$SOURCE_FILE" | grep -oP '\d{5,}' | sort -u)

# Check against destination
for id in $SOURCE_IDS; do
    if grep -rq "PolarionID:$id" "$DEST_TEST_DIR"/*.go 2>/dev/null; then
        echo "SKIP: Test $id already migrated"
    fi
done
```

Report any tests that will be skipped due to already being migrated.

#### Step 4: Identify Template Files to Copy

Find all template file references in the source code:

```bash
# Find generateTemplateAbsolutePath calls
grep -oP 'generateTemplateAbsolutePath\("([^"]+)"\)' "$SOURCE_FILE" | \
    grep -oP '"([^"]+)"' | tr -d '"' | sort -u
```

For each referenced template file:
1. Check if it exists in `<source-repo>/test/extended/testdata/mco/`
2. Check if it already exists in `<dest-repo>/test/extended-priv/testdata/files/`
3. Mark for copying if it exists in source but not in destination

#### Step 5: Identify Helper Functions to Migrate

For each non-test function in the source file:
1. Extract the function signature
2. Search the destination for a function with the same name
3. If the function already exists in destination, skip it
4. If not, mark it for migration

Also check for helper functions in related source files (e.g., `util.go`, `resource.go`, `node.go` in the source mco/ directory) that are called by the migrated tests.

#### Step 6: Identify compat_otp Sub-Package Dependencies

Check if the source uses any compat_otp sub-packages:

```go
// Check for these import patterns:
"github.com/openshift/origin/test/extended/util/compat_otp/architecture"
"github.com/openshift/origin/test/extended/util/compat_otp/clusterinfra"
"github.com/openshift/origin/test/extended/util/compat_otp/logext"
"github.com/openshift/origin/test/extended/util/compat_otp/bootstrap"
```

For each sub-package used:
1. Check if the equivalent package exists in destination (`<dest-repo>/test/extended-priv/util/<subpackage>/`)
2. If it exists, just update the import path
3. If it doesn't exist, mark the required functions for migration from compat_otp source

### Phase 3: Migration Execution

Execute the migration transformations based on the analysis from Phase 2.

**CRITICAL RULES:**
- Do NOT simplify or refactor any function logic
- Migrate code as-is, only changing references
- Write functions in the same order as the original file
- For compat_otp functions, use the same file names as in compat_otp
- If a new file has to be created in destination, create it

#### Step 1: Transform Package Declaration

Change:
```go
package mco
```
To:
```go
package extended
```

#### Step 2: Transform Imports

Apply the complete import mapping:

| Source Import | Destination Import |
|---|---|
| `compat_otp "github.com/openshift/origin/test/extended/util/compat_otp"` | `exutil "github.com/openshift/machine-config-operator/test/extended-priv/util"` |
| `exutil "github.com/openshift/origin/test/extended/util"` | `exutil "github.com/openshift/machine-config-operator/test/extended-priv/util"` |
| `"github.com/openshift/origin/test/extended/util/compat_otp/architecture"` | `"github.com/openshift/machine-config-operator/test/extended-priv/util/architecture"` |
| `logger "github.com/openshift/origin/test/extended/util/compat_otp/logext"` | `logger "github.com/openshift/machine-config-operator/test/extended-priv/util/logext"` |
| `clusterinfra "github.com/openshift/origin/test/extended/util/compat_otp/clusterinfra"` | `clusterinfra "github.com/openshift/machine-config-operator/test/extended-priv/util/clusterinfra"` |
| `"github.com/openshift/origin/test/extended/util/compat_otp/bootstrap"` | `"github.com/openshift/machine-config-operator/test/extended-priv/util/bootstrap"` |

**Important:** Keep all other imports unchanged (e.g., `github.com/onsi/ginkgo/v2`, `github.com/onsi/gomega`, standard library imports).

Remove any imports that are no longer needed after transformation. Add any imports that are newly needed (e.g., if the destination uses `logger` but the source didn't import it).

#### Step 3: Transform Function References

Replace all function call prefixes throughout the code:

- `compat_otp.By(` -> `exutil.By(`
- `compat_otp.NewCLI(` -> `exutil.NewCLI(`
- `compat_otp.KubeConfigPath()` -> `exutil.KubeConfigPath()`
- `compat_otp.FixturePath(` -> `exutil.FixturePath(`
- Any other `compat_otp.` prefix -> `exutil.`

**Important:** Do NOT change function arguments or logic. Only change the package prefix.

#### Step 4: Transform Describe Block

Transform the `g.Describe()` block signature.

**Source format:**
```go
g.Describe("[sig-mco] MCO config drift", func() {
```

**Destination format:**
```go
g.Describe("[sig-mco][Suite:openshift/machine-config-operator/longduration][Serial][Disruptive] MCO config drift", func() {
```

**Suite tag selection:**
- Default: `[Suite:openshift/machine-config-operator/longduration]` for most MCO tests
- Use `[Suite:openshift/machine-config-operator/disruptive]` if the tests are specifically disruptive-only (check existing destination patterns for the same test area)
- Always include `[Serial][Disruptive]` after the suite tag (standard for MCO extended-priv tests)

**Preserve existing `g.Label(...)` decorators** if present:
```go
// Source:
g.Describe("[sig-mco] MCO ControlPlaneMachineSet", g.Label("Platform:gce", "Platform:aws", "Platform:azure"), func() {
// Destination:
g.Describe("[sig-mco][Suite:openshift/machine-config-operator/disruptive][Serial][Disruptive] MCO ControlPlaneMachineSet", g.Label("Platform:gce", "Platform:aws", "Platform:azure"), func() {
```

#### Step 5: Transform Test Names (g.It blocks)

This is the most critical transformation. Apply the following algorithm to each `g.It()` test name:

**Source pattern:**
```
Author:USERNAME-QUALIFIER1-QUALIFIER2-...-NNNNN-[PRIORITY][TAGS] Description [TRAILING_TAGS]
```

**Transformation algorithm:**

1. **Extract PolarionID**: Find the numeric ID (5+ digits) in the test name
   - Pattern: look for `-(\d{5,})-` in the name
   - Example: `Author:sregidor-Longduration-NonPreRelease-High-46943-[P1]...` -> ID = `46943`
   - If multiple IDs exist (e.g., `Critical-43048-Critical-43064`), use the first one as primary

2. **Extract description**: The text after the last `]` tag group following the ID, before any trailing `[Serial]`/`[Disruptive]`
   - Example: `...-46943-[P1][OnCLayer] Config Drift. Config file. [Serial]` -> Description = `Config Drift. Config file.`

3. **Handle special qualifiers:**
   - `ConnectedOnly` in source -> add `[Skipped:Disconnected]` tag in destination
   - `[OCPFeatureGate:XXX]` -> preserve as `[OCPFeatureGate:XXX]` in destination

4. **Remove qualifiers** (these are NOT carried to destination):
   - `Author:USERNAME`
   - `NonHyperShiftHOST`, `NonPreRelease`, `Longduration`, `LEVEL0`
   - `Critical`, `High`, `Medium`, `Low` (priority qualifiers in the Author prefix)
   - `[P1]`, `[P2]`, `[P3]` (priority tags)
   - `[OnCLayer]`
   - Trailing `[Serial]` (moved to Describe block)
   - Trailing `[Disruptive]` (moved to Describe block)

5. **Compose destination name:**
   ```
   [PolarionID:NNNNN][OTP] Description
   ```
   With optional additional tags:
   ```
   [PolarionID:NNNNN][OTP][Skipped:Disconnected] Description
   [PolarionID:NNNNN][OTP][OCPFeatureGate:XXX] Description
   ```

**Verified transformation examples:**

| Source Test Name | Destination Test Name |
|---|---|
| `Author:sregidor-Longduration-NonPreRelease-High-46943-[P1][OnCLayer] Config Drift. Config file. [Serial]` | `[PolarionID:46943][OTP] Config Drift. Config file.` |
| `Author:sregidor-NonHyperShiftHOST-NonPreRelease-Longduration-Medium-81917-[P1][OnCLayer] Pinned images when disk-pressure [Disruptive]` | `[PolarionID:81917][OTP] Pinned images when disk-pressure` |
| `Author:sregidor-ConnectedOnly-NonHyperShiftHOST-NonPreRelease-Longduration-High-81921-[OnCLayer] Pinned images with a ImageDigestMirrorSet mirroring a single repository [Disruptive]` | `[PolarionID:81921][OTP][Skipped:Disconnected] Pinned images with a ImageDigestMirrorSet mirroring a single repository` |
| `Author:sregidor-NonHyperShiftHOST-NonPreRelease-Medium-81955-[P2][OnCLayer] Pinnedimageset invalid pinned images [Disruptive]` | `[PolarionID:81955][OTP] Pinnedimageset invalid pinned images` |
| `Author:sserafin-NonHyperShiftHOST-NonPreRelease-Longduration-High-73648-[OnCLayer] A rebooted node reconciles with the pinned images status [Disruptive]` | `[PolarionID:73648][OTP] A rebooted node reconciles with the pinned images status` |

**Preserve `g.Label(...)` decorators on individual tests** if present:
```go
// Source:
g.It("Author:sregidor-...-81403-[P1] In BootImages Machineset should update by default", g.Label("Platform:aws", "Platform:gce"), func() {
// Destination:
g.It("[PolarionID:81403][OTP] In BootImages Machineset should update by default", g.Label("Platform:aws", "Platform:gce"), func() {
```

#### Step 6: Transform Step Logging

Replace test step logging calls:
- `compat_otp.By(` -> `exutil.By(`

No other changes needed. The `exutil.By()` function in the destination wraps `g.By()` similarly to `compat_otp.By()`.

#### Step 7: Migrate Helper Functions

For each helper function identified in Phase 2 Step 5:

1. **If the function already exists in destination**: Skip it entirely
2. **If the function does NOT exist in destination**:
   a. Determine the correct destination file:
      - If the function is in the same source test file -> place it in the same destination test file
      - If the function is in a shared helper file (e.g., `util.go`, `resource.go`, `node.go`) -> place it in the corresponding file in `<dest-test-dir>/`
   b. Copy the function body exactly as-is
   c. Only change `compat_otp.` references to `exutil.` and update imports
   d. Preserve the function order from the original file

#### Step 8: Migrate compat_otp Utility Functions

For each compat_otp sub-package function identified in Phase 2 Step 6:

1. **If the equivalent already exists in destination util/**: Skip it
2. **If it does NOT exist**:
   a. Read the function from the compat_otp source (using `<compat-otp-path>`)
   b. Create or append to the corresponding file in `<dest-repo>/test/extended-priv/util/` (or `util/<subpackage>/`)
   c. Use the same file name as in the compat_otp source
   d. Change only the package declaration and imports
   e. Preserve the function signature and implementation exactly

**If compat_otp path was not provided**: Check if the required functions already exist in destination. If not, report which functions need to be manually migrated.

#### Step 9: Copy Template/Testdata Files

For each template file identified in Phase 2 Step 4:

1. **If the file already exists in destination**: Skip it
2. **If it does NOT exist**:
   ```bash
   cp "$SOURCE_TESTDATA_DIR/$TEMPLATE_FILE" "$DEST_TESTDATA_DIR/$TEMPLATE_FILE"
   ```

After copying, verify the file was copied correctly:
```bash
if [ -f "$DEST_TESTDATA_DIR/$TEMPLATE_FILE" ]; then
    echo "Copied: $TEMPLATE_FILE"
else
    echo "ERROR: Failed to copy $TEMPLATE_FILE"
fi
```

**Important:** After adding new testdata files, the embedded testdata assets may need regeneration. Check if the destination has a `go:embed` directive or asset generation step and follow the existing pattern.

#### Step 10: Write the Destination File

Assemble the transformed code and write it to the destination:

```bash
DEST_FILE="$DEST_TEST_DIR/$DEST_FILENAME"
```

**For whole-file migration:** Write the entire transformed file to `$DEST_FILE`

**For suite extraction:** Create a new file containing:
1. The transformed package declaration and imports
2. The `g.Describe()` block with appropriate tags
3. The `g.JustBeforeEach()` setup block (copied from source)
4. Only the selected `g.It()` test blocks
5. Any helper functions used exclusively by the selected tests

#### Step 11: Format the Code

Run goimports to fix import ordering and formatting:

```bash
cd "$DEST_REPO"
goimports -w "$DEST_FILE"
```

If `goimports` is not available, use `gofmt`:
```bash
gofmt -w "$DEST_FILE"
```

### Phase 4: Verification

Verify the migration was successful.

#### Step 1: Build the Test Binary

```bash
cd "$DEST_REPO"
make machine-config-tests-ext
```

If `make` fails, try direct build:
```bash
go build -o machine-config-tests-ext ./cmd/machine-config-tests-ext/
```

**On build failure:**
1. Read the error message carefully
2. Common issues and fixes:
   - **Missing function**: A helper function used by the migrated test wasn't migrated. Find it in the source and migrate it.
   - **Missing import**: An import was not properly transformed. Check the import mapping table.
   - **Duplicate function**: A function was migrated that already exists. Remove the duplicate.
   - **Missing testdata file**: A template file wasn't copied. Copy it from source testdata.
   - **Type mismatch**: A compat_otp type is different from exutil type. Check the destination util package for the correct type.
3. Fix the issue and retry the build

**Iterate** until the build succeeds. Report each error and fix to the user.

#### Step 2: Verify Test Listing

After successful build, verify the migrated tests appear in the listing:

```bash
# List all tests and search for migrated PolarionIDs
for id in $MIGRATED_IDS; do
    echo "Checking PolarionID:$id..."
    ./_output/linux/amd64/machine-config-tests-ext list | grep "$id"
done
```

**Expected output:** Each migrated test should appear with the correct name format:
```text
[sig-mco][Suite:openshift/machine-config-operator/longduration][Serial][Disruptive] MCO config drift [PolarionID:46943][OTP] Config Drift. Config file.
```

If a test is missing from the listing, check:
1. The test file is in the correct package (`package extended`)
2. The `g.Describe` and `g.It` blocks are properly formed
3. The file is in `test/extended-priv/` directory

#### Step 3: Optional Test Execution

Ask the user: "Would you like to run a specific migrated test? This requires a KUBECONFIG pointing to a running cluster. [y/N]:"

If yes:

```bash
export KUBECONFIG=<user-provided-kubeconfig-path>

# Show available migrated tests
echo "Migrated tests:"
for id in $MIGRATED_IDS; do
    ./_output/linux/amd64/machine-config-tests-ext list | grep "$id"
done

# Ask user to select a test
echo "Enter the full test name to run (copy from above):"
```

Run the selected test:
```bash
./_output/linux/amd64/machine-config-tests-ext run-test "$TEST_NAME"
```

#### Step 4: Migration Summary

Provide a comprehensive summary:

```text
========================================
MCO Migration Summary
========================================

Files Created/Modified:
  - <dest-test-dir>/<dest-filename> (NEW)
  - <dest-testdata-dir>/<template1>.yaml (COPIED)
  - <dest-testdata-dir>/<template2>.yaml (COPIED)
  - <dest-util-dir>/<util-file>.go (MODIFIED - added functions)

Tests Migrated:
  1. [PolarionID:NNNNN][OTP] Test description 1
  2. [PolarionID:NNNNN][OTP] Test description 2
  ...

Tests Skipped (already migrated):
  - PolarionID:XXXXX

Build: PASSED
Test Listing: ALL MIGRATED TESTS FOUND

Next Steps:
  1. Review the migrated code in <dest-filename>
  2. Run the tests against a cluster:
     export KUBECONFIG=/path/to/kubeconfig
     ./_output/linux/amd64/machine-config-tests-ext run-test "<test-name>"
  3. Commit the changes when satisfied
========================================
```

## Troubleshooting

### Build Error: undefined function

A helper function used by the test was not migrated. Search for the function in the source repository:

```bash
grep -rn "func FunctionName" "$SOURCE_TEST_DIR/"
```

Migrate the function to the corresponding file in the destination.

### Build Error: duplicate function

A function was migrated that already exists in the destination. Remove the duplicate from the newly created file.

### Build Error: import cycle

An import creates a cycle. Check that utility functions are placed in the correct package (`util/` vs the test file itself).

### Test not listed after build

Check that:
1. The file uses `package extended`
2. The `g.Describe()` block uses `var _ = g.Describe(...)` (init-time registration)
3. The file is saved in `test/extended-priv/`

### Template file not found at runtime

Ensure:
1. The template file was copied to `test/extended-priv/testdata/files/`
2. The `generateTemplateAbsolutePath()` function resolves to the correct embedded path
3. If using `go:embed`, the embed directive covers the new file

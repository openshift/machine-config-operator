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

The migration is a **multi-phase workflow** that collects configuration, analyzes source and destination code, executes the migration transformations, and verifies the result.

**EXECUTE EACH PHASE IN ORDER:**

1. User Input Collection (5 inputs)
2. Analysis (source/destination code analysis, dependency mapping)
3. Migration Execution (code transformation, file creation, template copying)
4. Verification (build, test listing)

**Key Design Principles:**
- **Code preservation**: Do NOT simplify, refactor, or modify function logic - only change references
- **IMPORTANT — Function order**: All migrated functions in the destination file must be placed in the same order as they appear in the source file, not appended at the end
- **File naming**: Use the same file names as the original for compat_otp utility functions
- **Duplicate detection**: Skip tests and functions already present in destination
- **Accurate name transformation**: Follow the precise test naming algorithm documented below

## Migration Phases

### Phase 1: User Input Collection (5 inputs)

Collect all necessary information from the user before starting the migration.

**CRITICAL INSTRUCTIONS:**
- Ask each input explicitly using AskUserQuestion tool or direct prompts
- WAIT for user response before proceeding to the next input
- Validate paths exist before accepting them
- Check for a memory file named `migrate_tests_config.md` in the project memory directory — if it exists, read the saved paths and offer them as defaults so the user can press Enter to reuse them
- After paths are confirmed, run `git pull` on the source and destination repos to ensure you have the latest test cases and already-migrated tests before proceeding

#### Input 1: Source Repository Path

If a saved source path exists in memory, ask: "What is the path to your `openshift-tests-private` repository? (last used: `<saved-path>`, type `y` to reuse)"

Otherwise ask: "What is the path to your `openshift-tests-private` repository?"

**Handling the response:** If the user responds with `y`, `yes`, or any affirmative AND a saved path exists in memory, use the saved path as the value and proceed to Input 2.

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

**Save to memory immediately:** After validation passes, write (or update) the `migrate_tests_config.md` memory file with the source_repo path. If the memory file doesn't exist yet, create it with the full frontmatter. If it already exists, update only the `source_repo` line under `## Last-Used Paths`. Also ensure `MEMORY.md` has an entry pointing to this file.

#### Input 2: Destination Repository Path

If a saved destination path exists in memory, ask: "What is the path to your `machine-config-operator` repository? (last used: `<saved-path>`, type `y` to reuse)"

Otherwise ask: "What is the path to your `machine-config-operator` repository?"

**Handling the response:** If the user responds with `y`, `yes`, or any affirmative AND a saved path exists in memory, use the saved path as the value and proceed to Input 3.

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

**Save to memory immediately:** After validation passes, update the `migrate_tests_config.md` memory file with the dest_repo path under `## Last-Used Paths`.

#### Input 3: compat_otp Library Path (Optional)

If a saved compat_otp path exists in memory, ask: "What is the path to the compat_otp library? (last used: `<saved-path>`, type `y` to reuse, or type `skip` to skip)"

Otherwise ask: "What is the path to the compat_otp library? (type `skip` to skip)
This is typically at: `<origin-repo>/test/extended/util/compat_otp/`
It is needed if the source tests use compat_otp sub-package functions (architecture, clusterinfra, logext, bootstrap) that don't already exist in the destination."

**Handling the response:** If the user responds with `y`, `yes`, or any affirmative AND a saved path exists in memory, use the saved path. If the user responds with `skip`, set the value to empty/skipped.

**Store in variable:** `<compat-otp-path>` (empty if skipped)

**Save to memory immediately:** After the user provides a path (or skips), update the `migrate_tests_config.md` memory file with the compat_otp_path under `## Last-Used Paths`. If the user skipped, save `compat_otp_path: skipped`.

#### Input 3b: Sync Repositories

Once all paths are confirmed, pull the latest changes so the migration works with up-to-date code:

```bash
echo "Syncing repositories to latest..."

# Pull latest in source repo
cd "$SOURCE_REPO" && git pull --ff-only 2>/dev/null || echo "WARNING: Could not pull source repo (may have local changes)"

# Pull latest in destination repo
cd "$DEST_REPO" && git pull --ff-only 2>/dev/null || echo "WARNING: Could not pull destination repo (may have local changes)"
```

This ensures:
- Source repo has the latest test cases available for migration
- Destination repo has the latest already-migrated tests for accurate duplicate detection

If `git pull` fails (e.g., uncommitted local changes), warn the user but continue — they may be working on a branch intentionally.

#### Input 4: Migration Target

Ask the user directly: "What do you want to migrate? Enter:
- A **filename** to migrate the whole file (e.g., `mco_configdrift.go`)
- A **filename:keyword** to extract matching tests from a file (e.g., `mco.go:kernel`, `mco_security.go:cipher`)"

**Validate the file exists:**
```bash
# Extract filename (before the colon, if present)
FILENAME=$(echo "$USER_INPUT" | cut -d: -f1)
SOURCE_FILE="$SOURCE_TEST_DIR/$FILENAME"
if [ ! -f "$SOURCE_FILE" ]; then
    echo "ERROR: File not found: $SOURCE_FILE"
    # Re-ask the user
fi
```

**Determine migration mode:**

If input contains a colon (`filename:keyword`):
```
MIGRATION_MODE="suite-extraction"
EXTRACTION_KEYWORD=$(echo "$USER_INPUT" | cut -d: -f2)
```

Derive a **suggested** destination filename:
- If source is `mco.go`: suggest `mco_<keyword>.go`
- If source is `mco_<topic>.go`: suggest `mco_<topic>_<keyword>.go`

Then ask the user: "The extracted tests will be saved as `<suggested-filename>`. Enter a custom filename or press Enter to accept:"

**Handling the response:** If the user presses Enter (empty response), use the suggested filename. Otherwise, use the user-provided filename. Ensure the filename ends with `.go` — append it if missing.

Show matching tests **grouped by functionality**. For each matching test:
1. Extract the PolarionID and plain-text description (strip Author/qualifier prefix and tags)
2. Group tests that share a common action or subject (e.g., "add kernel argument", "switch kernel type", "reject MC with kernel")
3. Present them grouped so the user can see the functional areas at a glance:

```text
Found 9 tests matching "kernel" in mco.go:

  Kernel arguments:
    1. 42365 - add real time kernel argument
    2. 42364 - add selinux kernel argument
    3. 67825 - Use duplicated kernel arguments
    4. 54922 - daemon: add check before updating kernelArgs
    5. 72136 - Reject MCs with ignition containing kernelArguments

  Kernel type (64k-pages):
    6. 67787 - switch kernel type to 64k-pages for arm64
    7. 67788 - kernel type 64k-pages not supported on non-arm64
    8. 67790 - create MC with extensions, 64k-pages kernel type and kernel argument

  FIPS + kernel:
    9. 53668 - FIPS and realtime kernel both enabled, node should NOT be degraded
```

The grouping is done by reading the test descriptions and clustering by shared keywords/subject matter. This is best-effort — the goal is to help the user understand what the tests cover, not to create a perfect taxonomy.

If no input contains a colon (whole file):
```
MIGRATION_MODE="whole-file"
DEST_FILENAME="$FILENAME"
```

**Store in variables:**
- `<migration-mode>` = "whole-file" or "suite-extraction"
- `<source-file>` = full path to source file
- `<dest-filename>` = destination filename
- `<extraction-keyword>` = keyword (suite extraction only)
- `<selected-tests>` = list of test names/line numbers to extract (suite extraction only)

#### Input 4b: Analyze the selected target

Once the user has selected a file (and optionally a keyword), run targeted checks on **only that file**:

1. **Check which tests are already migrated:**
   ```bash
   DEST_IDS=$(grep -roh 'PolarionID:[0-9]*' "$DEST_TEST_DIR"/*.go 2>/dev/null | grep -oP '\d+' | sort -u)
   SOURCE_IDS=$(grep 'g\.It("' "$SOURCE_FILE" | grep -oP '\d{5,}' | sort -u)
   # For suite extraction, filter to only tests matching the keyword
   ```

   For each source PolarionID, check if it exists in `$DEST_IDS`. Report skipped tests.

2. **Display summary for the selected target:**
   ```text
   <filename> — <total> tests, <done> already migrated, <available> ready to migrate
   ```

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
Tests Skipped:       <count> (already migrated)
```

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
- **IMPORTANT**: All migrated functions in the destination file must be placed in the same order as they appear in the source file, not appended at the end
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
- `[Serial][Disruptive]` after the suite tag is **always required** for MCO extended-priv tests. In the old framework `[Disruptive]` implied `[Serial]`, but in the new framework it does not — both must be explicitly specified.

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
```text
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
   - Trailing `[Serial]` (stripped — the Describe block already includes this tag)
   - Trailing `[Disruptive]` (stripped — the Describe block already includes this tag)

5. **Compose destination name:**
   ```text
   [PolarionID:NNNNN][OTP] Description
   ```
   With optional additional tags:
   ```text
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
   d. **IMPORTANT**: Preserve the migrated function order from the original source file

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

Destination Repo Changes (machine-config-operator):
  - <dest-test-dir>/<dest-filename> (NEW)
  - <dest-testdata-dir>/<template1> (COPIED)
  - <dest-testdata-dir>/<template2> (COPIED)
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
  1. Review the migrated code
  2. (Optional) Run the tests against a cluster:
     export KUBECONFIG=/path/to/kubeconfig
     ./_output/linux/amd64/machine-config-tests-ext run-test "<test-name>"
========================================
```

#### Step 5: Save Migration History to Memory

After a successful migration, update the `migrate_tests_config.md` memory file with the migration history. The `## Last-Used Paths` section is already up-to-date (paths are saved immediately during Phase 1 as each path is entered). Only update the migration history sections:

```markdown
## Already Migrated PolarionIDs
<list all PolarionIDs currently in dest-repo test files, including ones just migrated>

## Last Migration
- date: <current date>
- file: <dest-filename>
- tests migrated: <list of PolarionIDs migrated in this run>
```

On subsequent runs, this memory provides:
- Saved paths so the user can press Enter to reuse them (saved immediately during Input 1/2/3)
- A cached list of already-migrated PolarionIDs for faster duplicate detection
- Context about what was last migrated and when

**Important:** Always run `git pull` (Input 3b) and re-scan destination files (Phase 2 Step 3) even if memory has cached PolarionIDs — the memory is a hint to speed things up, not a substitute for checking current state.

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

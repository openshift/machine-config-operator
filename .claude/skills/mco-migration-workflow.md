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
- Check for a memory file named `migrate_tests_config.md` in the project memory directory — if it exists, read the saved paths and offer them as defaults so the user can press Enter to reuse them
- After paths are confirmed, run `git pull` on the source and destination repos to ensure you have the latest test cases and already-migrated tests before proceeding

#### Input 1: Source Repository Path

If a saved source path exists in memory, ask: "What is the path to your `openshift-tests-private` repository? (last used: `<saved-path>`, press Enter to reuse)"

Otherwise ask: "What is the path to your `openshift-tests-private` repository?"

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

If a saved destination path exists in memory, ask: "What is the path to your `machine-config-operator` repository? (last used: `<saved-path>`, press Enter to reuse)"

Otherwise ask: "What is the path to your `machine-config-operator` repository?"

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

If a saved compat_otp path exists in memory, ask: "What is the path to the compat_otp library? (last used: `<saved-path>`, press Enter to reuse, or type `skip` to skip)"

Otherwise ask: "What is the path to the compat_otp library? (press Enter to skip)
This is typically at: `<origin-repo>/test/extended/util/compat_otp/`
It is needed if the source tests use compat_otp sub-package functions (architecture, clusterinfra, logext, bootstrap) that don't already exist in the destination."

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

#### Input 4: Migration Dashboard

Instead of asking the user to guess filenames or keywords, build a live dashboard by scanning source and destination repos at runtime.

**Step 4a: Collect all PolarionIDs from destination (already migrated)**

```bash
DEST_IDS=$(grep -roh 'PolarionID:[0-9]*' "$DEST_TEST_DIR"/*.go 2>/dev/null | grep -oP '\d+' | sort -u)
```

**Step 4b: Collect in-flight PolarionIDs from open PRs (best-effort)**

If `gh` CLI is available, check open PRs on `openshift/machine-config-operator` for PolarionID references:

```bash
IN_FLIGHT=""
if command -v gh &>/dev/null; then
    for pr_num in $(gh pr list --repo openshift/machine-config-operator --state open --json number --jq '.[].number' 2>/dev/null); do
        PR_BODY=$(gh pr view --repo openshift/machine-config-operator "$pr_num" --json body --jq '.body' 2>/dev/null)
        for id in $(echo "$PR_BODY" | grep -oP 'PolarionID:\K\d+' | sort -u); do
            IN_FLIGHT="$IN_FLIGHT $id:#$pr_num"
        done
    done
fi
```

If `gh` is not available or fails, log a warning and continue without PR data.

**Step 4c: Scan each source test file and classify tests**

For each `mco*.go` file in `$SOURCE_TEST_DIR` that contains `g.It(` calls:

1. Extract all PolarionIDs from the file:
   ```bash
   grep 'g\.It("' "$FILE" | grep -oP '\d{5,}' | sort -u
   ```

2. For each PolarionID, classify as:
   - **Done** — ID found in `$DEST_IDS`
   - **In-PR** — ID found in `$IN_FLIGHT` (note which PR#)
   - **Available** — ID not in either list

3. Count total, done, in-PR, and available tests for the file
4. Skip files where available count is 0 (fully migrated)

**Step 4d: Discover topic keywords for suite extraction candidates**

For files with **6 or more available tests**, dynamically discover topic groupings:

1. For each available (non-migrated) test in the file, extract the description:
   - Strip the `Author:*-NNNNN-` prefix and all `[tag]` brackets
   - Take the remaining plain-text description
2. Tokenize descriptions into lowercase words
3. Filter out stop words: `the`, `a`, `an`, `is`, `in`, `on`, `to`, `for`, `with`, `and`, `or`, `of`, `should`, `not`, `be`, `if`, `when`, `that`, `it`, `as`, `by`
4. Count how many tests each remaining word appears in
5. Keep words that appear in **2 or more** available tests as topic keywords
6. Sort by test count descending
7. Tests not matching any discovered topic go into an "other" group

**Step 4e: Display the dashboard**

Present the results to the user as a numbered table with two sections:

**Section 1: Whole-file migration options**
- List each source test file as a numbered row
- Show columns: file name, total tests, done, in-PR, available
- Mark files with 0 available as "DONE"
- Only show files that contain `g.It(` calls (skip pure helper files)

**Section 2: Suite extraction options**
- For each file with 6+ available tests, show the discovered topic keywords
- Show columns: topic keyword, total matching tests, done, in-PR, available
- Each topic is labeled with a letter (a, b, c, ...)

Include a legend explaining: Done = already in destination, In-PR = in an open PR, Available = ready to migrate

**Step 4f: User selection**

Ask: "Enter your choice:
- A **number** to migrate a whole file
- A **file.topic** (e.g., `mco.kernel`) to extract a test suite by topic
- Or type a **custom keyword** to search across all files"

**If user picks a number (whole file):**
```
MIGRATION_MODE="whole-file"
SOURCE_FILE="<path to selected file>"
DEST_FILENAME="<same filename>"
```

**If user picks a file.topic (suite extraction):**
```
MIGRATION_MODE="suite-extraction"
SOURCE_FILE="<path to the file>"
EXTRACTION_KEYWORD="<topic keyword>"
```

Derive the destination filename:
- If source is `mco.go`: destination is `mco_<keyword>.go`
- If source is `mco_<topic>.go`: destination is `mco_<topic>_<keyword>.go`

Show the matching tests and ask user to confirm:
```bash
grep -n 'g\.It("' "$SOURCE_FILE" | grep -i "$KEYWORD" | nl -w2 -s'. '
```

**If user types a custom keyword:**
Search across all source files for matching tests:
```bash
for f in "$SOURCE_TEST_DIR"/mco*.go; do
    matches=$(grep -c "g\.It(\".*$KEYWORD" "$f" 2>/dev/null || true)
    if [ "$matches" -gt 0 ]; then
        echo "$(basename $f): $matches matching tests"
        grep -n 'g\.It("' "$f" | grep -i "$KEYWORD"
    fi
done
```

If matches span multiple files, ask the user which file to extract from, then proceed as suite extraction.

**Store in variables:**
- `<migration-mode>` = "whole-file" or "suite-extraction"
- `<source-file>` = full path to source file
- `<dest-filename>` = destination filename
- `<extraction-keyword>` = keyword (suite extraction only)
- `<selected-tests>` = list of test names/line numbers to extract (suite extraction only)

#### Input 4g: Warn about in-flight conflicts

If any of the selected tests have PolarionIDs that were detected as in-flight (from Step 4b), display warnings:

```text
WARNING: The following tests are already being migrated in open PRs:
  - PolarionID:NNNNN → PR #XX
  - PolarionID:NNNNN → PR #YY
```

Include these warnings in the confirmation summary (Input 5) so the user can decide whether to skip those tests or continue.

#### Input 5: Configuration Summary and Confirmation

Display all collected inputs for user review, including any in-flight PR warnings from Step 4g:

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

If any in-flight conflicts were detected in Step 4g, include them here and ask the user whether to:
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

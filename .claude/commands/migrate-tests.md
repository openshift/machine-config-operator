---
description: Automate MCO test migration from openshift-tests-private to machine-config-operator
argument-hint: ""
---

## Name

migrate-tests

## Synopsis

```bash
/migrate-tests
```

## Description

The `/migrate-tests` command automates the migration of MCO (Machine Config Operator) test cases from the `openshift-tests-private` repository to the `machine-config-operator` repository. It handles all transformations required to port tests between these repositories, including package renaming, import rewriting, test name reformatting, template file copying, and utility function migration.

**What it does:**

1. Collects source, destination, and migration target configuration
2. Analyzes source test files and destination patterns
3. For suite extraction: optionally mirrors changes in `openshift-tests-private` (creates extracted file, removes tests from original, commits and opens PR)
4. Transforms and migrates test code (package, imports, naming, references)
5. Migrates helper functions and compat_otp utilities
6. Copies referenced template/testdata files
7. Builds the test binary and verifies migrated tests appear
8. Commits, pushes, and creates PR in `machine-config-operator`
9. Optionally runs a specific test against a cluster

**Key Features:**

- **Two migration modes** - Migrate a whole test file (e.g., `mco_configdrift.go`) or extract a test suite from any file by keyword (e.g., `mco.go:kernel`)
- **Source repo mirroring** - For suite extraction, optionally creates the same new file in `openshift-tests-private` and removes extracted tests from the original file. The source repo PR is created first, then the MCO migration PR follows.
- **Accurate test name transformation** - Converts `Author:USERNAME-Qualifiers-ID-[Tags] Description` format to `[PolarionID:ID][OTP] Description` format
- **Import rewriting** - Replaces all `compat_otp` references with `exutil` equivalents
- **Targeted analysis** - Only scans the selected file for duplicates and open PR conflicts, keeping the workflow fast
- **Duplicate detection** - Skips tests already present in destination and warns about tests being migrated in open (unmerged) PRs on GitHub
- **Code preservation** - Migrates code as-is without simplification or refactoring
- **Template migration** - Copies referenced testdata YAML files to the correct destination path
- **Build verification** - Compiles the binary and verifies migrated tests are listed

## Implementation

**IMPORTANT: Use the MCO Migration Workflow skill for implementation.**

This command uses the `mco-migration-workflow` skill which provides detailed step-by-step implementation guidance for all phases of the migration.

To execute this command:

1. **Invoke the skill** to get detailed implementation instructions:
   - The skill is located at: `.claude/skills/mco-migration-workflow.md`
   - Follow the skill's workflow phases exactly as documented

2. **The workflow phases are:**
   - **Phase 1**: User Input Collection (6 inputs - source repo, dest repo, compat_otp path, migration target, confirmation, source repo mirroring)
   - **Phase 2**: Analysis (read source/dest files, identify dependencies, map test names)
   - **Phase 3A**: Source Repository Mirroring (suite extraction only — extract tests into new file in openshift-tests-private, remove from original, commit and PR)
   - **Phase 3B**: MCO Migration Execution (transform and write code, copy templates, migrate utilities)
   - **Phase 4**: Verification (build binary, list tests, optional test run, summary)

3. **Critical implementation notes:**
   - Do NOT simplify or refactor migrated code - only change references
   - Write functions in the same order as the original file
   - For compat_otp functions, use the same file names as the original
   - Check for existing functions in destination before creating duplicates
   - `ConnectedOnly` in source maps to `[Skipped:Disconnected]` in destination
   - Trailing `[Disruptive]` and `[Serial]` tags move from `g.It()` to the `g.Describe()` block

**For complete implementation details, transformation rules, and verification steps, refer to the skill documentation.**

## Return Value

**Format**: Migration summary with verification results

**On success:**

- Source repo changes and PR URL (if source mirroring was enabled)
- Destination repo changes, branch name, and PR URL
- List of all tests migrated with their new names
- Build verification result (pass/fail)
- Test listing verification showing migrated tests
- Next steps for optional test execution

**On failure:**

- Clear error message indicating which phase failed
- For build failures: error analysis and suggested fixes (usually missing imports or utility functions)
- Guidance on how to resolve the issue and retry

## Examples

### Migrate a whole test file

```bash
/migrate
```

The command will:

1. Ask for the source repository path (openshift-tests-private)
2. Ask for the destination repository path (machine-config-operator)
3. Ask for the compat_otp library path (optional)
4. Ask what to migrate (filename or filename:keyword)
5. Analyze only the selected file for duplicates and open PR conflicts
6. Display configuration summary for confirmation
7. Create a new branch in the destination repo
8. Execute the migration transformations
9. Build and verify the test binary
10. Commit, push, and create PR in machine-config-operator
11. Provide summary and next steps

### Example interaction — whole file

```text
User: /migrate-tests

AI: What is the path to your openshift-tests-private repository?
User: /home/user/repos/openshift-tests-private

AI: What is the path to your machine-config-operator repository?
User: /home/user/repos/machine-config-operator

AI: What is the path to the compat_otp library? (press Enter to skip)
User: /home/user/repos/origin/test/extended/util/compat_otp

AI: What do you want to migrate? Enter a filename (e.g., mco_configdrift.go)
    or filename:keyword to extract a subset (e.g., mco.go:kernel)
User: mco_configdrift.go

AI: mco_configdrift.go — 5 tests, 0 already migrated, 0 in open PRs, 5 available
    [shows confirmation summary]
    Proceed? [Y/n]
User: Y

AI: [executes migration]
    Migration complete! 5 tests migrated successfully.
    PR created: https://github.com/openshift/machine-config-operator/pull/XXX
```

### Example interaction — suite extraction

```text
AI: What do you want to migrate?
User: mco.go:kernel

AI: The extracted tests will be saved as mco_kernel.go.
    Enter a custom filename or press Enter to accept:
User: [presses Enter]

AI: Found 9 tests matching "kernel" in mco.go:
    1. 42365 - add real time kernel argument
    2. 67787 - switch kernel type to 64k-pages for arm64
    ...
    0 already migrated, 0 in open PRs, 9 available
    These will be extracted into mco_kernel.go. Proceed? [Y/n]
User: Y

AI: Do you also want to create mco_kernel.go in openshift-tests-private
    and remove the extracted tests from mco.go? [Y/n]
User: Y

AI: [Phase 3A: Source repo mirroring]
    Created openshift-tests-private/test/extended/mco/mco_kernel.go (9 tests)
    Modified openshift-tests-private/test/extended/mco/mco.go (removed 9 tests)
    PR created: https://github.com/openshift/openshift-tests-private/pull/XXX

    [Phase 3B: MCO migration]
    Migrating tests to machine-config-operator...
    Build: PASSED
    Test listing: All 9 migrated tests found
    PR created: https://github.com/openshift/machine-config-operator/pull/YYY
```

### Example interaction — suite extraction with custom filename

```text
AI: What do you want to migrate?
User: mco.go:kernel

AI: The extracted tests will be saved as mco_kernel.go.
    Enter a custom filename or press Enter to accept:
User: mco_kernel_args.go

AI: Found 9 tests matching "kernel" in mco.go:
    ...
    These will be extracted into mco_kernel_args.go. Proceed? [Y/n]
User: Y
```

## Arguments

This command takes no arguments. All configuration is collected interactively during execution.

## Notes

- **Prerequisites**: Go toolchain, Git, access to both openshift-tests-private and machine-config-operator repositories
- **Code preservation**: The migration does NOT simplify or refactor code - it only changes package names, imports, and function reference prefixes
- **Function order**: Functions are written in the same order as in the original source file
- **File naming**: For compat_otp utility functions, the same file names as the original are used
- **Duplicate detection**: Tests already migrated (by PolarionID) in destination are skipped. Open PRs on GitHub are also checked for in-flight migrations of the same tests (requires `gh` CLI)
- **Template files**: Referenced testdata YAML files are copied from `testdata/mco/` to `testdata/files/`
- **Suite extraction**: Use `filename:keyword` syntax to extract a subset of tests from any file (e.g., `mco.go:kernel`, `mco_security.go:cipher`)
- **Build verification**: The command builds the test binary using `make machine-config-tests-ext` and verifies migrated tests appear in the listing

## See Also

- Implementation skill: `.claude/skills/mco-migration-workflow.md`
- Machine Config Operator: <https://github.com/openshift/machine-config-operator>
- OpenShift Tests Private: <https://github.com/openshift/openshift-tests-private>

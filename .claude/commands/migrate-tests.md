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
3. Transforms and migrates test code (package, imports, naming, references)
4. Migrates helper functions and compat_otp utilities
5. Copies referenced template/testdata files
6. Builds the test binary and verifies migrated tests appear
7. Optionally runs a specific test against a cluster

**Key Features:**

- **Two migration modes** - Migrate a whole test file (e.g., `mco_configdrift.go`) or extract a test suite from the large `mco.go` file by keyword
- **Accurate test name transformation** - Converts `Author:USERNAME-Qualifiers-ID-[Tags] Description` format to `[PolarionID:ID][OTP] Description` format
- **Import rewriting** - Replaces all `compat_otp` references with `exutil` equivalents
- **Duplicate detection** - Skips tests already present in destination and warns about tests being migrated in open (unmerged) PRs on GitHub
- **Code preservation** - Migrates code as-is without simplification or refactoring
- **Template migration** - Copies referenced testdata YAML files to the correct destination path
- **Build verification** - Compiles the binary and verifies migrated tests are listed

## Implementation

**IMPORTANT: Use the MCO Migration Workflow skill for implementation.**

This command uses the `mco-migration-workflow` skill which provides detailed step-by-step implementation guidance for all 4 phases of the migration.

To execute this command:

1. **Invoke the skill** to get detailed implementation instructions:
   - The skill is located at: `.claude/skills/mco-migration-workflow.md`
   - Follow the skill's 4-phase workflow exactly as documented

2. **The workflow phases are:**
   - **Phase 1**: User Input Collection (5 inputs - source repo, dest repo, compat_otp path, migration target, confirmation)
   - **Phase 2**: Analysis (read source/dest files, identify dependencies, map test names)
   - **Phase 3**: Migration Execution (transform and write code, copy templates, migrate utilities)
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

- List of all files created/modified in destination repo
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
4. List available test files and ask which to migrate
5. Display configuration summary for confirmation
6. Analyze source and destination patterns
7. Execute the migration transformations
8. Build and verify the test binary
9. Provide summary and next steps

### Example interaction - whole file migration

```text
User: /migrate-tests

AI: What is the path to your openshift-tests-private repository?

User: /home/user/repos/openshift-tests-private

AI: What is the path to your machine-config-operator repository?

User: /home/user/repos/machine-config-operator

AI: What is the path to the compat_otp library? (press Enter to skip)

User: /home/user/repos/origin/test/extended/util/compat_otp

AI: Available test files in openshift-tests-private/test/extended/mco/:
    1. mco.go (4136 lines - large file, consider suite extraction)
    2. mco_alerts.go
    3. mco_bootimages.go
    4. mco_configdrift.go
    ...

    Choose migration mode:
    a) Whole file - enter filename (e.g., mco_configdrift.go)
    b) Suite extraction from mco.go - enter keyword to filter tests

User: mco_configdrift.go

AI: ========================================
    Migration Configuration Summary
    ========================================
    Source:        /home/user/repos/openshift-tests-private/test/extended/mco/mco_configdrift.go
    Destination:   /home/user/repos/machine-config-operator/test/extended-priv/
    compat_otp:    /home/user/repos/origin/test/extended/util/compat_otp
    Mode:          Whole file migration
    Tests found:   5 test cases
    ========================================

    Proceed with migration? [Y/n]:

User: Y

AI: [executes migration phases 2-4]
    ...
    Migration complete! 5 tests migrated successfully.
```

### Example interaction - suite extraction from mco.go

```text
User: /migrate-tests

...

AI: Choose migration mode:
    a) Whole file
    b) Suite extraction from mco.go

User: b

AI: Enter keyword to filter tests from mco.go (e.g., "kernel", "ssh", "fips"):

User: kernel

AI: Found 4 tests matching "kernel" in mco.go:
    1. Author:harshpat-...-42347-... health check for MCO kernel [Serial]
    2. Author:harshpat-...-43048-... kernel arguments [Disruptive]
    ...

    These will be extracted into mco_kernel.go. Proceed? [Y/n]:

User: Y

AI: [executes migration]
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
- **Large mco.go**: The `mco.go` file is 4000+ lines - use suite extraction mode to break it into smaller, focused test files before migrating
- **Build verification**: The command builds the test binary using `make machine-config-tests-ext` and verifies migrated tests appear in the listing

## See Also

- Implementation skill: `.claude/skills/mco-migration-workflow.md`
- Machine Config Operator: <https://github.com/openshift/machine-config-operator>
- OpenShift Tests Private: <https://github.com/openshift/openshift-tests-private>

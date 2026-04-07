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

- **Two migration modes** - Migrate a whole test file (e.g., `mco_configdrift.go`) or extract a test suite from any file by keyword
- **Accurate test name transformation** - Converts `Author:USERNAME-Qualifiers-ID-[Tags] Description` format to `[PolarionID:ID][OTP] Description` format
- **Import rewriting** - Replaces all `compat_otp` references with `exutil` equivalents
- **Live migration dashboard** - Scans source and destination repos at runtime to show what's already migrated, what's in open PRs, and what's available — with discoverable topic filters for suite extraction
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
4. Scan both repos and open PRs to build a live migration dashboard
5. Present available migration options with status (done/in-PR/available)
6. Let user pick from the dashboard or type a custom keyword
7. Display configuration summary for confirmation
8. Analyze source and destination patterns
9. Execute the migration transformations
10. Build and verify the test binary
11. Provide summary and next steps

### Example interaction

```text
User: /migrate-tests

AI: What is the path to your openshift-tests-private repository?
User: /home/user/repos/openshift-tests-private

AI: What is the path to your machine-config-operator repository?
User: /home/user/repos/machine-config-operator

AI: What is the path to the compat_otp library? (press Enter to skip)
User: /home/user/repos/origin/test/extended/util/compat_otp

AI: Scanning source and destination repos...
    Checking open PRs for in-flight migrations...

    ========================================
    MCO Test Migration Dashboard
    ========================================

    Whole-file migration options:
      #  File                                Total  Done  In-PR  Available
      1. mco_configdrift.go                     5     0      0       5
      2. mco_alerts.go                          5     2      1       2
      3. mco_bootimages.go                     11    11      0       0  DONE
      ...

    Suite extraction options (files with 6+ available tests):

      mco.go (72 tests, 15 done, 2 in-PR, 55 available):
        #   Topic          Available
        a.  kernel             9
        b.  registry           2
        c.  drain              4
        ...

    Enter your choice:
    - A number (e.g., 1) to migrate a whole file
    - A file.topic (e.g., mco.kernel) to extract a suite
    - Or type a custom keyword to search

User: 1

AI: [shows confirmation summary, then executes migration]
    Migration complete! 5 tests migrated successfully.
```

## Arguments

This command takes no arguments. All configuration is collected interactively during execution.

## Notes

- **Prerequisites**: Go toolchain, Git, access to both openshift-tests-private and machine-config-operator repositories
- **Code preservation**: The migration does NOT simplify or refactor code - it only changes package names, imports, and function reference prefixes
- **Function order**: Functions are written in the same order as in the original source file
- **File naming**: For compat_otp utility functions, the same file names as the original are used
- **Live dashboard**: The migration dashboard is built in real-time by scanning source files, destination files, and open PRs — nothing is hardcoded. Topic keywords for suite extraction are auto-discovered from test descriptions each run
- **Duplicate detection**: Tests already migrated (by PolarionID) in destination are skipped. Open PRs on GitHub are also checked for in-flight migrations of the same tests (requires `gh` CLI)
- **Template files**: Referenced testdata YAML files are copied from `testdata/mco/` to `testdata/files/`
- **Suite extraction**: Available for any source file with 6+ available tests, not just `mco.go`. Topics are discovered dynamically from test descriptions
- **Build verification**: The command builds the test binary using `make machine-config-tests-ext` and verifies migrated tests appear in the listing

## See Also

- Implementation skill: `.claude/skills/mco-migration-workflow.md`
- Machine Config Operator: <https://github.com/openshift/machine-config-operator>
- OpenShift Tests Private: <https://github.com/openshift/openshift-tests-private>

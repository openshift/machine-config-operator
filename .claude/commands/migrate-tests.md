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

**Key Features:**

- **Whole-file migration** - Migrate complete test files (e.g., `mco_configdrift.go`)
- **Accurate test name transformation** - Converts `Author:USERNAME-Qualifiers-ID-[Tags] Description` format to `[PolarionID:ID][OTP] Description` format
- **Import rewriting** - Replaces all `compat_otp` references with `exutil` equivalents
- **Duplicate detection** - Skips tests already present in destination
- **Code preservation** - Migrates code as-is without simplification or refactoring
- **Template migration** - Copies referenced testdata files to the correct destination path
- **Build verification** - Compiles the binary and verifies migrated tests are listed

## Implementation

**IMPORTANT: Use the MCO Migration Workflow skill for implementation.**

This command uses the `mco-migration-workflow` skill which provides detailed step-by-step implementation guidance for all phases of the migration.

To execute this command:

1. **Invoke the skill** to get detailed implementation instructions:
   - The skill is located at: `.claude/skills/mco-migration-workflow.md`
   - Follow the skill's workflow phases exactly as documented

2. **The workflow phases are:**
   - **Phase 1**: User Input Collection (source repo, dest repo, compat_otp path, migration target, confirmation)
   - **Phase 2**: Analysis (read source/dest files, identify dependencies, map test names)
   - **Phase 3**: Migration Execution (transform and write code, copy templates, migrate utilities)
   - **Phase 4**: Verification (build binary, list tests, summary)

3. **Critical implementation notes:**
   - Do NOT simplify or refactor migrated code - only change references
   - **IMPORTANT**: All migrated functions in the destination file must be placed in the same order as they appear in the source file, not appended at the end
   - For compat_otp functions, use the same file names as the original
   - Check for existing functions in destination before creating duplicates
   - `ConnectedOnly` in source maps to `[Skipped:Disconnected]` in destination
   - `[Serial][Disruptive]` is always added to the Describe block — in the new framework `[Disruptive]` no longer implies `[Serial]` as it did in the old one
   - Tags like `[Skipped:Disconnected]` and `[OCPFeatureGate:XXX]` stay on individual `g.It()` blocks since not all tests in the same Describe section share the same tags

**For complete implementation details, transformation rules, and verification steps, refer to the skill documentation.**

## Return Value

**Format**: Migration summary with verification results

**On success:**

- Destination repo changes and list of files created/modified
- List of all tests migrated with their new names
- Build verification result (pass/fail)
- Test listing verification showing migrated tests

**On failure:**

- Clear error message indicating which phase failed
- For build failures: error analysis and suggested fixes (usually missing imports or utility functions)
- Guidance on how to resolve the issue and retry

## Examples

### Migrate a whole test file

```bash
/migrate-tests
```

The command will:

1. Ask for the source repository path (openshift-tests-private)
2. Ask for the destination repository path (machine-config-operator)
3. Ask for the compat_otp library path (optional)
4. Ask what to migrate (filename)
5. Analyze the selected file for duplicates
6. Display configuration summary for confirmation
7. Execute the migration transformations
8. Build and verify the test binary
9. Provide summary and next steps

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
User: mco_configdrift.go

AI: mco_configdrift.go — 5 tests, 0 already migrated, 5 available
    [shows confirmation summary]
    Proceed? [Y/n]
User: Y

AI: [executes migration]
    Migration complete! 5 tests migrated successfully.
    Build: PASSED
    Test listing: All 5 migrated tests found
```

## Arguments

This command takes no arguments. All configuration is collected interactively during execution.

## Notes

- **Prerequisites**: Go toolchain, Git, access to both openshift-tests-private and machine-config-operator repositories
- **Code preservation**: The migration does NOT simplify or refactor code - it only changes package names, imports, and function reference prefixes
- **IMPORTANT — Function order**: All migrated functions in the destination file must be placed in the same order as they appear in the source file, not appended at the end
- **File naming**: For compat_otp utility functions, the same file names as the original are used
- **Duplicate detection**: Tests already migrated (by PolarionID) in destination are skipped
- **Template files**: Referenced testdata files are copied from `testdata/mco/` to `testdata/files/`
- **Build verification**: The command builds the test binary using `make machine-config-tests-ext` and verifies migrated tests appear in the listing

## See Also

- Implementation skill: `.claude/skills/mco-migration-workflow.md`
- Machine Config Operator: <https://github.com/openshift/machine-config-operator>
- OpenShift Tests Private: <https://github.com/openshift/openshift-tests-private>

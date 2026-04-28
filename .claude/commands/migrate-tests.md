---
description: Automate MCO test migration from openshift-tests-private to machine-config-operator
argument-hint: ""
---

Migrate MCO test files from `openshift-tests-private/test/extended/mco/` to `machine-config-operator/test/extended-priv/`.

Use the transformation rules in `.claude/skills/mco-migration-workflow.md` for all domain-specific mappings (imports, test names, qualifiers, paths).

**Workflow:**
1. Ask for source repo, destination repo, compat_otp path (optional), and filename. Check memory (`migrate_tests_config.md`) for saved paths.
2. Analyze the source file — identify tests, helpers, templates, and compat_otp dependencies. Check what already exists in destination.
3. Migrate — apply all transformations, copy templates, migrate helpers. Don't simplify or refactor code. Preserve function order from source.
4. Build with `make machine-config-tests-ext` and verify migrated tests appear in listing. Fix build errors iteratively.
5. **Verify ordering** — compare the sequence of functions, test cases (g.It), and helpers in the destination file against the source file. Report any ordering mismatches and fix them.
6. Save paths to memory for next run.

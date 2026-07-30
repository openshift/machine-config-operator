---
name: iri-mcd
description: Describes the acceptance criteria and BDD tests to be used for new/existing features of the InternalReleaseImage MachineConfigDaemon manager. The manager implements the NoRegistryClusterInstall main feature for the part related to manage/monitor a single control plane node
user-invocable: false
---

# InternalReleaseImage daemon manager BDD workflow

When working on InternalReleaseImage daemon manager changes:

1. Identify the specific behavior being changed.
2. Read only the relevant acceptance file under `acceptance/`.
3. If the change touches multiple behaviors, read all matching files.
4. Do not invent behavior that is not covered by the acceptance criteria.
5. When behavior changes, update the relevant acceptance file before implementing code.

# General implementation notes

When defining the implementation:

- Ensure that any new portion of code is covered at least by one or more unit tests.
- Keep production changes minimal.
- Avoid duplications, prefer a coding style that improves the readability and maintenance.
- Do not perform broad refactors unless needed to make the behavior testable.
- If a behavior is not covered by acceptance criteria, stop and ask before implementing it.
- When reporting completion, map each changed test back to the acceptance scenario it covers.
- Minimize comments, and keep them short.

# Specific IRI MCD manager implementation notes

- Keep the `syncInternalReleaseImage` method short and readable. Prefer to refactor included tasks into private type methods.
- Do not instantiate the IRI registry within `syncInternalReleaseImage` method more than once per loop: keep it simple stupid.

# Test implementation notes

- When testing different cases for the same scenario, use the cases := []struct{} to capture the relevant key fields for the test. Add always a speaking
  name field to represent the current case.
- Reuse the existing test methods if possible.
- Keep the test focused on the behaviors, avoid testing unnecessary technical details.
- Do not add too many comments to the tests

# Functional testing notes

The NoRegistryClusterInstall feature is also tested functionally in `/test/e2e-iri` folder.

## Supporting files

- `acceptance/storage-reclaim.md`: IRI deletion workflow, how to reclaim the disk node space previously used.
- `acceptance/feature-disabled.md`: the expected behavior when the feature is not enabled.

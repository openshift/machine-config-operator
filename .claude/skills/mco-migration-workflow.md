---
name: MCO Migration Workflow
description: Transformation rules for migrating MCO tests from openshift-tests-private to machine-config-operator
---

# MCO Test Migration Rules

Migrate test files from `openshift-tests-private/test/extended/mco/` to `machine-config-operator/test/extended-priv/`.

## Core Rules

- **Do NOT simplify or refactor** migrated code — only change references
- **Preserve function order** from the source file (don't append at end)
- **Use same filenames** as the source for both test files and compat_otp utility files
- **Skip duplicates** — check destination for existing functions and PolarionIDs before migrating

## Package Rename

```go
package mco  →  package extended
```

## Import Mapping

| Source | Destination |
|---|---|
| `compat_otp "github.com/openshift/origin/test/extended/util/compat_otp"` | `exutil "github.com/openshift/machine-config-operator/test/extended-priv/util"` |
| `exutil "github.com/openshift/origin/test/extended/util"` | `exutil "github.com/openshift/machine-config-operator/test/extended-priv/util"` |
| `"github.com/openshift/origin/test/extended/util/compat_otp/architecture"` | `"github.com/openshift/machine-config-operator/test/extended-priv/util/architecture"` |
| `logger "github.com/openshift/origin/test/extended/util/compat_otp/logext"` | `logger "github.com/openshift/machine-config-operator/test/extended-priv/util/logext"` |
| `clusterinfra "github.com/openshift/origin/test/extended/util/compat_otp/clusterinfra"` | `clusterinfra "github.com/openshift/machine-config-operator/test/extended-priv/util/clusterinfra"` |
| `"github.com/openshift/origin/test/extended/util/compat_otp/bootstrap"` | `"github.com/openshift/machine-config-operator/test/extended-priv/util/bootstrap"` |

All `compat_otp.` function call prefixes become `exutil.` (e.g., `compat_otp.By(` → `exutil.By(`).

## Describe Block Format

Source:
```go
g.Describe("[sig-mco] MCO <SuiteName>", func() {
```

Destination:
```go
g.Describe("[sig-mco][Suite:openshift/machine-config-operator/longduration][Serial][Disruptive] MCO <SuiteName>", func() {
```

- `[Serial][Disruptive]` is **always required** — in the new framework `[Disruptive]` no longer implies `[Serial]`
- Preserve any existing `g.Label(...)` decorators

## Test Name Transformation (g.It blocks)

Source pattern:
```
Author:USERNAME-QUALIFIER1-...-NNNNN-[PRIORITY][TAGS] Description [TRAILING_TAGS]
```

Destination pattern:
```
[PolarionID:NNNNN][OTP] Description
```

**Algorithm:**
1. Extract the numeric ID (5+ digits) from the name
2. Extract the description — text after the last `]` tag group following the ID
3. Strip trailing `[Serial]` and `[Disruptive]` from description (already on Describe block)

**Qualifier mappings:**
- `ConnectedOnly` → add `[Skipped:Disconnected]` tag
- `[OCPFeatureGate:XXX]` → preserve as-is
- Tags like `[Skipped:Disconnected]` and `[OCPFeatureGate:XXX]` stay on individual `g.It()` blocks (not all tests in a Describe share them)

**Stripped qualifiers** (not carried to destination): `Author:USERNAME`, `NonHyperShiftHOST`, `NonPreRelease`, `Longduration`, `LEVEL0`, priority words (`Critical`/`High`/`Medium`/`Low`), `[P1]`/`[P2]`/`[P3]`, `[OnCLayer]`

**Examples:**

| Source | Destination |
|---|---|
| `Author:sregidor-Longduration-NonPreRelease-High-46943-[P1][OnCLayer] Config Drift. Config file. [Serial]` | `[PolarionID:46943][OTP] Config Drift. Config file.` |
| `Author:sregidor-ConnectedOnly-NonHyperShiftHOST-NonPreRelease-Longduration-High-81921-[OnCLayer] Pinned images with IDMS [Disruptive]` | `[PolarionID:81921][OTP][Skipped:Disconnected] Pinned images with IDMS` |
| `Author:sregidor-NonHyperShiftHOST-NonPreRelease-Medium-81955-[P2][OnCLayer] Pinnedimageset invalid [Disruptive]` | `[PolarionID:81955][OTP] Pinnedimageset invalid` |

Preserve `g.Label(...)` decorators on individual tests.

## Path Mappings

| What | Source | Destination |
|---|---|---|
| Test files | `test/extended/mco/` | `test/extended-priv/` |
| Testdata/templates | `test/extended/testdata/mco/` | `test/extended-priv/testdata/files/` |
| Utility functions | compat_otp source | `test/extended-priv/util/` (or `util/<subpackage>/`) |

## Build & Verify

```bash
make machine-config-tests-ext
./_output/linux/amd64/machine-config-tests-ext list | grep <PolarionID>
```

## Workflow

1. **Collect inputs** — source repo path, destination repo path, compat_otp path (optional), filename to migrate. Check memory (`migrate_tests_config.md`) for saved paths.
2. **Analyze** — read source file, identify tests/helpers/templates/compat_otp deps, check what already exists in destination.
3. **Migrate** — apply all transformations above, copy templates, migrate helper functions and compat_otp utilities as needed.
4. **Verify** — build and confirm migrated tests appear in listing. Fix any build errors iteratively.
5. **Save paths** to memory for next run.

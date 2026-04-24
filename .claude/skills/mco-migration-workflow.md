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

- Both `[Serial]` and `[Disruptive]` are **always applied by default** — do not ask which suite to use
- In the new framework `[Disruptive]` no longer implies `[Serial]`, so both must be present

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

**Stripped qualifiers** (not carried to destination): `Author:USERNAME`, `NonHyperShiftHOST`, `NonPreRelease`, `Longduration`, priority words (`Critical`/`High`/`Medium`/`Low`), `[P1]`/`[P2]`/`[P3]`, `[OnCLayer]`

**Examples:**

| Source | Destination |
|---|---|
| `Author:sregidor-Longduration-NonPreRelease-High-46943-[P1][OnCLayer] Config Drift. Config file. [Serial]` | `[PolarionID:46943][OTP] Config Drift. Config file.` |
| `Author:sregidor-ConnectedOnly-NonHyperShiftHOST-NonPreRelease-Longduration-High-81921-[OnCLayer] Pinned images with IDMS [Disruptive]` | `[PolarionID:81921][OTP][Skipped:Disconnected] Pinned images with IDMS` |
| `Author:mhanss-LEVEL0-Longduration-NonPreRelease-Critical-42438-[P2][OnCLayer] add journald systemd config [Disruptive]` | `[PolarionID:42438][OTP][LEVEL0] add journald systemd config` |

## Platform Filtering

Convert `skipTestIfSupportedPlatformNotMatched` calls to `g.Label(...)` on the `g.It()` line and remove the call.

| Source Constant | g.Label Value |
|---|---|
| `AWSPlatform` | `"Platform:aws"` |
| `GCPPlatform` | `"Platform:gce"` (note: gcp → gce) |
| `AzurePlatform` | `"Platform:azure"` |
| `VspherePlatform` | `"Platform:vsphere"` |

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

1. **Collect inputs** — source repo path, destination repo path, compat_otp path (optional), and filename to migrate. Both `[Serial]` and `[Disruptive]` labels are always applied by default (do not ask). Check memory (`migrate_tests_config.md`) for saved paths.
2. **Analyze** — read source file, identify tests/helpers/templates/compat_otp deps, check what already exists in destination.
3. **Migrate** — apply all transformations above, copy templates, migrate helper functions and compat_otp utilities as needed.
4. **Verify build** — build and confirm migrated tests appear in listing. Fix any build errors iteratively.
5. **Verify ordering** — compare the sequence of all functions (test case functions, helper functions, and `g.It` blocks) in the destination file against their order in the source file. List any mismatches and fix them before proceeding.
6. **Save paths** to memory for next run.

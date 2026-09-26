# Test Plan — Skip Firstboot Node Image Pivot

**Jira Feature (OCPSTRAT):** [OCPSTRAT-3772](https://redhat.atlassian.net/browse/OCPSTRAT-3772) — [TP] Optionally skip the firstboot node image pivot to eliminate one reboot during install and scale-out

**Jira Epic/Story (implementation ticket):** [MCO-2574](https://redhat.atlassian.net/browse/MCO-2574) — Implement opt-in skip of the firstboot node image pivot (Tech Preview)

**Related items:**
- [OSDOCS-22166](https://redhat.atlassian.net/browse/OSDOCS-22166) — Docs epic
- [MCO-2565](https://redhat.atlassian.net/browse/MCO-2565) — Baremetal scale-up improvements (complementary approach)
- [RFE-817](https://redhat.atlassian.net/browse/RFE-817) — Allow OpenShift to scale out with latest image compatible with cluster (original customer RFE)

**Feature status at time of writing:** New, targeted OCP 5.1 Tech Preview

**Snapshot pin:** This plan reflects `openshift/machine-config-operator` at commit `7c6154a43` as of 2026-09-24. No implementation PRs exist yet; all test scenarios are Proposed. Re-verify scope if the referenced code has since changed (see Risks).

---

## 1. Test Plan Identifier

| Field | Value |
|---|---|
| **Identifier** | TP-OCPSTRAT-3772-skip-firstboot-pivot |
| **Revision** | 1 |
| **Date** | 2026-09-24 |

---

## 2. Introduction

### 2.1 Purpose

Every RHCOS node that joins an OpenShift cluster reboots at firstboot to pivot from the boot image to the payload node image. This feature makes it possible, as an explicit opt-in behind the `TechPreviewNoUpgrade` feature gate (`SkipFirstbootPivot`), for joining nodes to live-overlay the container runtime stack (CRI-O, kubelet/hyperkube) from the payload node image instead of rebooting. The full node image deployment is staged and activates at the next upgrade or admin-initiated reboot. The cluster administrator explicitly accepts that the node's kernel and non-overlaid user space remain at the boot image version until that reboot.

### 2.2 Scope

Per the Jira acceptance criteria, the feature MUST deliver in TP:

1. Day-0 opt-in: an `install-config.yaml` field causes control-plane and worker nodes to skip the firstboot pivot
2. Day-2 opt-in: a cluster API field (candidate: `MachineConfiguration`, cluster-scoped) governs scale-out behavior after install
3. Day-0 and Day-2 controls are independently settable — enabling at install MUST NOT force it for later scale-out
4. Gated behind a `TechPreviewNoUpgrade` feature gate
5. Overlaid node reports, in a machine-readable way, that it is running in a drifted state: boot image version, payload node image version, and the staged deployment
6. Full node image deployment is staged at firstboot and activates on the next reboot with no further operator action
7. The next OpenShift upgrade converges a drifted node to the payload node image via the normal upgrade reboot
8. Overlay failure is safe: the node falls back to the existing pivot-and-reboot path rather than joining in an inconsistent state
9. must-gather captures per-node drift state

### 2.3 Background — current behavior relevant to testing

Grounded in the codebase at commit `7c6154a43`:

- `pkg/daemon/daemon.go:1210` — `RunFirstbootCompleteMachineconfig()`: reads the encapsulated MachineConfig, compares boot image vs. payload, performs rpm-ostree rebase, and reboots. This is the code path the feature will modify to add the overlay-instead-of-reboot branch.
- `pkg/daemon/daemon.go:2119-2145` — Bootstrap pivot logic in `triggerUpdate()`: the bootstrap node already avoids the full pivot via a live overlay of the CRI-O/kubelet layer. This is the direct precedent the feature generalizes to control-plane and worker nodes.
- `pkg/daemon/update.go:43-44` — Imports `pivottypes` and `pivotutils` for OS pivot operations.
- `cmd/machine-config-daemon/firstboot_complete_machineconfig.go` — CLI entry point for the `firstboot-complete-machineconfig` subcommand.
- `pkg/daemon/constants/constants.go` — Firstboot-related path constants (`MachineConfigEncapsulatedPath`, etc.).
- **This is entirely new work.** No implementation PRs exist for OCPSTRAT-3772 or MCO-2574. The feature gate `SkipFirstbootPivot` has not been added to `openshift/api` yet.

Existing tests that touch the firstboot/pivot area:
- `test/extended-priv/mco_daemon.go:202` (PolarionID:69091) — asserts MCD skips reboot when config matches during bootstrap pivot (assisted-installer clusters only)
- `test/extended-priv/mco_alerts.go:113` (PolarionID:63866) — asserts `MCDPivotError` alert fires on pivot errors
- `test/extended-priv/mco_observability.go:42` — asserts `mcd_pivot_errors_total` metric exists
- `test/extended-priv/node.go:1487` — `ReplaceRpmOstree` helper for inducing pivot failures

### 2.4 References

| Ref | Document |
|---|---|
| R1 | [OCPSTRAT-3772](https://redhat.atlassian.net/browse/OCPSTRAT-3772) — Feature ticket |
| R2 | [MCO-2574](https://redhat.atlassian.net/browse/MCO-2574) — Implementation epic |
| R3 | [OSDOCS-22166](https://redhat.atlassian.net/browse/OSDOCS-22166) — Documentation epic |
| R4 | [MCO-2565](https://redhat.atlassian.net/browse/MCO-2565) — Baremetal scale-up improvements (complementary) |
| R5 | [RFE-817](https://redhat.atlassian.net/browse/RFE-817) — Original customer RFE |
| R6 | `pkg/daemon/daemon.go` — `RunFirstbootCompleteMachineconfig()` (line 1210), bootstrap pivot (line 2119) |
| R7 | `pkg/daemon/update.go` — Pivot utilities |
| R8 | `cmd/machine-config-daemon/firstboot_complete_machineconfig.go` — CLI entry point |
| R9 | `test/extended-priv/mco_daemon.go:202` — PolarionID:69091 (existing firstboot test) |
| R10 | `test/extended-priv/mco_alerts.go:113` — PolarionID:63866 (pivot error alert test) |
| R11 | `test/extended-priv/mco_observability.go:42` — Pivot error metric test |

---

## 3. Test Items

- **Feature gate** — `SkipFirstbootPivot` in `openshift/api`, gated under `TechPreviewNoUpgrade`; inert when disabled
- **Day-0 install-config field** — New field in `install-config.yaml` plumbed through the installer into MCO's rendered firstboot configuration
- **Day-2 cluster API field** — New field on `MachineConfiguration` (cluster-scoped), validated, defaulted off, independently settable from Day-0
- **MCD firstboot overlay path** — Modified `machine-config-daemon-firstboot` logic that overlays the container runtime stack (CRI-O, kubelet/hyperkube) from the payload node image instead of performing a full rpm-ostree rebase and reboot
- **Fallback-on-failure path** — Error handling that reverts to the existing pivot-and-reboot behavior on overlay failure
- **Drift state reporting** — Node annotations or MCD status fields exposing boot image version, payload node image version, and staged deployment; readable via `oc`
- **must-gather integration** — Drift state captured in must-gather output
- **Documentation** — OSDOCS-22166 content on flag usage, drift accepted, convergence, and "do not use if" guidance

---

## 4. Features to Be Tested

| ID | Requirement / Scenario | Priority | Status | Test location / evidence |
|---|---|---|---|---|
| F1 | Day-0 install with flag on: control-plane and worker nodes overlay CRI-O/kubelet from payload, stage full deployment, join Ready without rebooting | High | Proposed | none yet |
| F2 | Day-2 scale-out with flag on: new nodes join without firstboot reboot, even when MachineSet boot image is older than cluster node image | High | Proposed | none yet |
| F3 | Day-0 and Day-2 controls are independently settable: enabling at install does not force it for scale-out, and vice versa | High | Proposed | none yet |
| F4 | Feature gate enforcement: behavior is active only when `TechPreviewNoUpgrade` feature set is enabled and `SkipFirstbootPivot` gate is on | High | Proposed | none yet |
| F5 | Drifted node reports drift state: boot image version, payload node image version, staged deployment — readable via `oc` | High | Proposed | none yet |
| F6 | Staged deployment activates on next reboot with no further operator action | High | Proposed | none yet |
| F7 | Convergence on upgrade: the next OpenShift upgrade reboots a drifted node onto the payload node image via normal upgrade path | High | Proposed | none yet |
| F8 | Overlay failure falls back to pivot-and-reboot: node joins normally, failure surfaced in MCD logs and node state | High | Proposed | none yet |
| F9 | must-gather captures per-node drift state | Medium | Proposed | none yet |
| F10 | Flag off: existing firstboot pivot-and-reboot behavior is byte-for-byte identical — zero regression | High | Proposed | none yet |
| F11 | Day-0 install-config field validation: invalid values are rejected at install time | Medium | Proposed | none yet |
| F12 | Day-2 API field validation: invalid values on `MachineConfiguration` are rejected with clear error | Medium | Proposed | none yet |
| F13 | SNO install with flag on: single node overlays and joins Ready without firstboot reboot (stretch) | Medium | Proposed | none yet |
| F14 | Drifted nodes do not trip existing config-drift detection or degrade the MachineConfigPool | High | Proposed | none yet |
| F15 | MCDPivotError alert does not fire spuriously on successful skip-pivot nodes | Medium | Implemented (adjacent) | `test/extended-priv/mco_alerts.go:113` PolarionID:63866 |
| F16 | Interaction with boot image management (ManagedBootImages): when both features are enabled, behavior is correct and documented | Medium | Proposed | none yet |
| F17 | Existing firstboot test (PolarionID:69091) continues to pass: MCD still skips reboot when config matches during bootstrap pivot on assisted-installer clusters | Medium | Implemented | `test/extended-priv/mco_daemon.go:202` PolarionID:69091 |
| F18 | Disconnected/restricted network: overlay works when the payload node image is pulled from a mirror registry | Medium | Proposed | none yet |
| F19 | Joining node runs payload-version kubelet and CRI-O after overlay (verified by version check) | High | Proposed | none yet |
| F20 | Admin-initiated reboot of a drifted node converges it to the staged deployment | Medium | Proposed | none yet |
| F21 | Drifted node drained and rebooted by an unrelated actor (e.g. node maintenance) converges cleanly | Medium | Proposed | none yet |

---

## 5. Features Not to Be Tested

| Excluded | Rationale |
|---|---|
| Admin force convergence on demand without a full upgrade (Req 10) | "No (GA)" per requirements table — deferred to GA |
| Alert or cluster condition when drift exceeds a supported skew bound (Req 11) | "No (GA)" per requirements table — deferred to GA |
| Overlay version-skew validation and enforcement (Req 12) | "No (GA)" per requirements table — "Overlay is version-skew validated against the boot image before being applied; refuse and fall back if out of bounds" deferred to GA |
| Managed OpenShift (ROSA/OSD/ARO) | "Self-managed only in TP. Managed (ROSA/OSD/ARO) is SRE-controlled and out of scope until GA." |
| Hosted control planes (HCP) | "Classic standalone in TP. HCP NodePool ignition is rendered by MCO binaries executed from the HyperShift ignition server, so the mechanism is plausibly reusable, but it is not validated in TP." |
| RHEL worker nodes (non-RHCOS) | "RHEL worker nodes (non-RHCOS)" listed under Out of Scope |
| ppc64le / s390x architectures | "x86_64 and aarch64 in TP. ppc64le / s390x follow subject to RHCOS parity." |
| Defaulting the behavior on | "Making this the default. It is opt-in in Tech Preview and there is no current plan to change that at GA." |
| Live-applying the full node image including kernel | "Live-applying the full node image, including the kernel. Only the container runtime stack is overlaid." |
| Compact topology | Per requester: compact topology is not a test target for TP |
| Kernel module operator interoperability (NFD, KMM, GPU) | Operator-level validation is outside MCO scope; documented risk only. Operators that assume kernel matches payload are at risk on drifted nodes — this is a docs/customer-guidance item, not an MCO test item. |

---

## 6. Approach

### 6.1 Strategy

MCO owns the controller/API/daemon behavior tested in this plan: the feature gate plumbing, the firstboot overlay path, the fallback-on-failure logic, drift state reporting, and must-gather integration. Cross-repo dependencies and their owners:

- **openshift/api** — Feature gate definition (`SkipFirstbootPivot`). Owned by API reviewers. MCO tests verify the gate is honored, not its definition.
- **openshift/installer** — `install-config.yaml` field plumbing. Owned by the installer team. MCO tests verify the downstream effect (MCD receives the config), not the installer's field handling.
- **RHCOS / rpm-ostree / bootc** — The CRI-O/kubelet layer must be a stable, separately addressable contract. Stage-without-reboot plus partial live overlay must be a supported state. MCO tests verify the MCO orchestration of this capability, not rpm-ostree internals.
- **CI capacity** — New E2E jobs required (install-with-flag, scale-out-with-flag, SNO variant). CI job definitions are owned by the CI team; MCO provides the test code.
- **Documentation** — OSDOCS-22166. MCO verifies the feature behaves as documented; docs content is owned by the docs team.

The bootstrap node's existing live overlay (line 2119 of `pkg/daemon/daemon.go`) is the direct precedent. This plan does not re-test the bootstrap node's behavior — it tests the generalization to CP/worker nodes.

### 6.2 Test Levels

**Unit tests** — Cover the firstboot branch logic (overlay vs. pivot decision), API field validation, drift state computation, and fallback triggering. Target: F3, F4, F5, F8, F10, F11, F12, F14.

**Extended E2E (Ginkgo)** — Cover full install and scale-out flows with the flag on, convergence on upgrade, induced overlay failure with clean fallback, must-gather output, and non-interference with existing behavior. Target: F1, F2, F6, F7, F8, F9, F10, F13, F15, F16, F17, F18, F19, F20, F21.

**Manual/exploratory** — Cover edge cases not automatable in CI: very old boot images, bare-metal-specific timing, interaction with boot image management on real MachineSets. Target: F16, F21.

### 6.3 Security / ProdSec Review

N/A — No new key material, credentials, or security-sensitive data paths are introduced. The overlay uses the same node image pull path as the existing pivot. No new network endpoints are exposed.

### 6.4 Regression

Existing suites to re-run with the flag **off** to confirm zero regression:

- PolarionID:69091 (`test/extended-priv/mco_daemon.go:202`) — Firstboot skip-reboot on config match (F17)
- PolarionID:63866 (`test/extended-priv/mco_alerts.go:113`) — MCDPivotError alert (F15)
- `test/extended-priv/mco_observability.go:42` — `mcd_pivot_errors_total` metric
- Full extended-priv and extended suite pass with the feature gate disabled (F10)

---

## 7. Item Pass/Fail Criteria

- Per case: passes only when actual matches expected; else fails and a defect is filed.
- Unit tests: all unit tests must pass; both new unit tests and old unit tests. No regression should happen at unit test level.
- Feature acceptance: every High-priority F# item has at least one passing case with zero Blocker/Critical defects.
- **Quantitative targets from the feature's Success Criteria:**
  - Firstboot reboots per joining node with flag on: **0** (currently 1)
  - Induced overlay failures that fall back cleanly to pivot-and-reboot: **100%**
  - Drifted nodes that correctly converge at the next upgrade: **100%**
  - Firstboot failure rate, flag on vs. off: no statistically significant regression
  - Support cases attributable to unexpected kernel drift: **0** during TP
- **GA graduation:** This feature is Tech Preview in 5.1 under `TechPreviewNoUpgrade`. When the feature gate graduates to default in a future release, the criteria defined in https://github.com/openshift/api#defining-featuregate-e2e-tests must be met before the gate can flip.

---

## 8. Suspension/Resumption Criteria

N/A

---

## 9. Test Deliverables

1. This test plan.
2. Test case specifications (one per F# row in Section 4).
3. Automated test code (paths from Section 4/6).
4. Execution logs / evidence (job URLs, Polarion results).
5. Quality stories filed in Jira, one per `Proposed`/`Blocked` row in Section 4 — filed as real tickets, not drafted inside this document.

---

## 10. Testing Tasks

Populate in Jira; track via MCO-2574 child epic/story/task hierarchy.

---

## 11. Environmental Needs

- Platforms: AWS (primary), bare metal (primary); GCP, Azure, vSphere (stretch/best-effort)
- Architectures: x86_64 / AMD64 (primary), aarch64 / ARM64 (primary)
- Topology: multi-node (primary); SNO (stretch/best-effort); compact not tested
- FIPS / disconnected: disconnected tested (F18); FIPS not specifically scoped but should not regress
- OCP versions in scope: 5.1
- Build / image source: feature-branch image with `SkipFirstbootPivot` gate enabled via `TechPreviewNoUpgrade` feature set
- Tooling: `oc`, `kubectl`, `must-gather`, `rpm-ostree status`, `crictl version`, `kubelet --version`, `journalctl`
- Test data / fixtures: clusters installed with `TechPreviewNoUpgrade` feature set; MachineSets with intentionally stale boot images for scale-out testing

---

## 12. Responsibilities

Tracked in the Jira feature: [OCPSTRAT-3772](https://redhat.atlassian.net/browse/OCPSTRAT-3772)

---

## 13. Staffing and Training Needs

- Familiarity with MCO internals: `machine-config-daemon-firstboot` service, rpm-ostree container-native layering, the bootstrap pivot flow
- Understanding of the difference between boot image, payload node image, and overlay — and the drift contract this feature introduces
- Experience with bare-metal cluster provisioning (for primary test target)
- Familiarity with Ginkgo test framework and MCO extended test suite structure

---

## 14. Schedule

Tracked in the Jira feature: [OCPSTRAT-3772](https://redhat.atlassian.net/browse/OCPSTRAT-3772)

---

## 15. Risks and Contingencies

| ID | Risk | Impact | Mitigation |
|---|---|---|---|
| R-1 | rpm-ostree/bootc may not support stage-without-reboot composed with a partial live overlay as a supported state (MCO-2574 open question #2: "Highest risk") | Reshapes the design; overlay approach may not be viable | Engage RHCOS/rpm-ostree team for explicit confirmation before implementation starts; block E2E test authoring on design resolution |
| R-2 | Enhancement proposal in openshift/enhancements not yet merged — gates all implementation | No code to test; test plan remains entirely Proposed | Track enhancement PR; test plan activates once the proposal merges and PRs land |
| R-3 | Feature gate `SkipFirstbootPivot` not yet defined in openshift/api | Cannot enable the feature in test clusters | Coordinate with API reviewers; feature gate PR is a prerequisite for any E2E work |
| R-4 | CI capacity for new E2E jobs (install-with-flag, scale-out-with-flag, SNO variant) not yet provisioned | Tests cannot run in CI; manual-only execution | Request CI job provisioning early in the development cycle alongside the implementation PRs |
| R-5 | Day-2 API surface undecided: `MachineConfiguration` vs. per-pool on `MachineConfigPool` (MCO-2574 open question #3) | Test scenarios F3, F12 may need revision depending on API placement | Design both test paths; finalize after API review decision |
| R-6 | "Container runtime stack" composition undecided: CRI-O + kubelet at minimum, possibly runc/crun, conmon, selinux-policy (OCPSTRAT-3772 open question #1) | Verification commands for F19 depend on which binaries are overlaid | Use version checks for CRI-O and kubelet as minimum; extend to additional components once composition is finalized |

---

## 16. Approvals

Tracked in the Jira feature: [OCPSTRAT-3772](https://redhat.atlassian.net/browse/OCPSTRAT-3772)

---

*Generated to accompany OCPSTRAT-3772/MCO-2574. File paths, line numbers, and commit references correspond to the revision named in the Snapshot pin above; re-verify after the referenced PRs merge.*

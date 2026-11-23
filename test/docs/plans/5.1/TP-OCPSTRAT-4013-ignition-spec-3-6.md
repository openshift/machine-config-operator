# Test Plan: Ignition spec 3.6 support

## 1. Test Plan Identifier

| Field | Value |
|---|---|
| **Identifier** | TP-OCPSTRAT-4013-ignition-spec-3-6 |
| **Revision** | Draft 3, clean-up after initial review |
| **Date** | 2026-11-23 |

---

## 2. Introduction

### 2.1 Purpose

Validate the MCO delta for Ignition spec 3.6 while preserving supported older configurations. The feature requires that "MCO accepts MachineConfigs on spec 3.6 and serves them through the Machine Config Server". It also requires that "Mixed-version MachineConfigs (for example, 3.2 + 3.6) in one pool render into a single rendered config".

### 2.2 Scope

The feature lists seven acceptance criteria (AC). This plan maps each to test items:

- AC1 (installer): deferred to [MCO-2665](https://redhat.atlassian.net/browse/MCO-2665). See F14.
- AC2 (MCS accepts and serves 3.6): F1 through F3, F10.
- AC3 (mixed-version rendering): F2.
- AC4 (no regressions, including upgrades from older specs): F1, F2, F6 through F10, F12.
- AC5 (setuid/setgid/sticky bits written by MCD on day 2): F4, F5.
- AC6 (documentation): F13.
- AC7 (QE coverage for install and day 2): execution evidence across the applicable F# items.

### 2.3 Background

[PR #6236](https://github.com/openshift/machine-config-operator/pull/6236) (merged) adds 3.6 as the internal Ignition version, updates parser/types and conversion edges, changes permission handling, and updates tests. [PR #6760](https://github.com/openshift/machine-config-operator/pull/6760) (merged) adds e2e coverage for special mode bits. Both extend the previous spec bump ([MCO-1234](https://redhat.atlassian.net/browse/MCO-1234), [PR #4903](https://github.com/openshift/machine-config-operator/pull/4903), [PR #4913](https://github.com/openshift/machine-config-operator/pull/4913)).

### 2.4 References

| Ref | Document |
|---|---|
| R1 | [OCPSTRAT-4013: feature requirements](https://redhat.atlassian.net/browse/OCPSTRAT-4013) |
| R2 | [MCO-2402: MCO implementation scope](https://redhat.atlassian.net/browse/MCO-2402) |
| R3 | [MCO PR #6236: implementation (merged)](https://github.com/openshift/machine-config-operator/pull/6236) |
| R4 | [MCO PR #6760: special mode bits e2e tests (merged)](https://github.com/openshift/machine-config-operator/pull/6760) |
| R5 | [MCO-2590: test-plan tracking](https://redhat.atlassian.net/browse/MCO-2590) |
| R6 | [MCO-2659: pre-merge verification requirements](https://redhat.atlassian.net/browse/MCO-2659) |
| R7 | [MCO-2665: separate installer work](https://redhat.atlassian.net/browse/MCO-2665) |
| R8 | [MCO-2551: documentation review](https://redhat.atlassian.net/browse/MCO-2551) |
| R9 | [OSDOCS-24351: feature documentation epic](https://redhat.atlassian.net/browse/OSDOCS-24351) |

---

## 3. Test Items

- Ignition parser, validator, recursive converter, and rendered MachineConfig output.
- Machine Config Server version/content negotiation and response payloads.
- Machine Config Daemon file creation/replacement, ownership, special-mode preservation, and on-disk validation.
- Compatibility of already-supported versions and existing reconcilability rules.
- Documentation deliverables under [MCO-2551](https://redhat.atlassian.net/browse/MCO-2551) and [OSDOCS-24351](https://redhat.atlassian.net/browse/OSDOCS-24351).
- Installer delivery is excluded from MCO-only completion (F14).

---

## 4. Features to Be Tested

Each scenario is defined once here. Later sections reference these IDs. "Implemented" means test code exists in the merged PRs (R3, R4). "Proposed" identifies new or extended coverage. "Blocked" identifies an external prerequisite.

| ID | Requirement / Scenario | Priority | Status | Notes |
|---|---|---|---|---|
| F1 | AC2/AC4: Parse and normalize supported versions to internal 3.6. Preserve converter routes and error handling. | High | Implemented | `TestIgnitionConverterGetSupportedMinorVersions`, `TestIgnitionConverterGetSupportedMinorVersion`, `TestIgnitionConverterConvert`, `TestParseAndConvert` |
| F2 | AC2/AC3/AC4: Mixed 2.2.0 and 3.0.0 through 3.6.0 configurations in one worker pool produce a single rendered 3.6.0 configuration with all version-specific files. | High | Implemented | `Deploy files using all available ignition configs` |
| F3 | AC2: Version negotiation recognizes 3.6 alongside existing accepted headers. Full response fidelity is F10. | High | Implemented | `TestAcceptHeaders`, `TestAPIHandler` |
| F4 | AC5: 3.6 sticky-bit mode accepted, v2 special-mode validation rejected, missing expected setuid detected on disk. | High | Implemented | `TestValidateIgnition`, `TestValidateFiles` |
| F5 | AC5: On real nodes, make sure that setuid, setgid and sticky bits work with ordinary permissions. Include ownership, replacement content, and return to ordinary permissions. Drift detection and reboot persistence use the same MCD path as ordinary files. | High | Partial | `Ignition 3.6 special mode bits are applied to files on nodes`. Ownership and permission restoration gaps tracked in [MCO-2691](https://redhat.atlassian.net/browse/MCO-2691). |
| F6 | AC4/[MCO-2659](https://redhat.atlassian.net/browse/MCO-2659) regression: Execute existing suites for ordinary files, compressed contents, units, kubelet/runtime configuration, rendering and drift. Record new failures. | High | Implemented | `TestIgnitionMergeCompressed`, `TestParseAndConvertGzippedConfig` |
| F7 | Boot-image interoperability: MCS serves appropriate content for boot images based on versions defined in request. | High | Implemented | `Scaleup using 4.13 cloud image`, `Scaleup using 4.16 cloud image` |
| F8 | AC2/AC4: Reject malformed, missing, and unsupported versions and out-of-range file modes. Preserve the healthy configuration and show recovery after invalid input is removed. | High | Implemented | Covered by F1 and F4. Recovery covered by `TestPoolDegradedOnFailToRender`, `TestMCCReconcileAfterBadMC`. |
| F9 | AC4: Use a documented supported upgrade path. Clusters installed on older configs must successfully complete upgrades. | High | Implemented | Handled by Component Readiness upgrade periodics([sample run](https://prow.ci.openshift.org/job-history/gs/test-platform-results-public/logs/periodic-ci-openshift-release-main-ci-5.1-upgrade-from-stable-5.0-e2e-gcp-ovn-rt-upgrade)). |
| F10 | AC2/AC4: File content, mode, ownership and units survive compatible down-conversion and MCS response generation. Inspect response fields, not just status or version. | High | **Proposed** | Unit. Extend `helpers_test.go` and `api_test.go`. [MCO-2691](https://redhat.atlassian.net/browse/MCO-2691). |
| F11 | 3.6 behavior: Arbitrary custom Clevis pin accepted at parser/converter boundary and rejected for incompatible down-conversion. | High | **Proposed** | Unit. Converter logic is in vendored `ign-converter`. [MCO-2691](https://redhat.atlassian.net/browse/MCO-2691). |
| F12 | AC4: When input version is 3.6, preserve existing day-2 reconcilability restrictions. Make sure that no unintended disk mutation occurs. | High | **Proposed** | Unit. Extend `IsRenderedConfigReconcilable` cases. [MCO-2691](https://redhat.atlassian.net/browse/MCO-2691). |
| F13 | AC6: Make sure that documentation agrees with the delivered MCO scope and downgrade limitations. | Medium | **Proposed** | Manual review. |
| F14 | AC1/AC5 installation parity: Qualify 3.6 installation inputs and compare file-mode behavior with day 2, once installer and boot-image prerequisites are available. | High | **Blocked** | Waiting on installer ([MCO-2665](https://redhat.atlassian.net/browse/MCO-2665)). |

---

## 5. Features Not to Be Tested

| Excluded | Rationale |
|---|---|
| Installer delivery as part of MCO-only 5.1 completion | Under [MCO-2665](https://redhat.atlassian.net/browse/MCO-2665). F14 tracks the dependency. |
| Expanding day-2 storage capabilities | Same reconcilability rules apply to 3.6. F12 tests the boundary. |
| RHCOS/Ignition upstream changes | Upstream 3.6 support is already done. |
| Butane changes | Tracked under [COS-4433](https://redhat.atlassian.net/browse/COS-4433), blocked on this feature. |
| HCP, provider/architecture/FIPS/disconnected matrix | Beyond the standard multi-node topology. |
| Feature-gate graduation and product downgrade | N/A |

---

## 6. Approach

### 6.1 Strategy

Use existing tests first. Add focused assertions only for the gaps in Section 4. Component source tests belong in `openshift/machine-config-operator`. Proposed extended additions reuse `test/extended-priv/mco_storage.go`.

Run cluster tests on disposable environments. Serialize tests that touch a common pool or node. Restore only test-owned resources and wait for pool/node readiness before you release the cluster. Use the selected worker pool and inspect all targeted nodes.

### 6.2 Test Levels

- `unit`: F1, F3, F4, F6, F8, F10, F11, F12
- `disruptive`: F2, F5, F7
- `upgrade`: F9.
- `e2e-periodic`, external prerequisites: F14.
- Manual review: F13.

### 6.3 Security

No ProdSec review was supplied. F4, F5 test permission/privilege correctness with inert fixtures and no credentials. Evidence must not expose keys, tokens or sensitive node content.

### 6.4 Regression

Execute F6 and retain unit results from F1/F3/F4 as a baseline. Extend with F10/F11/F12 for negative and compatibility boundaries, and F9 for upgrade qualification.

---

## 7. Item Pass/Fail Criteria

- If observed state matches the expected outcome, pass. Record failures as defects.
- All applicable unit tests pass.
- Every new cluster capability introduced by Ignition bump to 3.6.0 has at least one passing test in the disruptive suite.
- F14 is deferred from MCO-only sign-off.
- No unresolved Blocker/Critical defect is acceptable.

---

## 8. Suspension/Resumption Criteria

N/A. Use existing test framework safety controls.

---

## 9. Test Deliverables

1. This plan.
2. Automated test changes for Proposed items.
3. Execution logs, job URLs, candidate identifiers, rendered-configuration evidence, and mode/ownership observations.
4. Quality stories for Proposed/Blocked rows not covered by existing tracking items. Reconcile with [MCO-2590](https://redhat.atlassian.net/browse/MCO-2590)/[MCO-2659](https://redhat.atlassian.net/browse/MCO-2659).
5. A result summary that distinguishes MCO-only acceptance from deferred installer acceptance.

---

## 10. Testing Tasks

- Reconcile existing work with F1 through F14. Implement the coverage gaps.
- If component mapping for added or renamed tests is incorrect, add mapping work.
- Supply candidate builds and record the execution inventory from Section 11.
- Execute and archive evidence. Distinguish infrastructure failures from functional defects.
- Resolve R-1 external prerequisites.

---

## 11. Environmental Needs

- Topologies: disposable standard multi-node clusters on the target worker pool.
- OCP versions: target `openshift-5.1`. The F9 source version must be a documented supported upgrade source. New-feature cases require a build that contains R3 and R4.
- Build/image source: candidate that contains the merged PRs (R3, R4). Record the MCO image identifier. Native 3.6 requires Ignition 2.26.0 or later. Supply legacy boot images for F7.
- Access: cluster administration and node inspection for disposable environments. No credentials in fixtures or logs.
- Fixtures: inert test-owned file paths, known ownership values, mixed old-version and 3.6 configurations, negative inputs, ordinary and special-bit mode combinations.

---

## 12. Responsibilities

Tracked in Jira under [OCPSTRAT-4013](https://redhat.atlassian.net/browse/OCPSTRAT-4013):

- Implementation: [MCO-2402](https://redhat.atlassian.net/browse/MCO-2402)
- Testing: [MCO-2590](https://redhat.atlassian.net/browse/MCO-2590), [MCO-2659](https://redhat.atlassian.net/browse/MCO-2659)
- Installer: [MCO-2665](https://redhat.atlassian.net/browse/MCO-2665)
- Documentation: [MCO-2551](https://redhat.atlassian.net/browse/MCO-2551), [OSDOCS-24351](https://redhat.atlassian.net/browse/OSDOCS-24351)

---

## 13. Staffing and Training Needs

Execution requires familiarity with MachineConfig rendering, Ignition version conversion, numeric file modes/ownership, and daemon drift validation.

---

## 14. Schedule

Tracked in Jira: [OCPSTRAT-4013](https://redhat.atlassian.net/browse/OCPSTRAT-4013). Schedule depends on candidate availability and external prerequisites in Section 15.

---

## 15. Risks and Contingencies

| ID | Risk | Impact | Mitigation |
|---|---|---|---|
| R-1 | The feature combines MCO and installer delivery, but [MCO-2402](https://redhat.atlassian.net/browse/MCO-2402) defers installer to [MCO-2665](https://redhat.atlassian.net/browse/MCO-2665). | F14 and install/parity parts of AC1/AC5/AC7 can be incorrectly reported as complete. | Record MCO-only results separately. Keep F14 Blocked. Require reconciliation before combined feature closure. |

---

## 16. Approvals

Tracked in Jira: [OCPSTRAT-4013](https://redhat.atlassian.net/browse/OCPSTRAT-4013).

---

# Test Plan — OpenShift Image Mode State Reporting GA

## Overview

| Section | Purpose |
|---|---|
| [Header metadata](#header) | Feature, implementation epic, related items, status, and snapshot pin. |
| [1. Introduction](#1-introduction) | Purpose, scope, current behavior, and references. |
| [2. Test Strategy](#2-test-strategy) | MCO ownership and dependency boundaries. |
| [3. Features to Be Tested](#3-features-to-be-tested) | Traceable functional and readiness coverage. |
| [4. Features Not to Be Tested](#4-features-not-to-be-tested) | Explicit exclusions and rationale. |
| [5. Interaction / Non-Interference Checks](#5-interaction--non-interference-checks) | Existing behavior that can regress. |
| [6. Target Environments](#6-target-environments) | Release, platform, topology, and configuration scope. |
| [7. Approach](#7-approach) | Test levels, security review, and regression execution. |
| [8. Item Pass/Fail Criteria](#8-item-passfail-criteria) | Case, feature, and GA graduation criteria. |
| [9. Suspension/Resumption Criteria](#9-suspensionresumption-criteria) | Stop and resume conditions. |
| [10. Test Deliverables](#10-test-deliverables) | Evidence and proposed work-item outputs. |
| [11. Testing Tasks](#11-testing-tasks) | Work breakdown for execution and evidence collection. |
| [12. Risks and Contingencies](#12-risks-and-contingencies) | Feature-specific risks and mitigations. |
| [13. Approvals](#13-approvals) | Publication and merge-review approvals. |
| [14. External Dependencies / Unresolved Items](#14-external-dependencies--unresolved-items) | External owners and awaited outcomes. |

---

<a id="header"></a>

**Jira Feature (OCPSTRAT):** OCPSTRAT-1282 — OpenShift Image Mode State Reporting GA
**Jira Epic/Story (implementation ticket):** MCO-1506 — Image Mode Status Reporting GA & MCN Improvements
**Related items:** MCO-1735, MCO-1736, MCO-1775, MCO-1798; [MCO PR #5141](https://github.com/openshift/machine-config-operator/pull/5141), [#5282](https://github.com/openshift/machine-config-operator/pull/5282), [#5363](https://github.com/openshift/machine-config-operator/pull/5363), [#5411](https://github.com/openshift/machine-config-operator/pull/5411); [openshift/api PR #2678](https://github.com/openshift/api/pull/2678)
**Feature status at time of writing:** In Progress, targeted 5.1
**Snapshot pin:** This plan reflects OCPSTRAT-1282/MCO-1506 at [MCO PR #6547](https://github.com/openshift/machine-config-operator/pull/6547) as of 2026-09-16. Re-verify scope if the referenced code has since changed (see Risks).

---

## 1. Introduction

### 1.1 Purpose

OCPSTRAT-1282 enables customers, troubleshooters, and maintainers to make informed decisions from reliable node-update state. Its feature overview states: “The Machine Config Operator (MCO) will provide accurate, granular, and consistent state reporting through the MachineConfigNode (MCN) resource, enabling customers, troubleshooters, and maintainers to make informed decisions based on reliable node update status information.” The Jira acceptance language requires “consistent status reporting across both standard and on-cluster image mode updates” and “accurate MachineConfigPool (MCP) status aligned with the original MCN enhancement design.”

### 1.2 Scope

MCO-1506 extends closed MCO-836; it is not greenfield. Confirmed scope is consistent MCN experience for standard and on-cluster image-mode updates, MCP status population consistent with the original MCN enhancement, component-readiness tests and monitoring, and feature-gate graduation evidence for OpenShift 5.1.

### 1.3 Background — current behavior relevant to testing

The existing serial disruptive [ImageModeStatusReporting suite](https://github.com/openshift/machine-config-operator/blob/main/test/extended/image_mode_status_reporting.go#L31-L117) has five cases for MCN/node properties, image and non-image condition transitions, and MCP counts. [MCN helpers](https://github.com/openshift/machine-config-operator/blob/main/test/extended/machineconfignode.go#L24-L112) and their [transition assertions](https://github.com/openshift/machine-config-operator/blob/main/test/extended/machineconfignode.go#L211-L415) cover `status.configImage`, `UpdateFiles`, `UpdateOS`, and `ImagePulledFromRegistry`. [Upgrade monitor gate handling](https://github.com/openshift/machine-config-operator/blob/main/pkg/upgrademonitor/upgrade_monitor.go#L135-L178), [status apply behavior](https://github.com/openshift/machine-config-operator/blob/main/pkg/upgrademonitor/upgrade_monitor.go#L290-L342), and [node migration/wrappers](https://github.com/openshift/machine-config-operator/blob/main/pkg/controller/node/node_controller.go#L2174-L2304) are the current implementation paths. Relevant history includes [#5141](https://github.com/openshift/machine-config-operator/pull/5141), [#5282](https://github.com/openshift/machine-config-operator/pull/5282), [#5363](https://github.com/openshift/machine-config-operator/pull/5363), [#5411](https://github.com/openshift/machine-config-operator/pull/5411), and related openshift/api and client-go API history.

### 1.4 References

| Ref | Document |
|---|---|
| [OCPSTRAT-1282](https://redhat.atlassian.net/browse/OCPSTRAT-1282) | Feature requirement and exclusions. |
| [MCO-1506](https://redhat.atlassian.net/browse/MCO-1506) | Implementation epic, GA evidence, and 5.1 target. |
| [MCO-1735](https://redhat.atlassian.net/browse/MCO-1735) | Readiness/CI signal contract and evidence owner. |
| [MCO-1736](https://redhat.atlassian.net/browse/MCO-1736), [MCO-1775](https://redhat.atlassian.net/browse/MCO-1775), [MCO-1798](https://redhat.atlassian.net/browse/MCO-1798) | Graduation, legacy-condition, and print-column follow-ups. |
## 2. Test Strategy

MCO owns the MCN/MCP reporting implementation and its extended Ginkgo coverage; this plan preserves that verified baseline and defines only needed deltas. API and client-go contract history, feature-gate graduation, and readiness-platform selection are cross-repo or external-owner inputs. Vendored and unrelated controller behavior is not re-tested: evidence must exercise the MCO status contract and its declared update paths, while separately owned work is consumed only as a dependency.
## 3. Features to Be Tested

| ID | Requirement / Scenario | Priority | Status | Test location / evidence |
|---|---|---|---|---|
| F1 | Standard and image-mode MCN status is consistent. | High | Implemented baseline | [Five-case suite](https://github.com/openshift/machine-config-operator/blob/main/test/extended/image_mode_status_reporting.go#L31-L117); Polarion 69187, 69197. |
| F2 | MCN fields and image/non-image transitions follow the contract. | High | Implemented baseline | [MCN helpers](https://github.com/openshift/machine-config-operator/blob/main/test/extended/machineconfignode.go#L24-L112) and [assertions](https://github.com/openshift/machine-config-operator/blob/main/test/extended/machineconfignode.go#L211-L415); Polarion 69205, 81831. |
| F3 | MCP updated/degraded counts align with MCN design. | High | Implemented baseline | [MCP count cases](https://github.com/openshift/machine-config-operator/blob/main/test/extended/image_mode_status_reporting.go#L93-L116); Polarion 69755. |
| F4 | Existing e2e and Polarion coverage is updated for the current contract. | High | Proposed delta | Existing suite and Polarion 69187, 69197, 69205, 81831, 69755, 74644, 80333, 85901. |
| F5 | CI/component-readiness evidence supports GA. | High | Blocked until external signal/evidence | MCO-1735 readiness records and API feature-promotion platform selection. |
| F6 | Gate-off-to-gate-on legacy migration has an explicit decision and coverage. | Medium | Proposed | [Migration path](https://github.com/openshift/machine-config-operator/blob/main/pkg/controller/node/node_controller.go#L2174-L2304); MCO-1775/MCO-1736 disposition. |
| F7 | Unchanged MCN spec/status values issue no SSA apply when the relevant fields are unchanged; when only `Spec.ConfigImage.DesiredImage` changes, the no-diff guard must not skip the apply and the resulting MCN spec must contain the new desired image. | Medium | Proposed | [Spec generation and no-diff guard](https://github.com/openshift/machine-config-operator/blob/main/pkg/upgrademonitor/upgrade_monitor.go#L489-L541); [status/config-image path](https://github.com/openshift/machine-config-operator/blob/main/pkg/upgrademonitor/upgrade_monitor.go#L290-L342); [unit-test baseline](https://github.com/openshift/machine-config-operator/blob/main/pkg/upgrademonitor/upgrade_monitor_test.go#L43-L108) covers unchanged no-diff and `ConfigVersion.Desired` change; the desired-image-only case is not currently covered. |
| F8 | Image-pull, file-application, and OS-application failure reporting has an explicit decision and coverage. | Medium | Proposed | [MCN status assertions](https://github.com/openshift/machine-config-operator/blob/main/test/extended/machineconfignode.go#L211-L415). |
| F9 | Existing standard-update behavior remains unchanged while status reporting is exercised. | High | Implemented baseline | [Non-image transition case](https://github.com/openshift/machine-config-operator/blob/main/test/extended/image_mode_status_reporting.go#L77-L91). |
| F10 | Feature-gate and non-image compatibility remains intact. | High | Implemented baseline | [Gate handling](https://github.com/openshift/machine-config-operator/blob/main/pkg/upgrademonitor/upgrade_monitor.go#L135-L178); [image/non-image cases](https://github.com/openshift/machine-config-operator/blob/main/test/extended/image_mode_status_reporting.go#L61-L91). |
## 4. Features Not to Be Tested

| Excluded | Rationale |
|---|---|
| MCN-triggered updates | Jira explicitly calls this a future enhancement requiring separate refinement, Technology Preview implementation, and soak. |
| Observability through customizable metrics | Jira explicitly assigns flexible/customizable metrics to a separate enhancement/refinement. |
| InternalReleaseImage and `test-e2e-iri` | The [IRI README](https://github.com/openshift/machine-config-operator/blob/main/test/e2e-iri/README.md) and [OWNERS](https://github.com/openshift/machine-config-operator/blob/main/test/e2e-iri/OWNERS) assign separate controller ownership; it is not status-reporting coverage. |
| MCO-1736, MCO-1775, and MCO-1798 implementation | This plan consumes their verified outcomes; it does not re-test separately owned graduation, retirement, or print-column implementation. |
## 5. Interaction / Non-Interference Checks

| ID | What must keep working | Why it's at risk |
|---|---|---|
| F1 | Standard and image-mode contracts remain mutually consistent; existing image and non-image cases in the F1 suite provide the baseline evidence. | Split conditions can diverge by update mode. |
| F3 | Existing MCP/MCN count behavior remains consistent; F3 MCP count cases provide the baseline evidence. | Count derivation can regress while MCN conditions change. |
| F9 | Standard non-image updates retain their established transition behavior; the F9 non-image transition case provides the baseline evidence. | Image-mode additions can alter shared update paths. |
| F10 | Gate-off and feature-gate/non-image compatibility remains intact; F10 gate handling and image/non-image cases provide the baseline evidence. | Gate logic can expose changed status semantics outside image mode. |
## 6. Target Environments

- Platforms: AWS and GCP are the primary execution platforms; other supported platforms are represented only through applicable existing CI jobs selected by the governing process.
- Architectures: This plan defines no invented fixed architecture matrix.
- Topology: SNO and 3-of-3 measurement rules are delegated to MCO-1735, not independent binding criteria in this plan.
- FIPS / disconnected: Coverage is N/A unless confirmed by selected CI.
- OCP versions in scope: OpenShift 5.1 is the target release.
## 7. Approach

### 7.1 Test Levels

| Level | Method and evidence | F# IDs |
|---|---|---|
| Unit/integration | Contract and implementation-path review; no unverified new unit suite is claimed. | F1, F2, F3, F6, F7, F10 |
| Extended/e2e Ginkgo | Re-run the serial disruptive ImageModeStatusReporting suite and its MCN helpers. | F1, F2, F3, F9, F10 |
| Manual/exploratory | Reconcile Polarion and inspect readiness/CI records; execute approved F4–F8 deltas. | F4, F5, F6, F7, F8 |
### 7.2 Security / ProdSec Review

N/A: no ProdSec review is applicable to this test-plan-only revision; no new security-sensitive feature behavior is introduced here.
### 7.3 Regression

Re-run ImageModeStatusReporting for F1/F3/F9/F10, its MCN helper assertions for F2, and applicable Polarion MCN cases 69187, 69197, 69205, 81831, 69755, 74644, 80333, and 85901. Re-run selected AWS/GCP CI evidence and preserve applicable other-platform results selected by MCO-1735.
## 8. Item Pass/Fail Criteria

- Per-case: a case passes only when its asserted MCN fields, transitions, and MCP counts match the declared update type; missing, contradictory, or persistent mismatched state fails.
- Feature acceptance: F1–F3 and F9–F10 require their existing evidence to pass; F4 and F6–F8 require an approved decision and resulting evidence before being counted; F5 requires MCO-1735 evidence.
- GA graduation: Component Readiness greater than 95% is the requester-confirmed binding criterion. MCO-1506 additionally requires seven green days on every platform selected by API `verify-feature-promotion`; the current rate is unknown here and its evidence is delegated to MCO-1735.
## 9. Suspension/Resumption Criteria

Suspend an affected execution for unavailable AWS/GCP capacity, an infrastructure/build failure that makes status results uninterpretable, a non-reporting selected CI route, or a critical reporting defect. Resume only after the environment/route is restored or the defect has an owner and disposition, then rerun the affected F# case with preserved artifacts. Suspend GA review when MCO-1735 or API platform-window evidence is absent; resume when that governing evidence is published.
## 10. Test Deliverables

1. This version-controlled plan and its reviewed traceability.
2. Existing ImageModeStatusReporting, MCN-helper, Polarion, and selected-CI results/artifacts.
3. Approved F4–F8 delta evidence and confirmed defect records, if any.
4. One real feature-specific Jira story for each Proposed or Blocked F# row (F4–F8), filed as a follow-up deliverable after plan approval, and MCO-1735 readiness evidence. This PR does not create Jira issues.
## 11. Testing Tasks

1. Reconcile F1–F3/F9–F10 assertions with the current MCN/MCP contract.
2. Re-run or collect selected AWS/GCP baseline and interaction evidence.
3. Update approved F4 traceability and implement only approved F6–F8 coverage.
4. Obtain F5 CI selection, readiness, and API feature-promotion evidence from its owners.
5. Review results against Section 8 and preserve artifacts for GA review.
6. After plan approval, file the real feature-specific Jira stories for Proposed or Blocked F4–F8 work; do not create Jira issues in this PR.
## 12. Risks and Contingencies

| ID | Risk | Impact | Mitigation |
|---|---|---|---|
| R1 | Serial disruptive status tests are unstable or cluster timing is ambiguous. | F1–F3/F9–F10 results are uninterpretable. | Preserve artifacts, separate infrastructure failures, and rerun the affected case. |
| R2 | MCO-1735 readiness signals or selected CI routes do not report. | F5/GA evidence cannot be evaluated. | Hold GA review and obtain the governing signal contract/evidence. |
| R3 | Feature-gate or legacy-condition dependencies change the contract after this snapshot. | F6/F10 coverage may become stale. | Re-verify against the pin and dependency outcomes before execution. |
| R4 | Failure-path or SSA proposals expand beyond confirmed scope. | F7/F8 could be misrepresented as GA coverage. | Require an explicit owner decision and feature-specific Jira story evidence. |
## 13. Approvals

Publication (commit/push/PR) required requester confirmation, and confirmation was received for this revision. Final MCO feature-owner and QE review remains required for merge; this plan does not create or change a PR.
## 14. External Dependencies / Unresolved Items

This section contains only external dependencies after chat.

1. MCO-1735 readiness/CI signal contract and evidence: waits on its owner’s selected-route, measurement-window, and readiness results.
2. MCO-1736 feature-gate graduation: waits on the API/feature-gate owner’s graduation outcome.
3. MCO-1775 legacy-condition retirement: waits on the owner’s retirement and migration-coverage disposition.
4. MCO-1798 MCN print columns: waits on the owner’s separately scoped implementation outcome.
5. [openshift/api PR #2678](https://github.com/openshift/api/pull/2678): waits on its API/print-column cleanup resolution, if still relevant at execution.

*Generated to accompany OCPSTRAT-1282/MCO-1506. File paths/line numbers/commit pin reference the `main` branch at time of writing (see Snapshot pin above); re-verify after the referenced PRs merge.*

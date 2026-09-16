# Test Plan — OpenShift Image Mode State Reporting GA

**Jira Feature (OCPSTRAT):** OCPSTRAT-1282 — OpenShift Image Mode State Reporting GA
**Jira Epic/Story (implementation ticket):** MCO-1506 — Image Mode Status Reporting GA & MCN Improvements
**Related items:** MCO-1735, MCO-1736, MCO-1775, MCO-1798; [MCO PR #5411](https://github.com/openshift/machine-config-operator/pull/5411); [MCO PR #6454](https://github.com/openshift/machine-config-operator/pull/6454); [openshift/api PR #2678](https://github.com/openshift/api/pull/2678)
**Feature status at time of writing:** In Progress, targeted 5.1

---

## 1. Test Plan Identifier

`TP-MCO-1506-v1` — OpenShift Image Mode State Reporting GA; revision 1; 2026-09-16.

## 2. Introduction

### 2.1 Purpose

This plan defines MCO test planning and GA evidence for OCPSTRAT-1282, implemented by MCO-1506. The feature requirement is to provide accurate, granular, and consistent MachineConfigNode (MCN) state reporting so that customers, troubleshooters, and automation can make decisions from reliable node-update state. The plan preserves existing coverage and identifies only the delta work needed to show readiness.

### 2.2 Scope

MCO-1506 extends the completed MCO-836 MCN baseline; it is not greenfield work. Its Jira scope is to make the experience consistent across standard node updates and on-cluster image-mode updates, and to populate MachineConfigPool (MCP) status consistently with the original MCN enhancement design. It also includes the test/readiness evidence needed for GA.

The plan covers AWS and GCP as primary environments. Other supported platforms may be represented by applicable existing CI jobs; this plan intentionally does not define a fixed platform or architecture matrix. MCO-1506 explicitly excludes MCN-triggered updates and observability through customizable metrics.

### 2.3 Background — current behavior relevant to testing

The existing serial, disruptive `ImageModeStatusReporting` suite is declared in [`test/extended/image_mode_status_reporting.go:31-110`](https://github.com/openshift/machine-config-operator/blob/main/test/extended/image_mode_status_reporting.go#L31-L110). Its five cases cover MCN/node properties, image and non-image condition transitions, and MCP machine counts. The shared assertions in [`test/extended/machineconfignode.go:30-409`](https://github.com/openshift/machine-config-operator/blob/main/test/extended/machineconfignode.go#L30-L409) validate `status.configImage`, `UpdateFiles`, `UpdateOS`, and `ImagePulledFromRegistry` behavior.

When the feature gate is enabled, [`pkg/upgrademonitor/upgrade_monitor.go:145-180`](https://github.com/openshift/machine-config-operator/blob/main/pkg/upgrademonitor/upgrade_monitor.go#L145-L180) uses the split conditions, and [`pkg/upgrademonitor/upgrade_monitor.go:290-342`](https://github.com/openshift/machine-config-operator/blob/main/pkg/upgrademonitor/upgrade_monitor.go#L290-L342) reports `status.configImage` and avoids an unchanged status apply. The node controller retains condition-migration behavior in [`pkg/controller/node/node_controller.go:2175-2235`](https://github.com/openshift/machine-config-operator/blob/main/pkg/controller/node/node_controller.go#L2175-L2235). The split-condition implementation is tracked by [MCO PR #5411](https://github.com/openshift/machine-config-operator/pull/5411).

The separately owned InternalReleaseImage (IRI) suite is not a status-reporting suite: [`test/e2e-iri/README.md`](https://github.com/openshift/machine-config-operator/blob/main/test/e2e-iri/README.md) assigns it to InternalReleaseImage controller behavior for Agent Installer OpenShift-Virt `NoRegistryClusterInstall`, and [`test/e2e-iri/OWNERS`](https://github.com/openshift/machine-config-operator/blob/main/test/e2e-iri/OWNERS) has separate owners. Its dedicated `test-e2e-iri` Makefile target is therefore outside this plan's scope.

### 2.4 References

| Reference | Relevance |
| --- | --- |
| [OCPSTRAT-1282](https://redhat.atlassian.net/browse/OCPSTRAT-1282) | Feature requirement: consistent reporting for standard and image-mode updates, accurate MCP status, and GA/readiness. |
| [MCO-1506](https://redhat.atlassian.net/browse/MCO-1506) | Implementation epic; records the 7-day green-test evidence required on all platforms selected by API feature-promotion verification. |
| [MCO-1735](https://redhat.atlassian.net/browse/MCO-1735) | Readiness-score monitoring and governing evidence process. |
| [MCO-1736](https://redhat.atlassian.net/browse/MCO-1736) and [openshift/api PR #2738](https://github.com/openshift/api/pull/2738) | Feature-gate graduation dependency. |
| [MCO-1775](https://redhat.atlassian.net/browse/MCO-1775) and [MCO-1798](https://redhat.atlassian.net/browse/MCO-1798) | Legacy-condition retirement and MCN print-column follow-up. |
| [openshift/api PR #2678](https://github.com/openshift/api/pull/2678) | Open, held print-column/API cleanup relevant to the follow-up. |
| [MCO PR #6454](https://github.com/openshift/machine-config-operator/pull/6454) | Related suite-routing change; use as evidence only after the selected route is observed. |

## 3. Test Items

- The `ImageModeStatusReporting` feature gate and MCN API fields: `status.configImage`, `UpdateFiles`, `UpdateOS`, and `ImagePulledFromRegistry`.
- MCN-to-MCP status reporting, including updated and degraded machine counts.
- The five existing disruptive ImageModeStatusReporting cases and their shared MCP-count oracle.
- Existing MCN and OSStreams Polarion coverage, including private MCN cases 69187, 69197, 69205, and 85901 and OSStreams cases 88122 and 88203.
- CI selection, component-readiness, and API feature-promotion evidence.

## 4. Features to Be Tested

| ID | Requirement | Priority |
| --- | --- | --- |
| F1 | MCN status MUST remain consistent for standard node updates and on-cluster image-mode updates. | High |
| F2 | Image-mode reporting MUST expose the current MCN contract, including `status.configImage`, `UpdateOS`, `UpdateFiles`, and `ImagePulledFromRegistry`, with transitions appropriate to update type. | High |
| F3 | MCP updated and degraded machine counts MUST align with the original MCN status-reporting design during the covered update transitions. | High |
| F4 | Existing ImageModeStatusReporting and MCN/OSStreams baseline coverage MUST remain traceable and be revalidated against the current contract without expanding its committed scope. | High |
| F5 | The selected CI route and readiness evidence MUST support GA evaluation; component readiness greater than 95% is the binding criterion. | High |
| F6 | Any additional migration, unchanged-status-apply, or failure-path coverage MUST be explicitly decided and traced before it is treated as GA coverage. | Medium |

## 5. Features Not to Be Tested

| Exclusion | Rationale |
| --- | --- |
| MCN-triggered updates | MCO-1506 states that using MCN resources to trigger updates is future work needing separate refinement, Technology Preview implementation, and soak; this plan tests reporting rather than a new update control plane. |
| Observability through customizable metrics | MCO-1506 excludes flexible/customizable metrics because they require product and engineering refinement and a separate enhancement. |
| InternalReleaseImage/IRI suite and `test-e2e-iri` | Verified separate ownership: this suite tests InternalReleaseImage controller behavior for Agent Installer OpenShift-Virt `NoRegistryClusterInstall`, not MCO-1506 status reporting. |
| Feature-gate graduation, legacy-condition retirement, and print-column implementation | MCO-1736, MCO-1775, and MCO-1798 own those changes. This plan may consume their outcomes as evidence but does not test their implementation as MCO-1506 scope. |
| A fixed cross-platform or architecture matrix | AWS and GCP are primary; coverage on other supported platforms follows existing applicable CI jobs rather than a matrix invented by this plan. |

## 6. Approach

### 6.1 Strategy

Use the existing serial disruptive suite as the baseline for functional regression, then make only narrowly scoped expectation, traceability, and oracle updates where the current MCN contract requires them. Review CI selection and readiness records as release evidence; do not infer a route from configuration alone. Manual review is appropriate for API/Polarion traceability and evidence records, while automated suites supply transition and regression evidence. Upgrade and rollback coverage is N/A because no supported upgrade or rollback commitment is recorded in the confirmed scope.

### 6.2 Existing / Baseline Coverage (not new work)

| ID | Existing coverage | Status | Test path / ID | Related item |
| --- | --- | --- | --- | --- |
| B1 | MCN properties match the associated node with on-cluster builds in a custom MCP, falling back to `master` when no worker pool exists. | Implemented | [`image_mode_status_reporting.go:45-59`](https://github.com/openshift/machine-config-operator/blob/main/test/extended/image_mode_status_reporting.go#L45-L59) | MCO-1506; MCO-1513 |
| B2 | MCN conditions transition during an image-based update with on-cluster builds. | Implemented | [`image_mode_status_reporting.go:61-75`](https://github.com/openshift/machine-config-operator/blob/main/test/extended/image_mode_status_reporting.go#L61-L75) | MCO-1775; [PR #5411](https://github.com/openshift/machine-config-operator/pull/5411) |
| B3 | MCN conditions transition during a non-image update with on-cluster builds. | Implemented | [`image_mode_status_reporting.go:77-91`](https://github.com/openshift/machine-config-operator/blob/main/test/extended/image_mode_status_reporting.go#L77-L91) | MCO-1775; [PR #5411](https://github.com/openshift/machine-config-operator/pull/5411) |
| B4 | MCP machine counts transition during a MachineConfig update in a default MCP. | Implemented | [`image_mode_status_reporting.go:93-100`](https://github.com/openshift/machine-config-operator/blob/main/test/extended/image_mode_status_reporting.go#L93-L100) | MCO-1506 |
| B5 | MCP machine counts transition while on-cluster image mode is enabled in a default MCP. | Implemented | [`image_mode_status_reporting.go:102-119`](https://github.com/openshift/machine-config-operator/blob/main/test/extended/image_mode_status_reporting.go#L102-L119) | MCO-1506 |
| B6 | MCN/OSStreams Polarion baseline, including MCN 69187, 69197, 69205, 85901 and OSStreams 88122, 88203. | Implemented | Polarion cases; existing MCN extended coverage | MCO-1512; MCO-1513 |
| B7 | MCP expected-count calculation based on node annotations. | Implemented | [`image_mode_status_reporting.go:471-560`](https://github.com/openshift/machine-config-operator/blob/main/test/extended/image_mode_status_reporting.go#L471-L560) | MCO-1506 |

### 6.3 New Coverage Needed

| ID | Delta work | Status | Traceability and disposition |
| --- | --- | --- | --- |
| N1 | Re-baseline the five existing cases with small expectation and suite updates for the current MCN fields and split conditions; retain their five-case scope. | Proposed | F1, F2, F4; baseline exists in B1-B5, so this is not a new test suite. |
| N2 | Refresh MCN/OSStreams Polarion references for split conditions, legacy-field follow-up, print-column follow-up, and observed suite placement. | Proposed | F4; baseline exists in B6. |
| N3 | Reconcile the MCP expected-count oracle with MCN-driven MCP status semantics and retain compatibility checks that remain valid. | Proposed | F3; baseline exists in B7. |
| N4 | Obtain CI selection/completion and Sippy-reporting evidence for the actual relevant route after related routing changes. | Blocked | F5; no verified route/evidence package currently exists. Waits on MCO-1735 and accepted routing changes. |
| N5 | Assemble the GA readiness evidence package, including component-readiness results and the API feature-promotion platform window. | Blocked | F5; no current measured rate is asserted. Waits on MCO-1735's governing measurement and evidence contract. |
| N6 | Decide whether targeted gate-off-to-gate-on legacy-condition migration coverage is required before GA. | Proposed | F6; no committed coverage is identified. Depends on MCO-1775/MCO-1736 disposition. |
| N7 | Decide whether targeted regression coverage is required for unchanged desired-image/status SSA behavior. | Proposed | F6; no committed coverage is identified. The existing no-diff guard is not claimed as test coverage. |
| N8 | Decide whether targeted image-pull, file-application, and OS-application status-reporting failure-path coverage is required. | Proposed | F6; no committed coverage is identified. |

### 6.4 Regression

Re-run the five-case `ImageModeStatusReporting` serial disruptive suite, its shared MCN helpers, the applicable MCN/OSStreams Polarion baseline cases, and the CI jobs selected by the current feature-promotion/readiness process. Re-run AWS and GCP as primary environments; preserve results from any other supported-platform jobs selected by existing CI.

## 7. Item Pass/Fail Criteria

- Functional status: F1-F3 pass when the existing or approved delta cases show the documented MCN fields, split-condition transitions, and MCP counts for their update type; contradictory, missing, or persistently mismatched state fails.
- Baseline and regression: F4 passes when B1-B7 and their approved expectation/traceability updates retain the five-case scope and complete without a status-reporting regression; an obsolete assertion, missing traceability, or regression fails.
- GA readiness: F5 passes only when Component Readiness is greater than 95%. MCO-1506 also records seven days of green tests on every platform required by the API `verify-feature-promotion` check as the governing GA evidence requirement. The current measured rate is unknown and is not asserted here.
- Evidence governance: SNO pass-rate and 3-of-3 Sippy rules are evidence/dependency inputs governed by MCO-1735's readiness process, including its measurement window, denominator, retry, missing-run, and reporting treatment. They are not independent binding criteria in this plan unless that owning process makes them required; lack of the required governing evidence prevents an approval decision.

## 8. Suspension Criteria and Resumption Requirements

Suspend the affected test execution when an AWS or GCP environment is unavailable, a blocking image build or cluster failure makes status results uninterpretable, the selected CI route cannot report, or a critical status-reporting defect is found. Resume when the environment or routing issue is corrected, the defect has an owner and recorded disposition, and the affected scenario can be rerun with preserved evidence. Suspend GA-evidence review when the MCO-1735 evidence contract or API feature-promotion results are unavailable; resume when their governing records are available.

## 9. Test Deliverables

- This version-controlled test plan.
- Results and artifacts for the baseline and any approved delta execution.
- Updated Polarion traceability where N2 is approved.
- Defect records for confirmed failures.
- The MCO-1735/API feature-promotion readiness evidence used for the GA decision.

## 10. Testing Tasks

1. Review the MCN API/status contract and reconcile B1-B7 expectations with it.
2. Execute or collect the applicable baseline and regression evidence on AWS and GCP.
3. Update only approved e2e expectations, suite references, Polarion links, and the MCP oracle.
4. Verify the actual CI route and preserve its completion/reporting evidence.
5. Collect the governing readiness evidence and record any N6-N8 disposition before treating it as coverage.

## 11. Environmental Needs

AWS and GCP clusters capable of the existing serial disruptive MCO tests, with access to MCN, MCP, on-cluster image mode, and the current feature-gate/CI configuration. Existing CI may supply applicable evidence for other supported platforms. SNO and 3-of-3 execution details are supplied by the MCO-1735 governing readiness process; this plan does not prescribe a topology matrix.

## 12. Responsibilities

| Role | Responsibility |
| --- | --- |
| MCO development | Maintain the status-reporting implementation and resolve confirmed defects. |
| MCO QE | Maintain approved test expectations and Polarion traceability; evaluate baseline results. |
| Readiness owner | Under MCO-1735, define and publish the readiness measurement/evidence contract and investigate failures. |
| API/feature-gate owner | Under MCO-1736, run the graduation process and `verify-feature-promotion` evaluation. |
| Release engineering/CI owners | Provide selected-job and platform-window evidence. |
| IRI owners | Own InternalReleaseImage controller and `test-e2e-iri` coverage independently of this plan. |

## 13. Staffing and Training Needs

No additional staffing or training need is identified. Execution requires the existing MCO QE/development familiarity with disruptive tests, MCN/MCP status, and the MCO-1735 readiness process.

## 14. Schedule

Complete N1-N3 early enough for their results to enter the current release evidence window. Collect N4-N5 only after the selected CI route is observable and MCO-1735 provides its governing evidence contract. Resolve the N6-N8 coverage dispositions before GA approval. The target release is OpenShift 5.1; no calendar date beyond the release target is committed by this plan.

## 15. Risks and Contingencies

| Risk | Contingency |
| --- | --- |
| Serial disruptive tests are affected by cluster timing or instability. | Preserve job artifacts, distinguish infrastructure failures from status defects, and rerun only through the governing process. |
| CI routing or 3-of-3 reporting does not produce readiness data. | Hold readiness evaluation and track the required route/evidence through MCO-1735. |
| API feature-gate, legacy-condition, or print-column dependencies remain open. | Keep their implementation out of MCO-1506 coverage and consume only verified outcomes. |
| New coverage decisions expand beyond confirmed scope. | Require an explicit N6-N8 disposition before scheduling or claiming additional tests. |

## 16. Approvals

The MCO QE lead, MCO feature owner, readiness owner, and API/feature-gate owner should approve the plan and the evidence used for GA. Publication required requester confirmation before commit/push; that confirmation applies to this revision, which is being made on the existing review branch `mco-1506/status-reporting-test-plan`.

## 17. Quality Stories (proposed, not filed automatically)

### Q1 — Capture CI selection and reporting evidence

**Description:** For F5/N4, define the evidence record that proves the selected ImageModeStatusReporting route ran, completed, and reported to the governing readiness process.

**Acceptance criteria:**

- [ ] The selected job or jobs and their result links are recorded.
- [ ] Completion and Sippy-reporting evidence is available to MCO-1735.
- [ ] Any routing gap is recorded as a dependency rather than treated as a passing result.

### Q2 — Assemble GA readiness evidence

**Description:** For F5/N5, assemble the MCO-1735 and API feature-promotion evidence needed to evaluate the greater-than-95% Component Readiness criterion and required seven-day platform window.

**Acceptance criteria:**

- [ ] Component Readiness evidence is recorded using the MCO-1735 governing process.
- [ ] The API feature-promotion-required platform window is identified and its seven-day green evidence is linked.
- [ ] The record does not claim a current measurement until the governing result is available.

### Q3 — Decide legacy-condition migration coverage

**Description:** For F6/N6, record whether targeted gate-off-to-gate-on legacy-condition migration coverage is required, deferred, or not applicable under MCO-1775 and MCO-1736.

**Acceptance criteria:**

- [ ] The owner records an explicit disposition.
- [ ] Any approved case traces to F6 and the relevant dependency.
- [ ] No unapproved migration scenario is claimed as GA coverage.

### Q4 — Decide unchanged-status SSA regression coverage

**Description:** For F6/N7, record whether the unchanged desired-image/status SSA no-diff behavior needs targeted regression coverage.

**Acceptance criteria:**

- [ ] The owner records an explicit disposition.
- [ ] Any approved case names the observable no-diff behavior and traces to F6/N7.
- [ ] The implementation guard alone is not represented as executed coverage.

### Q5 — Decide status-reporting failure-path coverage

**Description:** For F6/N8, record whether targeted image-pull, file-application, and OS-application failure-path reporting coverage is required.

**Acceptance criteria:**

- [ ] The owner records an explicit disposition for each proposed failure-path area.
- [ ] Any approved case traces to F6/N8 and defines the expected MCN status evidence.
- [ ] Deferred areas retain their rationale and are not counted as executed coverage.

## 18. External Dependencies / Unresolved Items

| Dependency or unresolved item | Waiting on |
| --- | --- |
| MCO-1735 readiness measurement and evidence contract, including any SNO or 3-of-3 Sippy measurement rules and the selected reporting window. | MCO readiness owner and the governing feature-promotion/readiness process. |
| MCO-1736 feature-gate graduation. | API/feature-gate owner and [openshift/api PR #2738](https://github.com/openshift/api/pull/2738). |
| MCO-1775 legacy `AppliedFilesAndOS` condition retirement. | MCO owners after graduation and regression-reference disposition. |
| MCO-1798 MCN print columns. | MCO/API owners after graduation. |
| Open API print-column cleanup. | [openshift/api PR #2678](https://github.com/openshift/api/pull/2678), which is open and held. |

---

*Generated test plan for OCPSTRAT-1282 / MCO-1506. Paths and line numbers reference `main` at time of writing (2026-09-16).*

# MCO-1506: Image Mode Status Reporting test plan

## Test Plan Identifier

`OCPSTRAT-1282-mco-1506-status-reporting-test-plan` — target release: OpenShift 5.1.

## Introduction and Scope

This plan covers Machine Config Operator (MCO) image-mode state-reporting readiness for general availability (GA). It is the MCO execution plan for OCPSTRAT-1282, **OpenShift Image Mode State Reporting GA**, implemented through MCO-1506. MCO-1506 extends the completed MCO-836 MCN baseline under OCPSTRAT-1845; it does not reopen that predecessor's scope.

The core API and image-mode implementation are existing baseline. Test work re-baselines and updates existing coverage, validates routing and readiness evidence, and makes remaining coverage decisions explicit. AWS and GCP are the primary execution environments. Existing CI supplies other platform coverage where applicable; this plan does not prescribe a fixed platform or architecture matrix.

### References

| Reference | Relevance |
| --- | --- |
| OCPSTRAT-1282 | Parent feature and GA outcome. |
| MCO-1506 | Implementation epic and this plan's primary scope. |
| MCO-836 / OCPSTRAT-1845 | Completed predecessor baseline. |
| MCO-1735, MCO-1736 | Component-readiness monitoring and feature-gate graduation. |
| MCO-1775, MCO-1798 | Legacy condition removal and MCN print-column follow-up. |
| MCO-1512, MCO-1513 | Existing pre-merge and e2e automation work. |
| openshift/api PR #2678 | Open, conflicting cleanup dependency; not new scope for this plan. |
| MCO PR #6454 | Moves related OCB/OSStreams suites to `longduration`; routing evidence. |

## Test Items

- The `ImageModeStatusReporting` feature gate and MCN status API: `status.configImage`, split `UpdateFiles` and `UpdateOS` conditions, and `ImagePulledFromRegistry`.
- MCN-to-MCP status reporting, including updated and degraded machine counts.
- Existing disruptive ImageModeStatusReporting scenarios in `test/extended/image_mode_status_reporting.go`.
- Existing MCN and OSStreams Polarion cases, including private MCN cases 69187, 69197, 69205, and 85901 and OSStreams cases 88122 and 88203.
- CI routing and feature-promotion/component-readiness evidence.

## Features to Be Tested

| ID | Feature or delta | Purpose / observable validation | Status | Evidence / related work |
| --- | --- | --- | --- | --- |
| F-01 | Existing MCN property reporting | Confirm MCN properties correspond to the associated node through image-mode enablement and cleanup. | Implemented baseline | `test/extended/image_mode_status_reporting.go`; MCO-1506, MCO-1513. |
| F-02 | Existing image and file update transitions | Confirm image updates use `UpdateOS` and `ImagePulledFromRegistry`; non-image updates use `UpdateFiles`. | Implemented baseline | Existing extended scenarios and `test/extended/machineconfignode.go`; MCO-1775 follow-up. |
| F-03 | Existing MCP count reporting | Confirm updated/degraded MCP counts agree with the test oracle during regular and on-cluster image-mode updates. | Implemented baseline | Existing extended count scenarios; MCO-1506. |
| F-04 | Re-baseline the five existing scenarios | Update the five cases' condition and field expectations to the current MCN contract; retain their scope rather than creating a broad suite. | Proposed | `test/extended/image_mode_status_reporting.go`; MCO-1513. |
| F-05 | Polarion and suite traceability update | Update MCN/OSStreams references for condition split, legacy-field and print-column follow-up, and current suite placement. | Proposed | MCN/OSStreams Polarion cases; MCO-1775, MCO-1798, PR #6454. |
| F-06 | MCP count oracle update | Reconcile the extended-test node-annotation oracle with MCN-driven MCP status semantics and document retained compatibility checks. | Proposed | `mcnAndNodeAnnotationMachineCountsMatch`; MCO-1506. |
| F-07 | CI/readiness routing | Demonstrate that relevant scenarios are selected, complete, and report to readiness tooling after routing changes. | Blocked | PR #6454 and MCO-1735; required 3-of-3 Sippy reporting is blocked pending timeout resolution. |
| F-08 | GA readiness evidence | Collect component-readiness evidence. Greater than 95% component readiness is binding; SNO must meet a 95% pass rate and 3-of-3 Sippy reporting must be restored. | Blocked | MCO-1735, MCO-1736. |
| F-09 | Gate transition coverage | Decide and, if committed, cover migration from gate-off legacy conditions to gate-on split conditions. No test is claimed here. | Proposed | Controller migration code; MCO-1775. |
| F-10 | Desired-image SSA no-diff behavior | Decide whether targeted regression coverage is required for unchanged desired-image/status application behavior. No test is claimed here. | Proposed | `pkg/upgrademonitor/upgrade_monitor.go`; MCO-1506. |
| F-11 | Status-reporting failure paths | Decide and, if committed, cover image-pull, file-application, and OS-application failure reporting. No test is claimed here. | Proposed | Daemon status updates and MCN conditions; MCO-1506. |

## Features Not to Be Tested with Rationale

| Exclusion | Rationale |
| --- | --- |
| MCN-triggered node updates | MCO-1506 identifies using MCN resources to trigger updates as a future, high-impact design requiring separate refinement and Technology Preview soak; this plan verifies reporting, not a new update control plane. |
| Customizable observability metrics | MCO-1506 says flexible/customizable metrics need product and engineering refinement plus a separate enhancement; they are not part of the original MCN/state-reporting enhancement. |
| Implementing feature-gate graduation, legacy-field removal, or print columns | MCO-1736, MCO-1775, and MCO-1798 own those changes. This plan tests and traces their effect only when owners make it available. |
| New fixed cross-platform or architecture matrix | The primary environments are AWS and GCP; any other coverage follows existing CI applicability rather than an invented matrix. |

## Approach

1. **Baseline regression:** execute and preserve the five existing serial, disruptive extended scenarios. They exercise custom/default MCP selection, image and non-image transitions, and MCP counts. They include SNO-aware behavior and must not be represented as newly created tests.
2. **Delta updates:** update assertions and Polarion traceability only where the current contract requires it: split conditions, `status.configImage`, legacy condition/print-column follow-up, suite placement, and the MCP count oracle.
3. **Readiness routing:** verify the selected suite reaches CI and reporting. PR #6454 proves related OCB and OSStreams suites route through `longduration`; the ImageModeStatusReporting suite's actual selected route must be observed, not inferred.
4. **GA evidence:** review component readiness first. The gate must not be recommended for GA below a component-readiness score greater than 95%. Require a SNO pass rate of at least 95% and working 3-of-3 Sippy reporting; record the evidence window and job identifiers.

Manual review is used for API output, assertions, Polarion traceability, and readiness records. Automated extended and existing CI coverage supplies regression evidence. Upgrade/rollback is not a committed scenario in available scope; migration behavior is tracked as F-09 rather than assumed.

### Coverage inventory

| Scenario | Purpose | Status | Test location / evidence | Related Jira or PR |
| --- | --- | --- | --- | --- |
| B-01 | MCN properties with OCB in a custom MCP, falling back to `master` when no worker exists. | Implemented | `image_mode_status_reporting.go`, first scenario. | MCO-1506, MCO-1513 |
| B-02 | MCN conditions during an image-based update. | Implemented | Same file, second scenario; shared transition helper. | MCO-1775 |
| B-03 | MCN conditions during a non-image update. | Implemented | Same file, third scenario; shared transition helper. | MCO-1775 |
| B-04 | Default-MCP machine-count transitions for a MachineConfig update. | Implemented | Same file, fourth scenario. | MCO-1506 |
| B-05 | Default-MCP machine-count transitions while enabling OCB. | Implemented | Same file, fifth scenario. | MCO-1506 |
| D-01 | Re-baseline B-01 through B-05 against current condition and field expectations. | Proposed | Existing five cases only; no new suite asserted. | MCO-1513 |
| D-02 | Refresh MCN/OSStreams Polarion references and suite placement. | Proposed | Private MCN/OSStreams cases; `longduration` routing from PR #6454. | MCO-1775, MCO-1798, PR #6454 |
| D-03 | Validate/revise the MCP expected-count oracle. | Proposed | `mcnAndNodeAnnotationMachineCountsMatch`. | MCO-1506 |
| D-04 | Verify CI selection and Sippy reporting for the relevant route. | Blocked | Job configuration/results must be supplied by owners. | MCO-1735, PR #6454 |
| D-05 | Produce readiness evidence for GA. | Blocked | Component-readiness and SNO/Sippy reports. | MCO-1735, MCO-1736 |
| D-06 | Establish gate-off/on migration coverage decision. | Proposed | Existing controller migration; no committed automated case. | MCO-1775 |
| D-07 | Establish desired-image SSA no-diff coverage decision. | Proposed | Existing no-diff guard; no committed automated case. | MCO-1506 |
| D-08 | Establish failure-path coverage decision. | Proposed | Existing daemon condition updates; no committed automated case. | MCO-1506 |

## Item Pass/Fail Criteria

| Item | Pass evidence | Fail condition |
| --- | --- | --- |
| B-01 through B-03 | Existing tests observe the gate-on MCN fields and split condition transitions expected for their update type; final `Updated=True` leaves other conditions false as asserted. | Missing, legacy-only, contradictory, or incorrectly transitioned status fields/conditions. |
| B-04 through B-05 | Actual MCP updated/degraded counts agree with the approved expected-count oracle throughout update and cleanup. | A persistent mismatch after the test's retry/timing allowance. |
| D-01 through D-03 | Review-approved updates preserve the five-case scope and align assertions, traceability, and oracle with current API semantics. | An update recreates coverage broadly, asserts obsolete semantics, or lacks traceability. |
| D-04 | CI proves selection, completion, and reporting for the agreed route. | Relevant tests are absent, time out, fail, or do not report as required. |
| D-05 | Component readiness is greater than 95%, the SNO pass rate is at least 95%, and 3-of-3 Sippy reporting evidence is attached. | Component readiness is 95% or lower, SNO pass rate is below 95%, 3-of-3 reporting is absent, or a required dependency is unresolved. |
| D-06 through D-08 | Owners record an explicit disposition: implement targeted coverage, defer with rationale, or mark not applicable. | The GA decision relies on an unrecorded coverage assumption. |

## Suspension/Resumption Criteria

Suspend affected execution when a required AWS/GCP environment is unavailable, a blocking image build or cluster failure prevents interpretation, the selected CI route fails to report, or a critical status-reporting defect is found. Resume after the environment/routing defect is corrected, a reproducible defect has an owner and disposition, and the affected baseline or delta scenario can be re-run with preserved evidence.

## Test Deliverables

- This version-controlled plan and its approved updates.
- Results for B-01 through B-05, including CI log identifiers and relevant Polarion updates.
- Delta review records for D-01 through D-08 and defect records for failures.
- Component-readiness, SNO, and 3-of-3 Sippy evidence used for the GA decision.
- Proposal-only Jira quality work items listed below; they are not filed by this plan.

## Testing Tasks

| Order | Task | Output |
| --- | --- | --- |
| 1 | Confirm current feature-gate/API contract and related dependency status. | Reviewed F-01 through F-03 semantics. |
| 2 | Execute/review B-01 through B-05 on AWS and GCP. | Baseline regression evidence. |
| 3 | Perform D-01 through D-03 updates after owner approval. | Focused existing-test and Polarion/oracle changes. |
| 4 | Verify D-04 CI selection and reporting. | Route and reporting evidence. |
| 5 | Track D-05 readiness and obtain decisions for D-06 through D-08. | GA evidence and documented coverage dispositions. |

## Environmental Needs

- AWS and GCP OpenShift 5.1 candidate clusters with an administrator test identity, MCO, MCN resources, and a usable default/compatible MCP.
- Image-mode/on-cluster build capability for B-01, B-02, and B-05; the tests create MachineOSConfig resources and apply/remove test MachineConfigs.
- Access to CI artifacts, component-readiness reporting, Sippy evidence, and the applicable Polarion records.
- SNO evidence meeting the required pass-rate criterion. Existing test logic already adapts timing and transition expectations for SNO.

## Responsibilities

| Role | Responsibility |
| --- | --- |
| MCO developers | Confirm API/implementation semantics; own defects and code changes. |
| QE | Maintain existing test and Polarion traceability, execute evidence collection, and propose focused coverage updates. |
| CI/release engineering | Confirm suite routing, reporting, and feature-promotion evidence. |
| Feature/readiness owner | Verify SNO and 3-of-3 evidence and make the GA readiness decision. |
| Product/architecture owners | Own deferred MCN-triggered updates and customizable metrics. |

## Staffing and Training Needs

N/A. Execution needs working knowledge of MCO update states, MCN/MCP resources, on-cluster image mode, and existing CI/Polarion reporting.

## Schedule

1. Re-baseline existing cases and traceability early enough to collect the readiness window.
2. Complete CI-route verification before feature-gate graduation review.
3. Collect readiness evidence and resolve documented coverage dispositions before the OpenShift 5.1 GA decision.

## Risks and Contingencies

| Risk | Contingency |
| --- | --- |
| Serial/disruptive timing, especially on SNO, obscures a short-lived transition. | Preserve existing SNO-aware tolerance, capture artifacts, and distinguish infrastructure interruptions from product failures. |
| PR #6454 routing is not present in the tested payload or 3-of-3 still times out. | Hold D-04/D-05 readiness evidence, identify the route owner, and re-run after the routing fix is available. |
| Open API cleanup or child work changes the observed contract. | Track MCO-1736, MCO-1775, MCO-1798, MCO-1512/1513, MCO-1735, and API PR #2678; do not expand this plan into their implementation. |
| High component readiness masks missing required evidence. | Treat greater than 95% readiness as necessary and also require SNO pass rate of at least 95% plus 3-of-3 Sippy reporting. |
| Proposed gaps become assumed coverage. | Require a recorded disposition for D-06 through D-08 before GA review. |

## Approvals

Approval is required from the MCO QE lead, MCO feature owner, CI/release engineering representative, and feature/readiness owner. Approval records must identify the evidence set used for the OpenShift 5.1 GA decision.

## Proposal-Only Jira Quality Stories

These are proposed work items derived from F-04 through F-11. They are not Jira issues and must not be filed automatically.

### QS-01 — Re-baseline the five ImageModeStatusReporting extended cases

**Description:** Update only the five existing scenarios in `test/extended/image_mode_status_reporting.go` for current split condition and MCN image-field expectations.

**Acceptance criteria:** All five existing scenario declarations remain; assertions distinguish `UpdateOS`, `UpdateFiles`, and `ImagePulledFromRegistry` as applicable; review identifies no new broad suite.

### QS-02 — Refresh MCN and OSStreams Polarion status-reporting traceability

**Description:** Update existing MCN/OSStreams references for split conditions, MCO-1775 legacy-field follow-up, MCO-1798 print-column follow-up, and suite placement.

**Acceptance criteria:** Affected Polarion cases link to correct implementation/dependency work; obsolete `AppliedFilesAndOS` expectations are marked for retirement rather than silently retained; routing references match observed suites.

### QS-03 — Reconcile the MCN-driven MCP count oracle

**Description:** Review and update, if needed, the expected-count logic used by the two existing MCP count scenarios.

**Acceptance criteria:** The approved oracle explains how updated/degraded counts map to MCN status and any retained node-annotation compatibility checks; the two existing count scenarios pass against that oracle.

### QS-04 — Verify ImageModeStatusReporting CI and Sippy routing

**Description:** Establish the exact selected CI route and validate completion and Sippy reporting after relevant suite-routing changes.

**Acceptance criteria:** Evidence identifies the job/suite route, successful execution, and Sippy reporting for agreed tests; the 3-of-3 timeout is resolved so the tests report to Sippy.

### QS-05 — Assemble component-readiness GA evidence

**Description:** Monitor the approved status-reporting signal and prepare the evidence package for feature-gate graduation.

**Acceptance criteria:** Evidence shows component readiness greater than 95%, SNO pass rate of at least 95%, and 3-of-3 Sippy reporting; blockers link to Jira records.

### QS-06 — Decide gate-off/on condition-migration coverage

**Description:** Decide whether a focused regression is needed for migration from `AppliedFilesAndOS` to split gate-on conditions.

**Acceptance criteria:** The owner records one of: targeted test implemented, deferred with rationale, or not applicable; the decision states its relationship to MCO-1775.

### QS-07 — Decide desired-image SSA no-diff regression coverage

**Description:** Evaluate focused coverage for unchanged desired-image status updates that use the no-diff guard.

**Acceptance criteria:** The owner records whether coverage is implemented, deferred with rationale, or not applicable; any implemented test is targeted to the existing guard rather than a new broad suite.

### QS-08 — Decide status-reporting failure-path coverage

**Description:** Evaluate focused coverage for image-pull, file-application, and OS-application status-reporting failures.

**Acceptance criteria:** The owner records a disposition for each failure class; any committed coverage asserts the corresponding MCN condition and does not claim unimplemented tests.

## Open Questions

1. Which exact AWS/GCP topology and architecture combinations are required for OpenShift 5.1 readiness evidence, and which additional existing CI jobs count as applicable coverage?
2. Which reporting window and sample size will document the required SNO pass rate of at least 95% alongside the binding greater-than-95% component-readiness criterion?
3. Which job identifier and Sippy query will prove the required 3-of-3 reporting after the PR #6454 routing change is available in a payload?
4. Which of D-06 through D-08 must be committed before GA, versus deferred to their owning stories with explicit rationale?

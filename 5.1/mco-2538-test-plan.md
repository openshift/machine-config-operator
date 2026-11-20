# Test Plan — MCO-2538 Kubernetes/OpenShift 1.37 dependency update

## Overview

| Section | Purpose |
|---|---|
| [Header metadata](#header) | Jira traceability and freshness pin |
| [1. Introduction](#1-introduction) | Why this matters, what's promised, current code state |
| [2. Test Strategy](#2-test-strategy) | Ownership boundary — what MCO tests vs. what's cross-repo/upstream |
| [3. Features to Be Tested](#3-features-to-be-tested) | Scenario-ID table: what's tested, priority, status |
| [4. Features Not to Be Tested](#4-features-not-to-be-tested) | Explicit exclusions with rationale |
| [5. Interaction / Non-Interference Checks](#5-interaction--non-interference-checks) | What this must NOT break |
| [6. Target Environments](#6-target-environments) | Platform/arch/topology coverage |
| [7. Approach](#7-approach) | Test levels, security review tie-in |
| [8. Item Pass/Fail Criteria](#8-item-passfail-criteria) | Per-priority exit bar, GA gate if applicable |
| [9. Suspension/Resumption Criteria](#9-suspensionresumption-criteria) | When to stop/restart testing |
| [10. Test Deliverables](#10-test-deliverables) | What gets produced |
| [11. Testing Tasks](#11-testing-tasks) | Work breakdown |
| [12. Risks and Contingencies](#12-risks-and-contingencies) | Named uncertainties, impact, mitigation |
| [13. Approvals](#13-approvals) | Who signs off before publication |
| [14. External Dependencies / Unresolved Items](#14-external-dependencies--unresolved-items) | Real blockers only — not requester questions |

---

<a id="header"></a>
**Jira Feature (OCPSTRAT):** [OCPSTRAT-3618](https://redhat.atlassian.net/browse/OCPSTRAT-3618) — Upgrade to Kubernetes 1.37
**Jira Epic/Story (implementation ticket):** [MCO-2538](https://redhat.atlassian.net/browse/MCO-2538) — Update MCO dependencies to Kubernetes 1.37
**Related items:** MCO-2538 dependency: downstream OpenShift/Kubernetes rebase PR merge; CI definitions: `openshift/release` release-5.1 periodic configuration. No MCO PR, sibling epic, design document, or ProdSec review is asserted by this snapshot.
**Feature status at time of writing:** MCO-2538 is New, has fix version `openshift-5.1`, and is a child epic of the In Progress OCPSTRAT-3618 feature.
**Snapshot pin:** This plan reflects `openshift/machine-config-operator` at commit `e13c364100db61f0c860b1fc102f8a4f33245ac1` as of 2026-09-16 (the checked-out `release-5.1` commit, “Merge pull request #6528 from isabella-janssen/ocpbugs-116491”). It is an issue-and-CI-configuration snapshot, not an MCO PR snapshot. Re-verify scope if the referenced code has since changed (see Risks).

---

## 1. Introduction

### 1.1 Purpose

MCO-2538 keeps the Machine Config Operator aligned with the Kubernetes/OpenShift 1.37 dependency update so that potential downstream-rebase issues are found before merge. This plan defines only the required existing long-duration CI validation; it neither adds a test case nor creates a new e2e scenario. The Jira acceptance criteria are: “All stories in this epic must be completed.” “Go version is upgraded for MCO components.” and “CI is running successfully with the upgraded components against the 5.1/main branch.”

### 1.2 Scope

Per the Jira acceptance criteria, the feature is expected to deliver:

- “All stories in this epic must be completed.”
- “Go version is upgraded for MCO components.”
- “CI is running successfully with the upgraded components against the 5.1/main branch.”

The validation scope is the existing AWS and vSphere MCO long-duration periodic CI definitions listed in Section 3. Their generated Prow jobs check out MCO with `extra_refs.base_ref: release-5.1`; no result from a run is claimed in this plan.

### 1.3 Background — current behavior relevant to testing

Grounded in the checked-out `release-5.1` snapshot (rather than `main`):

- MCO’s Makefile supplies Go unit and e2e verification targets, but it does not define these periodic jobs; `make verify` includes e2e compilation, lint, template, and helper verification ([`Makefile`](../Makefile):131), while the long-duration CI definitions are owned by `openshift/release`.
- No MCO source update or MCO PR is asserted by the Jira snapshot. The required dependency on the downstream OpenShift/Kubernetes rebase PR merge remains external to this documentation-only change.
- Existing coverage is configured in [`openshift/release` `openshift-machine-config-operator-release-5.1__periodics.yaml`](https://github.com/openshift/release/blob/main/ci-operator/config/openshift/machine-config-operator/openshift-machine-config-operator-release-5.1__periodics.yaml): vSphere long-duration at line 412 and AWS FIPS/proxy long-duration at line 426. Generated job definitions are in [`openshift-machine-config-operator-release-5.1-periodics.yaml`](https://github.com/openshift/release/blob/main/ci-operator/jobs/openshift/machine-config-operator/openshift-machine-config-operator-release-5.1-periodics.yaml).

### 1.4 References

| Ref | Document |
|---|---|
| R1 | [OCPSTRAT-3618 — Upgrade to Kubernetes 1.37](https://redhat.atlassian.net/browse/OCPSTRAT-3618) |
| R2 | [MCO-2538 — Update MCO dependencies to Kubernetes 1.37](https://redhat.atlassian.net/browse/MCO-2538) |
| R3 | MCO-2538 dependency statement: downstream OpenShift/Kubernetes repository rebase PR merge; no MCO PR is identified in the Jira snapshot. |
| R4 | [`openshift/release` release-5.1 periodic source configuration](https://github.com/openshift/release/blob/main/ci-operator/config/openshift/machine-config-operator/openshift-machine-config-operator-release-5.1__periodics.yaml) and [generated periodic jobs](https://github.com/openshift/release/blob/main/ci-operator/jobs/openshift/machine-config-operator/openshift-machine-config-operator-release-5.1-periodics.yaml) |
| R5 | Existing MCO long-duration suite selected by the CI source as `openshift/machine-config-operator/longduration`; no new MCO test file is in scope. |

---

## 2. Test Strategy

MCO owns the dependency-update readiness evidence represented by the existing MCO long-duration CI targets in this plan. Release engineering owns the job definitions and execution infrastructure in `openshift/release`; the generated jobs explicitly check out `openshift/machine-config-operator` at `release-5.1`. The downstream OpenShift/Kubernetes rebase is an external prerequisite owned outside this documentation change. This plan deliberately does not re-test or modify upstream/vendored dependency implementation, release job configuration, or individual e2e test code: the requested scope is to run and assess the existing AWS FIPS/proxy and vSphere TechPreview long-duration validations after the dependency update is integrated.

---

## 3. Features to Be Tested

Status below records that the existing CI definition is present; it does not state that a job has run or passed.

| ID | Requirement / Scenario | Priority | Status | Test location / evidence |
|---|---|---|---|---|
| F1 | Run and assess the existing AWS FIPS/proxy MCO long-duration validation for the upgraded components: `periodic-ci-openshift-machine-config-operator-release-5.1-periodics-e2e-aws-mco-fips-proxy-longduration-1of3`, `periodic-ci-openshift-machine-config-operator-release-5.1-periodics-e2e-aws-mco-fips-proxy-longduration-2of3`, and `periodic-ci-openshift-machine-config-operator-release-5.1-periodics-e2e-aws-mco-fips-proxy-longduration-3of3`; each targets `e2e-aws-mco-fips-proxy-longduration`. | High | Implemented | `openshift/release` generated periodic jobs, lines 1067/1081, 1175/1189, and 1283/1297; source target `e2e-aws-mco-fips-proxy-longduration`. |
| F2 | Run and assess the existing vSphere TechPreview MCO long-duration validation for the upgraded components: `periodic-ci-openshift-machine-config-operator-release-5.1-periodics-e2e-vsphere-mco-tp-longduration-1of2` and `periodic-ci-openshift-machine-config-operator-release-5.1-periodics-e2e-vsphere-mco-tp-longduration-2of2`; each targets `e2e-vsphere-mco-tp-longduration`. | High | Implemented | `openshift/release` generated periodic jobs, lines 5296/5310 and 5404/5418; source target `e2e-vsphere-mco-tp-longduration`. |

---

## 4. Features Not to Be Tested

| Excluded | Rationale |
|---|---|
| New MCO test cases or new e2e scenarios | This plan is limited to existing long-duration CI validation; no new test-case addition or e2e scenario is requested. |
| CI job-definition changes | The jobs are defined and owned in `openshift/release`, not in this MCO repository; this change adds only the plan. |
| Upstream/downstream OpenShift/Kubernetes rebase implementation | MCO-2538 names the downstream rebase PR merge as a dependency; it is not MCO-owned validation work in this plan. |
| Individual Go-version or dependency-update implementation stories | The acceptance criterion is traced through the required CI evidence; implementation of the epic’s child stories is not tested by this documentation-only plan. |
| Polarion test cases, test names, or historical CI results | No such identifiers or results are supplied by the verified Jira and CI-configuration snapshot. |
| GA/default-graduation gate | N/A — no MCO-2538 or sibling story establishing a GA/default feature-gate graduation was found. |

---

## 5. Interaction / Non-Interference Checks

| ID | What must keep working | Why it's at risk |
|---|---|---|
| F1 | The existing AWS FIPS/proxy long-duration selection, its three shards, and target `e2e-aws-mco-fips-proxy-longduration` must continue to execute against the updated MCO dependencies. | The Kubernetes/OpenShift 1.37 dependency update can affect MCO runtime or test behavior; FIPS/proxy coverage is a distinct existing platform configuration. |
| F2 | The existing vSphere TechPreview long-duration selection, its two shards, and target `e2e-vsphere-mco-tp-longduration` must continue to execute against the updated MCO dependencies. | The dependency update can affect MCO runtime or test behavior in the vSphere TechPreview configuration. |

---

## 6. Target Environments

- Platforms: AWS for F1; vSphere for F2.
- Architectures: N/A — no architecture coverage claim is present in the scoped periodic definitions.
- Topology: AWS F1 config sets `COMPUTE_NODE_REPLICAS: "2"`; vSphere F2 config sets `COMPUTE_NODE_REPLICAS: "2"`. No broader topology claim is made.
- FIPS / disconnected: F1 is FIPS-enabled and uses the AWS proxy workflow; F2 is TechPreview (`FEATURE_SET: TechPreviewNoUpgrade`). Disconnected coverage is N/A.
- OCP versions in scope: `5.1`. The Jira acceptance criterion calls for CI against `5.1/main`; the generated jobs use MCO `extra_refs.base_ref: release-5.1`.

---

## 7. Approach

### 7.1 Test Levels

The only execution level in scope is existing periodic long-duration e2e CI: F1 uses the existing three-shard AWS FIPS/proxy target and F2 uses the existing two-shard vSphere TechPreview target. No unit, integration, manual/exploratory, or newly-authored e2e case is added by this plan. After the dependency update and its downstream rebase prerequisite are integrated, collect the five resulting job URLs and artifacts and assess them against Section 8.

### 7.2 Security / ProdSec Review

N/A — no ProdSec review or security-specific acceptance criterion was identified. F1 retains the already-configured FIPS/proxy CI coverage; this is not a claim of a new security review or a passing security result.

### 7.3 Regression

Regression is F1 and F2: re-run the existing AWS FIPS/proxy and vSphere TechPreview long-duration periodic validations and confirm their configured shard sets and targets remain usable with the updated dependencies. No additional regression suite is introduced.

---

## 8. Item Pass/Fail Criteria

- Per case: F1 passes only when all three named AWS periodic jobs complete successfully against the updated components and their artifacts show the configured `e2e-aws-mco-fips-proxy-longduration` target; F2 passes only when both named vSphere periodic jobs complete successfully and their artifacts show the configured `e2e-vsphere-mco-tp-longduration` target. Otherwise the applicable item fails and a defect is filed.
- Feature acceptance: every High-priority F# item (F1 and F2) has the required passing existing CI evidence, with zero open Blocker/Critical defects attributable to the dependency update. The remaining Jira acceptance conditions also require all epic stories to be complete and the MCO Go-version upgrade to be complete; this plan does not claim either condition has been met.
- **GA graduation (if this ticket or a sibling graduates a feature gate to default):** N/A — no ticket or sibling story establishing such graduation was found. No 95% Component Readiness rate is a hard gate for MCO-2538, and no rate is asserted.

---

## 9. Suspension/Resumption Criteria

Suspend F1 or F2 if the downstream OpenShift/Kubernetes rebase prerequisite is not merged into the candidate under test, the periodic definition no longer checks out MCO at `release-5.1`, a required AWS/vSphere CI environment is unavailable, or an unrelated CI incident prevents a conclusive result. Resume only after the prerequisite/environment issue is resolved and the same existing target and shard set can be re-run; retain the original failing or blocked job URL as evidence.

---

## 10. Test Deliverables

1. This test plan.
2. N/A — no automated test code is added; the existing periodic definitions and target evidence are identified in Section 3.
3. Execution logs / evidence: URLs and artifacts for all five named periodic jobs, recorded only after the required runs occur.
4. N/A — Section 3 has no `Proposed` or `Blocked` row, so this plan does not call for a quality story. Any defect discovered by a required run is handled outside this document.

---

## 11. Testing Tasks

1. Confirm all MCO-2538 implementation stories and the downstream OpenShift/Kubernetes rebase prerequisite are integrated into the candidate being assessed.
2. Execute or collect executions of F1’s three existing AWS periodic jobs and F2’s two existing vSphere periodic jobs; do not add a job, test case, or e2e scenario.
3. Record job URLs and artifacts, then assess F1 and F2 using Section 8 without treating an absent result as a pass.
4. Record any dependency-update defect through the normal defect process; no Jira issue is created by this plan.

---

## 12. Risks and Contingencies

| ID | Risk | Impact | Mitigation |
|---|---|---|---|
| R-1 | The downstream OpenShift/Kubernetes rebase PR merge named by MCO-2538 is not available in the candidate. | The required CI evidence would not validate the intended dependency update. | Suspend the affected run and resume only after the rebase is merged into the candidate. |
| R-2 | The Jira wording “5.1/main” and the generated job checkout `release-5.1` are interpreted as different validation inputs. | Results could be associated with the wrong MCO revision. | Record the exact candidate revision and retain evidence that each scoped job uses `extra_refs.base_ref: release-5.1`. |
| R-3 | AWS FIPS/proxy or vSphere TechPreview periodic infrastructure is unavailable or produces an unrelated failure. | F1 or F2 cannot produce conclusive acceptance evidence. | Preserve the job URL, classify the infrastructure issue separately, and rerun the same existing target and shard after recovery. |

---

## 13. Approvals

Publication requires requester confirmation of this document’s content first; that authorization was provided for this documentation-only version. QE lead and MCO feature owner review the required CI evidence before declaring the epic’s CI acceptance criterion satisfied. Release engineering owns the periodic-job configuration and execution environment.

---

## 14. External Dependencies / Unresolved Items

1. The downstream OpenShift/Kubernetes repository rebase PR merge identified by MCO-2538 must be available in the candidate before F1 and F2 can provide conclusive acceptance evidence.

---

*Generated to accompany OCPSTRAT-3618/MCO-2538. File paths/line numbers and the commit pin reference the checked-out `release-5.1` branch snapshot as of 2026-09-16 (see Snapshot pin above); re-verify after the referenced dependency update merges.*

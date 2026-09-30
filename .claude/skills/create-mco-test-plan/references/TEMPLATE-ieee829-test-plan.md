# Test Plan — [FEATURE NAME]

**Jira Feature (OCPSTRAT):** [OCPSTRAT-KEY] — [one-line title]

**Jira Epic/Story (implementation ticket):** [JIRA-KEY] — [one-line title]

**Related items:** [predecessor epics, sibling epics, design docs, ProdSec review]

**Feature status at time of writing:** [e.g. In Progress, targeted 5.1]

**Snapshot pin:** This plan reflects `openshift/machine-config-operator` at commit/PR [SHA or PR#] as of [YYYY-MM-DD]. Re-verify scope if the referenced code has since changed (see Risks).

---

## 1. Test Plan Identifier

| Field | Value |
|---|---|
| **Identifier** | TP-[OCPSTRAT-KEY]-[short-slug] |

`[short-slug]`: 2–5 lowercase hyphenated words, max 40 characters.
| **Revision** | N |
| **Date** | [YYYY-MM-DD] |

---

## 2. Introduction

### 2.1 Purpose

[1 very short paragraph: business/user driver, quoting MUST/SHOULD acceptance criteria from Jira verbatim where they exist.]

### 2.2 Scope

Per the Jira acceptance criteria, the feature is expected to deliver:

- [requirement 1 (MUST/SHOULD, quoted from Jira)]
- [requirement 2 (MUST/SHOULD, quoted from Jira)]

### 2.3 Background — current behavior relevant to testing

Grounded in the codebase at `main` (cite file:line):

- [Existing relevant code paths]
- [New work or update? State explicitly, with GitHub PR links/numbers found]
- [Existing tests/coverage this builds on]

### 2.4 References

Each reference gets its own row — never combine multiple links in a single row. Add R# rows as needed.

| Ref | Document |
|---|---|
| R1 | [OCPSTRAT feature link] |
| R2 | [implementation epic/story link] |
| R3 | [one related epic, PR, design doc, or ProdSec review per row] |
| R4 | [relevant source files] |
| R5 | [relevant existing test files] |

---

## 3. Test Items

[List the specific software components, modules, or objects under test. Include version/build identifiers where applicable.]

- [Component 1 — e.g. new CLI command/API field and its flags]
- [Component 2 — e.g. controller or daemon logic being added/modified]
- [Component 3 — e.g. config schema additions, parsing, validation]
- [Component 4 — e.g. documentation deliverable]

---

## 4. Features to Be Tested

One row per scenario. Every scenario gets a permanent ID (F1, F2, ...) referenced everywhere else in this document — never re-describe a scenario, reference its ID.

| ID | Requirement / Scenario | Priority | Status | Test location / evidence |
|---|---|---|---|---|
| F1 | [requirement] | High/Medium/Low | Implemented / Proposed / Blocked | [file path, Polarion ID, or "none yet"] |

Include interaction / non-interference checks as regular F# rows here — anything this change could break in adjacent, already-shipped functionality (e.g. user-supplied config, existing upgrade paths, other feature gates). The associated risks belong in Section 15 (Risks and Contingencies).

---

## 5. Features Not to Be Tested

Every exclusion must have a rationale — prefer quoting the Jira ticket's own words over paraphrasing.

| Excluded | Rationale |
|---|---|
| [item] | [Jira-quoted reason it's out of scope/deferred, or "vendored/upstream — not MCO's to test"] |

---

## 6. Approach

### 6.1 Strategy

[Prose, not just a table: explain the ownership boundary. E.g. — MCO owns controller/API/daemon behavior tested here; cross-repo dependencies (openshift/api, CI/release signal, openshift-tests-private, docs) are named explicitly with who owns them. State why anything is deliberately *not* re-tested here (e.g. "vendored/upstream logic, MCO only orchestrates it").]

### 6.2 Test Levels

[Unit/integration, extended/e2e (Ginkgo), manual/exploratory — map each to the F# IDs it covers.]

### 6.3 Security / ProdSec Review

[If applicable: what ProdSec reviewed, what this plan verifies against those recommendations (key handling, permissions, no unintended data exposure). Use "N/A" if not applicable or there is nothing important to mention.]

### 6.4 Regression

[Existing suites to re-run to confirm no regression, tied to the interaction/non-interference F# rows in Section 4.]

---

## 7. Item Pass/Fail Criteria

- Per case: passes only when actual matches expected; else fails and a defect is filed.
- Unit tests: all unit tests must pass; both new unit tests and old unit tests. No regression should happen at unit test level.
- Feature acceptance: every High-priority F# item has ≥1 passing case with zero Blocker/Critical defects.
- **GA graduation (if this ticket or a sibling graduates a feature gate to default):** The criteria defined in https://github.com/openshift/api#defining-featuregate-e2e-tests must be met before the gate can flip.

---

## 8. Suspension/Resumption Criteria

N/A

---

## 9. Test Deliverables

1. This test plan.
2. Test case specifications
3. Automated test code (paths from Section 4/6).
4. Execution logs / evidence (job URLs, Polarion results).
5. Quality stories filed in Jira, one per `Proposed`/`Blocked` row in Section 4 — filed as real tickets, not drafted inside this document.

---

## 10. Testing Tasks

[Or: "Populate in Jira; track via feature child epic/story/task hierarchy."]

---

## 11. Environmental Needs

- Platforms: [confirmed with requester — e.g. AWS/GCP primary, others via CI job matrix]
- Architectures: [x86_64/arm64/s390x/ppc64le]
- Topology: [SNO/compact/multi-node/HyperShift-HCP]
- FIPS / disconnected: [yes/no/N/A]
- OCP versions in scope: [...]
- Build / image source: [e.g. feature-branch image, custom payload, standard pipeline]
- Tooling: [e.g. oc/kubectl, openssl, must-gather, specialized CLI]
- Test data / fixtures: [e.g. sample workloads, custom certificates, pre-configured resources]

---

## 12. Responsibilities

Tracked in the Jira feature: [link to the Jira feature]

---

## 13. Staffing and Training Needs

[Required skills and knowledge to properly test the feature — e.g. familiarity with specific subsystems, tools, or standards.]

---

## 14. Schedule

Tracked in the Jira feature: [link to the Jira feature]

---

## 15. Risks and Contingencies

Each risk should tie to a specific, named uncertainty in the feature spec — not generic risk language.

| ID | Risk | Impact | Mitigation |
|---|---|---|---|
| R-1 | [specific uncertainty, e.g. "requirement is only SHOULD, not MUST"] | [concrete consequence] | [concrete mitigation] |

Include external dependencies and unresolved items here — real blockers waiting on another team's decision, an upstream fix, or an undefined CI signal contract. Not for questions to the requester; those must be resolved in chat before this file is written.

---

## 16. Approvals

Tracked in the Jira feature: [link to the Jira feature]

---

*Generated to accompany [OCPSTRAT-KEY]/[JIRA-KEY]. File paths, line numbers, and commit references correspond to the revision named in the Snapshot pin above; re-verify after the referenced PRs merge.*
<!-- markdownlint-disable MD013 -->
# Test Plan: Adapt Boot Image Controller to Handle Cluster API Resources on AWS (Tech Preview)

**Epic:** [MCO-2251](https://redhat.atlassian.net/browse/MCO-2251)
**Target Release:** OpenShift 5.1 (Tech Preview), 5.2 (GA)
**QA Contact:** Urvashi Mohnani
**Last Updated:** 2026-11-10

## 1. Introduction

The MCO boot image controller currently reconciles boot images (AMIs, VM templates) for Machine API (MAPI) MachineSets and ControlPlaneMachineSets. OpenShift is migrating from MAPI to Cluster API (CAPI), and the controller must be extended to reconcile CAPI MachineSets and MachineDeployments on AWS.

CAPI uses an immutable infrastructure template model (e.g., `AWSMachineTemplate`) — unlike MAPI's direct merge-patch, updates require cloning the template with a new name and patching the `infrastructureRef` on the MachineSet/MachineDeployment. This is a fundamentally different reconciliation path.

This feature is gated behind the `ManagedBootImagesAWSCAPI` TechPreview feature gate for 5.1.

## 3. References

- Enhancement: [OCPSTRAT-764](https://redhat.atlassian.net/browse/OCPSTRAT-764) — ClusterAPIMachineManagement
- Design PR: [#6447](https://github.com/openshift/machine-config-operator/pull/6447) — MCO-2334 implementation
- Dev stories: MCO-2332 (feature gate), MCO-2333 (API update), MCO-2334 (reconciliation), MCO-2535 (API validation), MCO-2536 (status population), MCO-2619 (cert rotation)
- Bug fix: OCPBUGS-127707 (skew enforcement CAPI mode check)
- Source: `pkg/controller/bootimage/`, `pkg/operator/sync.go`

## 4. Features to be Tested

| ID | Feature | Dev Story | Status |
|----|---------|-----------|--------|
| F1 | `ManagedBootImagesAWSCAPI` feature gate | MCO-2332 | Closed |
| F2 | `ManagedBootImages` API accepts CAPI `cluster.x-k8s.io` APIGroup and `machinedeployments` resource type | MCO-2333 | Closed |
| F3 | Boot image reconciliation for CAPI MachineSets on AWS (template clone + infrastructureRef patch) | MCO-2334 | Closed |
| F4 | CEL validation rules for CAPI + skew enforcement (Automatic mode requires All for CAPI) | MCO-2535 | Closed |
| F5 | Operator populates `managedBootImageStatus` for CAPI resources | MCO-2536 | Closed |
| F6 | Skew enforcement aggregates across MAPI + CAPI resources | OCPBUGS-127707 | Verified |

## 5. Features Not to be Tested

| Feature | Reason |
|---------|--------|
| CAPI boot image on non-AWS platforms (GCP, Azure, vSphere) | Not in scope for 5.1 TP; only AWS is targeted |
| CAPI cert rotation (user-data Secret) | MCO-2619 — To Do, not yet implemented |
| ClusterClass / topology mode | Not supported yet per PR #6447 design notes |
| CAPI MachineDeployment direct reconciliation | ~~MachineDeployments own MachineSets; boot image managed via the child MachineSet~~ — Moved to tested: `syncCAPIMachineDeployment` clones templates and patches the MachineDeployment's `infrastructureRef` directly. See TC-26. |
| Old template cleanup / garbage collection | Follow-up work; acknowledged in PR #6447 as future scope |

## 6. Test Environment

- **Platform:** AWS
- **Cluster type:** TechPreview-enabled cluster (`featureSet: TechPreviewNoUpgrade`)
- **Prerequisite feature gates:** `ClusterAPIMachineManagement` (CAPI CRDs exist), `ManagedBootImagesAWSCAPI`
- **Cluster install:** AWS cluster with CAPI resources (CAPI MachineSet installation is TechPreview in 5.0/5.1)
- **Tools:** `oc`, `aws` CLI, cluster-bot
- **Controller location:** The boot image controller runs in the `machine-config-controller` pod (deployment `machine-config-controller` in `openshift-machine-config-operator` namespace), **not** `machine-config-operator`. All controller log inspection should target this pod.
- **OS stream prerequisite:** OCP 5.1 defaults to `rhel-10`, but `SupportedOSStream` is hardcoded to `rhel-9`. CAPI and MAPI MachineSets must be labeled with `machineconfiguration.openshift.io/osstream: rhel-9` before the boot image controller will process them. Without this label, MachineSets are skipped with "unsupported stream: rhel-10".
- **VAP constraint on CAPI labels:** When `authoritativeAPI=MachineAPI`, the ValidatingAdmissionPolicy blocks direct label changes on CAPI MachineSets. Relabel MAPI MachineSets first and let the migration controller sync the change to the CAPI counterpart.

## 7. Test Scenarios

### 7.1 Feature Gate Behavior

| TC-ID | Scenario | Steps | Expected Result | Coverage |
|-------|----------|-------|-----------------|----------|
| TC-01 | Feature gate disabled — no CAPI overhead | Deploy non-TP AWS cluster. Verify boot image controller behavior. | No CAPI informers created. No CAPI-related conditions. MAPI reconciliation works as before. | New |
| TC-02 | Feature gate enabled — CAPI informers start | Deploy TP AWS cluster with `ManagedBootImagesAWSCAPI` enabled. | CAPI MachineSets and MachineDeployments are watched. Controller logs show CAPI sync activity. | New |

### 7.2 ManagedBootImages API — CAPI Selection

| TC-ID | Scenario | Steps | Expected Result | Coverage |
|-------|----------|-------|-----------------|----------|
| TC-03 | Opt-in CAPI MachineSets with `All` mode | Set `ManagedBootImages` spec with `apiGroup: cluster.x-k8s.io`, `resource: machinesets`, selection `All`. | All CAPI MachineSets are enrolled. `managedBootImageStatus` reflects CAPI managers. | New |
| TC-04 | Opt-in CAPI MachineSets with `Partial` mode | 1. Set skew enforcement spec to `None` so status moves out of `Automatic`. 2. Set `ManagedBootImages` with `Partial` selection using `partial.machineResourceSelector` label selector matching target CAPI MachineSets. | Only CAPI MachineSets matching the label selector are enrolled. | New |
| TC-05 | Set CAPI MachineSets to `None` | 1. Set skew enforcement spec to `None` so status moves out of `Automatic`. 2. Set CAPI manager to `None`. | No CAPI MachineSets reconciled. Status reflects `None` for MachineSets. (MachineDeployment manager is independent and may remain enrolled.) | New |
| TC-06 | MachineDeployments `All` mode | Set `ManagedBootImages` with `apiGroup: cluster.x-k8s.io`, `resource: machinedeployments`, selection `All`. | MachineDeployments are enrolled. MachineSets owned by MachineDeployments are skipped (reconciled via parent). | New |
| TC-07 | Default opt-in with feature gate | Enable feature gate on fresh TP cluster. | Operator auto-populates CAPI managers in `managedBootImageStatus` matching the auto-opt-in logic used for MAPI. | New |

### 7.3 CAPI Boot Image Reconciliation (Core — F3)

| TC-ID | Scenario | Steps | Expected Result | Coverage |
|-------|----------|-------|-----------------|----------|
| TC-08 | CAPI MachineSet boot image update on AWS | 1. Deploy TP AWS cluster with CAPI MachineSets (labeled `rhel-9`). 2. Observe current `AWSMachineTemplate` AMI. 3. Create a copy of the template with a stale (older) AMI and patch the MachineSet's `infrastructureRef` to point to it. | Controller detects AMI mismatch, clones a new `AWSMachineTemplate` with the golden AMI (deterministic FNV-1a hash name: `<ms-name>-<8-hex-hash>`), and patches MachineSet's `infrastructureRef` to the new template. Log: `capi_helpers.go — "Successfully patched CAPI MachineSet ... to use infrastructure template ..."`. | New |
| TC-09 | Boot image already current — no-op | CAPI MachineSet already has the latest AMI. | Controller detects no diff. No new template created. No patch applied. | New |
| TC-10 | Multiple CAPI MachineSets | Multiple CAPI MachineSets in different AZs. | All enrolled MachineSets are reconciled. Each gets its own cloned template with the correct region-specific AMI. Other AZ-specific settings (subnet, etc.) remain intact. | New |
| TC-11 | MachineDeployment-owned MachineSet skipped | MachineSet is owned by a MachineDeployment. | Controller skips the MachineSet — it is not patched directly. `syncCAPIMachineSet` returns `patchSkipped=false` (not counted as a skip for skew enforcement). Boot image is managed via the MachineDeployment parent. | New |
| TC-26 | CAPI MachineDeployment boot image update on AWS | 1. Deploy TP AWS cluster with CAPI MachineDeployments enrolled (labeled `rhel-9`). 2. Create a copy of the `AWSMachineTemplate` with a stale AMI and patch the MachineDeployment's `infrastructureRef` to point to it. | Controller detects AMI mismatch on the MachineDeployment, clones a new `AWSMachineTemplate` with the golden AMI, and patches the MachineDeployment's `infrastructureRef`. Child MachineSet is NOT patched directly. Log: `capi_helpers.go — "Successfully patched CAPI MachineDeployment ... to use infrastructure template ..."`. | New |

### 7.4 Skew Enforcement with CAPI

| TC-ID | Scenario | Steps | Expected Result | Coverage |
|-------|----------|-------|-----------------|----------|
| TC-12 | Automatic skew mode with CAPI All | Set all MAPI and CAPI managers to `All`. Omit or remove `bootImageSkewEnforcement` spec override. | Operator computes `Automatic` in `status.bootImageSkewEnforcementStatus.mode`. Skew enforcement operates across all resource types. (`Automatic` is a computed status value, not a spec value — spec only accepts `Manual` and `None`.) | New |
| TC-13 | Automatic mode rejected without CAPI All | With skew status in `Automatic` (all managers `All`), attempt to change CAPI MachineSets to `Partial` or `None`. | CEL validation rejects the configuration because `status.bootImageSkewEnforcementStatus.mode` is `Automatic` and CAPI managers are not `All`. | New |
| TC-14 | Skew aggregation across MAPI + CAPI | Mixed MAPI and CAPI resources. Some have skew, some don't. | `updateClusterBootImage()` correctly aggregates skip/error counts from both MAPI and CAPI. `Upgradeable` condition reflects combined state. | New |
| TC-15 | CAPI None opinion — Manual skew mode | 1. Set skew enforcement spec to `None` to allow changing CAPI managers. 2. Set CAPI managers to `None`. 3. Remove the skew enforcement spec override. | Skew enforcement status computes `Manual` mode (not `Automatic`) because CAPI managers are not `All`. | Existing (OCPBUGS-127707) |

### 7.5 VAP Validation

| TC-ID | Scenario | Steps | Expected Result | Coverage |
|-------|----------|-------|-----------------|----------|
| TC-16 | CAPI enrollment restricted to AWS | On a GCP or Azure cluster, attempt to set CAPI `cluster.x-k8s.io` managers. | ValidatingAdmissionPolicy rejects the configuration (CAPI only supported on AWS for TP). | New |

### 7.6 Status and Conditions

| TC-ID | Scenario | Steps | Expected Result | Coverage |
|-------|----------|-------|-----------------|----------|
| TC-17 | MachineConfiguration status reflects CAPI counts | Enable CAPI managers. Verify `MachineConfiguration/cluster` status. | Status conditions include CAPI MachineSet and MachineDeployment counts alongside MAPI counts. | Existing (partial, `mco_capi_bootimages.go`) |
| TC-18 | Progressing/Degraded conditions for CAPI errors | Introduce an error in CAPI reconciliation (e.g., invalid template). | `Degraded` condition is set. Error message references the CAPI resource. | New |

### 7.7 MAPI to CAPI Migration Path

| TC-ID | Scenario | Steps | Expected Result | Coverage |
|-------|----------|-------|-----------------|----------|
| TC-19 | Migration: MAPI MachineSet transitions to CAPI | 1. Non-TP AWS cluster with MAPI MachineSets (labeled `rhel-9`). 2. Enable TP (`featureSet: TechPreviewNoUpgrade`). 3. Wait for CAPI infrastructure (pods, CRDs, CAPI MachineSets created by migration controller). 4. Verify CAPI path defers while `authoritativeAPI=MachineAPI` (log: `capi_helpers.go — "deferring to MAPI path"`). 5. Set `spec.authoritativeAPI: ClusterAPI` on a MAPI MachineSet. 6. Verify MAPI side skips (log: `ms_helpers.go — "removing from MAPI boot image tracking"`). 7. Verify CAPI side stops deferring and reconciles. 8. Trigger a stale AMI scenario and verify template clone + infraRef patch. | MCO skips the MAPI resource. Reconciles the CAPI copy instead. No double-patching. Transient `Migrating` state handled correctly. | New |
| TC-20 | Mixed MAPI + CAPI during migration | One MachineSet transitioned to `authoritativeAPI: ClusterAPI`, another remains at `authoritativeAPI: MachineAPI`. | MAPI MachineSet: MAPI path reconciles, CAPI path defers. CAPI MachineSet: MAPI path skips ("removing from MAPI boot image tracking"), CAPI path reconciles. Both reflected in status. No double-patching. | New |

### 7.8 Edge Cases and Negative Tests

| TC-ID | Scenario | Steps | Expected Result | Coverage |
|-------|----------|-------|-----------------|----------|
| TC-21 | CAPI CRDs unavailable — blocking risk | Feature gate enabled but CAPI CRDs not installed. | **Known issue:** `initCAPISetup` Phase A (`WaitForCacheSync` for MachineSet/MachineDeployment at `boot_image_controller.go:321`) uses an unbounded context. If these CRDs are missing, Phase A blocks indefinitely and the worker goroutine (line 299) never starts — ALL reconciliation (MAPI, CPMS, and CAPI) is blocked. Phase B (infrastructure template) has a 30s timeout and degrades correctly. **Test:** verify Phase A blocks when MachineSet/MachineDeployment CRDs are absent; file a bug to add a bounded timeout to Phase A matching Phase B's pattern. The code comment at line 289 ("MAPI and CPMS workers always start") is incorrect for this case. | New |
| TC-22 | AWS marketplace AMI with CAPI | CAPI MachineSet uses a marketplace AMI. | Controller correctly resolves and tracks marketplace AMI version for CAPI resources. | New |
| TC-23 | Hot-loop detection for CAPI | 1. Enroll a CAPI MachineSet. 2. Patch its `infrastructureRef` to point to a stale template. 3. Wait for the controller to reconcile (clones template, patches infraRef back). 4. Repeat step 2 to restore the stale reference — this forces the same patch again. 5. Repeat until `HotLoopLimit` (3) is exceeded. | Controller detects the same template spec being patched repeatedly via `checkCAPIMachineSetHotLoop` (`capi_helpers.go:408`). After exceeding the limit, reconciliation is refused with error "hot loop detected". Without restoring the stale reference each time, subsequent reconciliations are no-ops and hot-loop never triggers. | New |
| TC-24 | Template name collision / reuse | AWSMachineTemplate with the generated deterministic hash name already exists (e.g., from a previous reconciliation). | Controller detects `AlreadyExists`, reuses the existing template, and patches the MachineSet's `infrastructureRef`. Log: `capi_helpers.go — "Infrastructure template ... already exists, reusing for CAPI MachineSet ..."`. | New |
| TC-25 | Unsupported OS stream skip | CAPI MachineSet labeled with `machineconfiguration.openshift.io/osstream: rhel-10` (or any non-`rhel-9` value). | Controller skips the MachineSet without error. Log: `"unsupported stream: rhel-10, skipping boot image update"`. Not counted as a skew violation. | New |

## 8. Existing / Reference Coverage

The following tests already exist and serve as baseline coverage:

| Test File | Coverage | Notes |
|-----------|----------|-------|
| `test/extended-priv/mco_bootimages.go` | 12 test cases for MAPI boot image reconciliation (opt-in, restore, errors, ignition stub, marketplace) | Baseline — MAPI only |
| `test/extended-priv/mco_bootimages_skew.go` | Skew enforcement: Manual/Automatic/None modes, Upgradeable condition | Baseline — MAPI only |
| `test/extended-priv/mco_bootimages_aws_marketplace.go` | AWS marketplace-specific boot image tests | Baseline — MAPI only |
| `test/extended-priv/mco_capi_bootimages.go` | 1 test: MachineConfiguration status reflects CAPI spec changes (All/None/Partial) | Existing CAPI — status only, no reconciliation |
| `pkg/controller/bootimage/boot_image_controller_test.go` | Unit tests: stability, hot-loop, provider spec reconciliation | Baseline — MAPI only |

## 9. Test Deliverables

| Deliverable | Target Jira | Status |
|-------------|-------------|--------|
| This test plan | MCO-2251 (epic link) | Done |
| Pre-merge test results | MCO-2322 | Done |
| E2E automated tests (CAPI-specific) | MCO-2323 | Planned |
| CI job coverage | MCO-2324 | Planned |

## 10. Risks and Dependencies

| Risk | Impact | Mitigation |
|------|--------|------------|
| MCO-2619 (cert rotation) not implemented | CAPI user-data Secret rotation untested | Out of scope for this plan; separate task needed |
| Old AWSMachineTemplate cleanup not implemented | Orphaned templates may accumulate during testing | Acknowledged as follow-up; manually clean up during test runs |
| ClusterClass/topology mode may revert MCO patches | Not supported — document as known limitation | Exclude from test scope |
| CAPI installation itself is TechPreview | Test environment setup may be complex or unstable | Use cluster-bot with known-good TP configurations |
| `SupportedOSStream` hardcoded to `rhel-9` while OCP 5.1 defaults to `rhel-10` | MachineSets are silently skipped without relabeling, making it appear the controller is not reconciling | Relabel MachineSets to `rhel-9` as a prerequisite; documented in Test Environment section |

## 11. Pass/Fail Criteria

- All TC-01 through TC-26 execute successfully in their applicable environments (AWS TP cluster for most; non-TP AWS for TC-01; GCP or Azure for TC-16).
- No regressions in existing MAPI boot image tests.
- Skew enforcement correctly aggregates across MAPI and CAPI resources.
- Feature gate disabled path shows zero CAPI overhead.

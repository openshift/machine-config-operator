# Design: Boot Image Updates for CAPI MachineSets and MachineDeployments

## Background

The boot image controller (`pkg/controller/bootimage`) reconciles boot images for:
- MAPI `MachineSets` (`machine.openshift.io/v1beta1`)
- MAPI `ControlPlaneMachineSets` (`machine.openshift.io/v1`)
- CAPI `MachineSets` (`cluster.x-k8s.io/v1beta2`) — implemented on this branch

The controller struct carries `capiMachineSetStats` and `capiMachineDeploymentStats` stat fields, and the progressing/degraded conditions include CAPI message slots. The `syncCAPIMachineSets` and `syncCAPIMachineDeployments` callsites in `syncAll` are wired.

The `MachineAPIMigration` feature gate (Tech Preview) enables migration of MAPI MachineSets to CAPI authority. Once migrated, a MAPI MachineSet carries `status.authoritativeAPI: ClusterAPI`, and writes should target the CAPI copy instead. The `ClusterAPIMachineManagement` feature gate guarantees the CAPI CRDs exist (required for informer setup). The `ManagedBootImagesAWSCAPI` feature gate controls the per-platform AWS reconcile path.

---

## Scope

1. Detect MAPI MachineSets that have been migrated to CAPI authority and skip patching them via the MAPI client.
2. Reconcile CAPI `MachineSets` (`cluster.x-k8s.io/v1beta2`) enrolled through the `MachineConfiguration` API.
3. Reconcile CAPI `MachineDeployments` (`cluster.x-k8s.io/v1beta2`) enrolled through the `MachineConfiguration` API.
4. Propose the API changes needed in `openshift/api` to allow enrollment of CAPI resources.

---

## API Changes Required (openshift/api)

Two additions landed in `vendor/github.com/openshift/api/operator/v1/types_machineconfiguration.go` as part of this branch:

**1. New API group constant for CAPI (`opv1.ClusterAPI`):**
```go
// ClusterAPI represents the Cluster API group for CAPI-managed resources.
ClusterAPI MachineManagerMachineSetsAPIGroupType = "cluster.x-k8s.io"
```
Gated behind the `ManagedBootImagesAWSCAPI` feature gate via `FeatureGateAwareEnum`.

**2. New resource type for MachineDeployments (`opv1.MachineDeployments`):**
```go
// MachineDeployments represent the MachineDeployment resource type in the CAPI group.
MachineDeployments MachineManagerMachineSetsResourceType = "machinedeployments"
```
Also gated behind the same feature gate.

These are used directly in `capi_helpers.go` — no local shim constants.

**3. Extend the `Automatic` skew enforcement restriction to CAPI resources:**

The existing API validation enforces that `BootImageSkewEnforcement` can only be set to `Automatic` mode when the MAPI MachineSet selection is `opv1.All`. This validation must be extended to cover CAPI MachineSets and MachineDeployments: `Automatic` mode should require `opv1.All` selection for all enrolled resource types. Until this lands, the controller-side `skippedCount == 0` gate is correct for MAPI but may be reached for CAPI resources without the `All` guarantee.

This lets a cluster admin enroll CAPI resources in `MachineConfiguration` using:
```yaml
machineManagers:
  - apiGroup: cluster.x-k8s.io
    resource: machinesets
    selection:
      mode: All
  - apiGroup: cluster.x-k8s.io
    resource: machinedeployments
    selection:
      mode: All
```

---

## CAPI Infrastructure Template Model

CAPI does not embed provider specs inside `MachineSets` or `MachineDeployments`. Instead each resource holds an `infrastructureRef` pointing to a provider-specific template (e.g., `AWSMachineTemplate`, `AzureMachineTemplate`):

```
MachineDeployment
  spec.template.spec.infrastructureRef → AWSMachineTemplate (contains AMI ID)

MachineSet
  spec.template.spec.infrastructureRef → AWSMachineTemplate (same pattern)
```

CAPI infrastructure templates are **immutable by convention**: providers forbid in-place updates to the boot image field. The standard update flow is:

1. Read the existing template to get the current boot image and full spec.
2. Compare against the target boot image from the `coreos-bootimages` ConfigMap.
3. If update needed: clone the template under a deterministic name, create the new template, then patch the `MachineDeployment`/`MachineSet`'s `infrastructureRef.name` to point at it.

Template names follow `<machineset-name>-<fnv32a-hash>` where the hash is computed from the new template spec (matching the convention used by `cluster-capi-operator`).

Infrastructure templates carry an ownerReference to the CAPI `Cluster` object for garbage collection. The `Cluster` already has its own finalizer (`cluster.cluster.x-k8s.io`) managing its deletion lifecycle, so `blockOwnerDeletion` on the copied ownerReference is redundant and is stripped before creating the new template (it would otherwise require MCO to have `update` on `clusters/finalizers`).

---

## Access Strategy: Dynamic Listers with Typed Conversion

CAPI MachineSets, MachineDeployments, and infrastructure templates are accessed via a `dynamicinformer.DynamicSharedInformerFactory` with `dynamiclister.NamespaceLister` backed by the informer cache. This avoids live API calls on every sync.

The provider API packages are vendored for typed struct access in the per-platform reconcile functions:

| Package | Actual import path | Provides |
|---|---|---|
| `sigs.k8s.io/cluster-api` | `api/core/v1beta2` | `MachineSet`, `MachineDeployment` |
| `sigs.k8s.io/cluster-api-provider-aws/v2` | `api/v1beta2` | `AWSMachineTemplate` |

Only the AWS provider package is vendored. Azure, GCP, and vSphere provider packages are not imported — their template GVRs and reconcile functions will be added when those platforms are implemented.

Unstructured objects from the lister are converted to typed structs via `runtime.DefaultUnstructuredConverter.FromUnstructured` at the start of each platform reconcile function, and converted back via `ToUnstructured` for the return value.

---

## Controller Changes

### New fields on `Controller`

```go
// dynamic factory and listers for CAPI resources (all in openshift-cluster-api namespace)
capiInformerFactory            dynamicinformer.DynamicSharedInformerFactory
capiMachineSetLister           dynamiclister.NamespaceLister
capiMachineSetListerSynced     cache.InformerSynced
capiMachineDeploymentLister    dynamiclister.NamespaceLister
capiMachineDeploymentListerSynced cache.InformerSynced

// Single lister for the platform-specific infrastructure template CRD, wired lazily in Run().
capiInfraTemplateLister        dynamiclister.NamespaceLister
capiInfraTemplateListerSynced  cache.InformerSynced
```

### `New()` changes

No CAPI-specific setup is done in `New()`. The factory and all CAPI informers are deferred to `Run()` so that non-AWS clusters carry zero CAPI overhead regardless of feature gate state.

### `Run()` two-phase cache sync

The cluster platform is only known once the infra lister cache is warm. `Run()` therefore does a two-phase sync:

**Phase 1**: Wait for core informers to sync (configmap, MAPI MachineSets, infra, MachineConfiguration, ClusterVersion). No CAPI informers are started here.

**Phase 2**: After the infra cache is warm, check whether `ClusterAPIMachineManagement` is enabled, the platform is AWS, and `ManagedBootImagesAWSCAPI` is enabled. If all three hold:
1. Call `initCAPIInformers()` — creates the `DynamicSharedInformerFactory` for `openshift-cluster-api`, registers MachineSet and MachineDeployment informers with event handlers.
2. Start the factory and wait for MachineSet/MachineDeployment caches to sync.
3. Call `wireCAPITemplateInformer()` — registers the `awsmachinetemplates` informer into the factory.
4. Start the factory again (picking up the newly registered informer) and wait for it to sync.

Non-AWS clusters skip phase 2 entirely — no CAPI factory, no informers, no watches.

### `syncMAPIMachineSets` change — skip migrated sets

In `syncMAPIMachineSet`, gated on `FeatureGateMachineAPIMigration`, before any provider logic:

```go
if ctrl.fgHandler.Enabled(features.FeatureGateMachineAPIMigration) {
    switch machineSet.Status.AuthoritativeAPI {
    case machinev1beta1.MachineAuthorityClusterAPI, machinev1beta1.MachineAuthorityMigrating:
        // delete from mapiBootImageState, return patchSkipped=false
    }
}
```

This signals "not applicable to the MAPI path" rather than "skipped" — the MachineSet does not inflate `skippedCount` and does not block skew enforcement on the MAPI side.

### New `syncCAPIMachineSets` and `syncCAPIMachineDeployments`

These follow the same structure as `syncMAPIMachineSets`:

1. Read `MachineConfiguration` to find enrolled CAPI resources via `getMachineResourceSelectorFromMachineManagers` (extended for `ClusterAPI` API group).
2. List matching resources via the dynamic lister.
3. For each resource, call `syncCAPIMachineSet` / `syncCAPIMachineDeployment`.
4. Update conditions via `ctrl.capiMachineSetStats` / `ctrl.capiMachineDeploymentStats`.

### `syncCAPIMachineSet`

Per-resource sync logic:

1. **MachineDeployment owner skip**: If the MachineSet is owned by a MachineDeployment, skip (`patchSkipped=false`) — the boot image is managed via the parent.
2. **MAPI authoritativeAPI check** (gated on `FeatureGateMachineAPIMigration`): When migration is active, look up the MAPI MachineSet with the same name. If it exists and `authoritativeAPI != ClusterAPI`, defer to the MAPI path (`patchSkipped=false`). Only proceed if MAPI is fully handed off or absent. On pure-CAPI clusters where `MachineAPIMigration` is not enabled, this check is skipped entirely — there are no MAPI counterparts.
3. **Stream label check**: Skip non-default OS streams.
4. **Windows check**: Skip MachineSets with the Windows OS label.
5. **Arch detection**: `getArchFromCAPIMachineSet` — reads arch annotation; defaults to control plane arch on single-arch clusters; errors on multi-arch clusters with no annotation.
6. **Release version guard**: Fetch the `coreos-bootimages` ConfigMap; skip if the OCP release version stored in it doesn't match the running MCO version (cluster upgrade in progress).
7. **Fetch infra template**: `ctrl.getCAPIInfraTemplate(name)` — looks up the template by name from `capiInfraTemplateLister`.
8. **Platform dispatch**: `checkCAPIMachineSet` dispatches to the per-platform reconcile function in `capi_platform_helpers.go`. Each platform case is gated on its own feature gate (e.g., `ManagedBootImagesAWSCAPI` for AWS). Platforms without an enabled gate no-op silently.
9. **Hot loop detection**: `checkCAPIMachineSetHotLoop` using `capiBootImageState`.
10. **Create + patch**: `patchCAPIMachineSet` — creates the new template (with `blockOwnerDeletion` stripped from ownerReferences), then patches the MachineSet's `infrastructureRef.name`.

---

## Skew Enforcement in Mixed Environments

Skew enforcement gates `updateClusterBootImage()` on the condition that no enrolled machine resource has a pending boot image update that was blocked. In a mixed MAPI/CAPI world, this check must span all three resource types: MAPI MachineSets, CAPI MachineSets, and CAPI MachineDeployments.

### Gate condition

`updateClusterBootImage()` is called from `syncAll`, after all three sync functions have completed:

```go
noSkips := ctrl.mapiStats.skippedCount == 0 && ctrl.mapiStats.erroredCount == 0
noErrors := ctrl.mapiStats.erroredCount == 0
if ctrl.fgHandler.Enabled(features.FeatureGateClusterAPIMachineManagement) {
    // Only check CAPI stats when CAPI is active; avoids acting on stale
    // counts from a prior cycle where the gate was enabled but is now off.
    noSkips = noSkips &&
        ctrl.capiMachineSetStats.skippedCount == 0 &&
        ctrl.capiMachineSetStats.erroredCount == 0 &&
        ctrl.capiMachineDeploymentStats.skippedCount == 0 &&
        ctrl.capiMachineDeploymentStats.erroredCount == 0
    noErrors = noErrors &&
        ctrl.capiMachineSetStats.erroredCount == 0 &&
        ctrl.capiMachineDeploymentStats.erroredCount == 0
}
switch {
case noSkips:
    ctrl.updateClusterBootImage(rhcosVersion)
case noErrors:
    ctrl.resetClusterBootImage()
}
```

### What counts as a skip

`patchSkipped=true` is returned only when a resource was in scope for boot image management but could not be updated due to a condition requiring manual intervention (e.g., missing architecture annotation on a multi-arch cluster). These are the cases where a skew alert is warranted.

`patchSkipped=false` is returned — and therefore **not counted as a skip** — for:

1. **CAPI MachineSets owned by a MachineDeployment**: The `syncCAPIMachineSet` owner-reference check fires first and returns `false, nil`. The boot image is managed correctly through the parent MachineDeployment; this is not a skew condition.
2. **Non-authoritative resources**:
   - MAPI MachineSets with `authoritativeAPI: ClusterAPI` or `Migrating` return `false, nil` and are removed from `mapiBootImageState`. The CAPI sync path owns these resources now.
   - CAPI MachineSets where the MAPI counterpart is still authoritative (`authoritativeAPI != ClusterAPI`) return `false, nil`. The MAPI path still owns these; deferring prevents a dual-write conflict.
3. **Intentionally excluded resources**: Windows MachineSets, non-default OS stream MachineSets — these return `false, nil` because the MCO deliberately excludes them, not because of a transient block.

### Enrollment selection mode

The API currently enforces that skew enforcement can only be set to `Automatic` mode when the MAPI MachineSet selection is `opv1.All`. This invariant does not yet extend to CAPI MachineSets or MachineDeployments — that is a required API change tracked above. Once extended, `updateClusterBootImage()` (which already bails early if `BootImageSkewEnforcementStatus.Mode != Automatic`) will carry the full guarantee across all three resource types, and the controller will not need to re-check selection modes — `skippedCount == 0` is the correct and sufficient condition. The enforcement check for the additional resources will be guarded by `ClusterAPIManagement` feature gate. 

### Placement: `syncAll`, not `syncMAPIMachineSets`

The gate was previously embedded inside `syncMAPIMachineSets`, which ran before the CAPI syncs. Moving it to `syncAll`, after all three syncs complete, is what makes the multi-resource check possible. The old placement also meant that a zero MAPI skipped count alone could trigger `updateClusterBootImage()` even when CAPI resources had pending skips.

### Transitional state

During the `Migrating` transitional state, a MachineSet is removed from `mapiBootImageState` (MAPI returns `false`) and is not yet tracked by `capiBootImageState` (CAPI defers until `authoritativeAPI == ClusterAPI`). Stale entries from before the transition are cleared naturally without explicit migration-aware cleanup logic.

---

## Platform Support Mapping

| Platform | MAPI ProviderSpec field | CAPI infra template kind | Boot image field path | Status |
|---|---|---|---|---|
| AWS | `spec.ami.id` | `AWSMachineTemplate` | `spec.template.spec.ami.id` | Active (gated on `ManagedBootImagesAWSCAPI`) |
| Azure | `spec.image` | `AzureMachineTemplate` | `spec.template.spec.image` | not yet implemented |
| GCP | `spec.disks[].image` | `GCPMachineTemplate` | `spec.template.spec.image` | not yet implemented |
| vSphere | `spec.template` | `VSphereMachineTemplate` | `spec.template.spec.template` | not yet implemented |

Azure skips images with `SecurityType` set (Confidential/Trusted Launch VMs) and skips non-Marketplace images (ComputeGallery, custom IDs). GCP guards against patching custom images using the `projects/rhcos-cloud/global/images` prefix check.

---

## MachineDeployment vs MachineSet Enrollment

**Key distinction**: CAPI `MachineDeployments` own `MachineSets`, which in turn own individual `Machines`. The `infrastructureRef` the MCO needs to patch lives on the `MachineDeployment` (for the template used in future rollouts), not on the owned `MachineSets` (those are already-created copies). Patching a MachineDeployment-owned MachineSet directly would be immediately overwritten by the MachineDeployment controller.

**Owner references are guaranteed**: The CAPI MachineDeployment controller always sets an owner reference on every MachineSet it creates. The upstream source comment is explicit: *"By setting the ownerRef on creation we signal to the MachineSet controller that this is not a stand-alone MachineSet."* The MCO uses this owner reference to detect and skip MachineDeployment-owned MachineSets in `syncCAPIMachineSet`.

**Skew semantics**: A MachineSet skipped because it is owned by a MachineDeployment returns `patchSkipped=false`. It is not counted toward `skippedCount` and does not trigger a skew alert — the boot image is managed correctly through the parent MachineDeployment, not ignored.

**Current OpenShift reality**: As of May 2026, `cluster-capi-operator` converts MAPI MachineSets to CAPI `MachineSets` only — there are no `MachineDeployment` controllers or conversion paths in the operator. The MCO `syncCAPIMachineDeployments` path is therefore forward-looking and will not reconcile anything on a current cluster. It is implemented now to keep the architecture symmetric and avoid a follow-up wiring change.

Additionally, CAPI MachineSets in OpenShift carry the `Cluster` object as a non-controller owner reference (for garbage collection), not a MachineDeployment reference. The MachineDeployment owner check in `syncCAPIMachineSet` will never match in the current OpenShift CAPI setup.

### Open Question: ClusterClass / Topology Mode

CAPI's `ClusterClass` allows a `Cluster` object to own and continuously reconcile `MachineDeployments` via a topology controller. In that mode:
- MachineDeployments are **not** given an owner reference to the Cluster — they are identified by the label `topology.cluster.x-k8s.io/owned` instead.
- The topology controller continuously reconciles the MachineDeployment's template reference back to what the ClusterClass specifies — so any boot image patch the MCO applies could be reverted.

**OpenShift does not currently use ClusterClass/topology mode** (confirmed from `cluster-capi-operator` source — no topology controller exists). If it is introduced in a future release, the MCO will need to detect topology-owned MachineDeployments via the label and decide whether to skip them (delegating to a ClusterClass-aware mechanism) or whether MCO patches can coexist with the topology controller.

---

## Feature Gate Strategy

The boot image controller uses a three-layer feature gate scheme:

| Layer | Feature gate | Scope | What it controls in the boot image controller |
|---|---|---|---|
| **Infrastructure** | `ClusterAPIMachineManagement` | Tech Preview | Gates CAPI informer setup and sync dispatch (`syncAll()`). Guarantees the CAPI CRDs exist on the cluster. Without this gate, dynamic informers would fail to list/watch non-existent CRDs and block the entire controller via `WaitForCacheSync`. Checked in `Run()` alongside the platform type and `ManagedBootImagesAWSCAPI` before any CAPI informer is created. |
| **Per-platform reconcile** | `ManagedBootImagesAWSCAPI` | Tech Preview | Gates the AWS case inside `checkCAPIMachineSet()`. Each platform will get its own gate as CAPI support is added. Platforms without an enabled gate no-op silently. |
| **MAPI/CAPI handoff** | `MachineAPIMigration` | Tech Preview | Gates the MAPI counterpart check in `syncCAPIMachineSet()` (defer to MAPI path if `authoritativeAPI != ClusterAPI`) and the migrated-set skip in `syncMAPIMachineSet()`. Only relevant during migration; on pure-CAPI clusters this check is skipped entirely. |

### Related gates (not used in the boot image controller)

| Feature gate | Scope | Purpose |
|---|---|---|
| `ClusterAPIMachineManagementAWS` | Tech Preview | Gates whether the AWS CAPI provider is deployed. Owned by the Cloud Compute / CAPI Providers team. The boot image controller does not check this — if the AWS provider isn't deployed, there are no `AWSMachineTemplate` objects to reconcile. |
| `MachineAPIMigrationAWS` | Tech Preview | Gates the AWS-specific migration controller in `cluster-capi-operator`. |
| `MachineAPIMigrationOpenStack` | Tech Preview | Gates the OpenStack-specific migration controller. |
| `MachineAPIMigrationVSphere` | Dev Preview | Gates the vSphere-specific migration controller. |

### Current platform status

Only AWS CAPI boot image reconciliation is enabled. Azure, GCP, and vSphere cases in `checkCAPIMachineSet` log and return early — no reconcile functions exist for those platforms yet. Their provider packages are not vendored; they will be added alongside the reconcile implementation when each platform is implemented.

### Why three layers

- **`ClusterAPIMachineManagement` for informers**: The CAPI CRDs only exist when this gate is on. Without it, the dynamic informer's reflector gets a 404 from the API server, `WaitForCacheSync` blocks, and the entire boot image controller is killed — not just the CAPI path.
- **`ManagedBootImagesAWSCAPI` for per-platform reconcile**: Separates MCO feature enablement from CAPI infrastructure presence. The gate also controls API validation — without it, users cannot enroll `cluster.x-k8s.io` resources in `MachineManager`. The controller-side check is belt-and-suspenders.
- **`MachineAPIMigration` for MAPI/CAPI handoff**: Only relevant when both MAPI and CAPI copies of the same MachineSet coexist during migration. On pure-CAPI clusters (no migration), there are no MAPI counterparts to check.

---

## RBAC

Two new rules added to `manifests/machineconfigcontroller/clusterrole.yaml`:

```yaml
- apiGroups: ["cluster.x-k8s.io"]
  resources: ["machinesets", "machinedeployments"]
  verbs: ["get", "list", "watch", "patch"]
- apiGroups: ["infrastructure.cluster.x-k8s.io"]
  resources: ["awsmachinetemplates"]
  verbs: ["get", "list", "watch", "create"]
```

Additional template resource types will be added to the RBAC rule as each platform is implemented.

`create` is required on infrastructure templates because the update flow clones the existing template under a new name rather than patching in place. `patch` is not needed — templates are immutable. `delete` is not needed — old template cleanup is a follow-up.

---

## Decisions

1. **Template naming convention**: `<machineset-name>-<fnv32a-hash>` where the hash is FNV-1a 32-bit over the new template spec JSON. Matches `cluster-capi-operator`'s `GenerateInfraMachineTemplateNameWithSpecHash`. No OCP version in the name — it added length on nightly builds without uniqueness benefit (the hash already disambiguates).
2. **`blockOwnerDeletion` stripping**: New templates are created with `blockOwnerDeletion: nil` on copied ownerReferences. The owner is the CAPI `Cluster` object, which already carries `cluster.cluster.x-k8s.io` finalizer. `blockOwnerDeletion` would require MCO to have `update` on `clusters/finalizers`, which it doesn't need.
3. **Old template cleanup**: Before deleting an orphaned template, check whether any existing `Machine` objects still reference it — other MachineSets may also point to the same template. Cleanup is a follow-up; do not delete until all referencing Machines are gone.
4. **Three-layer feature gate scheme**: `ClusterAPIMachineManagement` for CAPI informer infrastructure, `ManagedBootImagesAWSCAPI` for the per-platform AWS reconcile path, `MachineAPIMigration` for MAPI/CAPI handoff during migration. Each layer is independently load-bearing — see Feature Gate Strategy for rationale.
5. **CAPI namespace**: The controller needs visibility into `openshift-cluster-api`. List/watch CAPI resources from that namespace.
6. **Immutability enforcement**: Assume `Create` succeeds for now. Handle `Create` failures (e.g. AlreadyExists) as a follow-up if encountered in practice.
7. **Infrastructure template informer**: Wire only the AWS template informer (`awsmachinetemplates`), in `Run()` after the infra cache syncs, and only when the platform is confirmed to be AWS. Non-AWS clusters skip the entire CAPI informer setup — no factory, no watches. Additional template informers will be registered when other platforms are implemented.

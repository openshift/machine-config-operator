# Design: Boot Image Updates for CAPI MachineSets and MachineDeployments

## Background

The boot image controller (`pkg/controller/bootimage`) reconciles boot images for:
- MAPI `MachineSets` (`machine.openshift.io/v1beta1`)
- MAPI `ControlPlaneMachineSets` (`machine.openshift.io/v1`)
- CAPI `MachineSets` (`cluster.x-k8s.io/v1beta2`) — implemented on this branch

The controller struct carries `capiMachineSetStats` and `capiMachineDeploymentStats` stat fields, and the progressing/degraded conditions include CAPI message slots. The `syncCAPIMachineSets` and `syncCAPIMachineDeployments` callsites in `syncAll` are wired.

The `MachineAPIMigration` feature gate (Tech Preview) enables migration of MAPI MachineSets to CAPI authority. Once migrated, a MAPI MachineSet carries `spec.authoritativeAPI: ClusterAPI`, and writes should target the CAPI copy instead. The `ClusterAPIMachineManagement*` feature gates cover pure-CAPI clusters where MAPI never applies.

---

## Scope

1. Detect MAPI MachineSets that have been migrated to CAPI authority and skip patching them via the MAPI client.
2. Reconcile CAPI `MachineSets` (`cluster.x-k8s.io/v1beta2`) enrolled through the `MachineConfiguration` API.
3. Reconcile CAPI `MachineDeployments` (`cluster.x-k8s.io/v1beta2`) enrolled through the `MachineConfiguration` API.
4. Propose the API changes needed in `openshift/api` to allow enrollment of CAPI resources.

---

## API Changes Required (openshift/api)

Two additions are needed in `vendor/github.com/openshift/api/operator/v1/types_machineconfiguration.go`:

**1. New API group constant for CAPI:**
```go
// ClusterAPI represents the Cluster API group for CAPI-managed resources.
ClusterAPI MachineManagerMachineSetsAPIGroupType = "cluster.x-k8s.io"
```
This requires a new `FeatureGateAwareEnum` tag gating this value behind the `MachineAPIMigration` (or a new `ManagedBootImagesCAPI`) feature gate.

**2. New resource type for MachineDeployments:**
```go
// MachineDeployments represent the MachineDeployment resource type in the CAPI group.
MachineDeployments MachineManagerMachineSetsResourceType = "machinedeployments"
```
Also gated behind the same feature gate.

**3. Extend the `Automatic` skew enforcement restriction to CAPI resources:**

The existing API validation enforces that `BootImageSkewEnforcement` can only be set to `Automatic` mode when the MAPI MachineSet selection is `opv1.All`. This validation must be extended to cover CAPI MachineSets and MachineDeployments: `Automatic` mode should require `opv1.All` selection for all enrolled resource types. Until this lands, the controller-side `skippedCount == 0` gate is correct for MAPI but may be reached for CAPI resources without the `All` guarantee.

Until these land in `openshift/api`, `capiAPIGroup` and `capiMachineDeployments` are defined as local constants in `capi_helpers.go`.

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
| `sigs.k8s.io/cluster-api-provider-azure` | `api/v1beta1` | `AzureMachineTemplate` |
| `sigs.k8s.io/cluster-api-provider-gcp` | `api/v1beta1` | `GCPMachineTemplate` |
| `sigs.k8s.io/cluster-api-provider-vsphere` | `api/govmomi/v1beta1` | `VSphereMachineTemplate` |

> **Note:** The vsphere package path changed from `apis/v1beta1` (used in cluster-capi-operator v1.15.2) to `api/govmomi/v1beta1` in v1.16.1. The `govmomi` prefix distinguishes govmomi-based vSphere from supervisor (Tanzu) mode; it is not limited to upload operations. The on-cluster CRD version is `infrastructure.cluster.x-k8s.io/v1beta1` for all providers.

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

Inside the `FeatureGateMachineAPIMigration` gate block, a `DynamicSharedInformerFactory` is created for `openshift-cluster-api`. MachineSet and MachineDeployment informers are registered here with event handlers. The infrastructure template informer is **not** registered in `New()` — see the `Run()` two-phase init below.

### `Run()` two-phase cache sync

The template CRD to watch depends on the cluster platform, which is only known once the infra lister cache is warm. `Run()` therefore does a two-phase sync:

**Phase 1**: Start the capiInformerFactory (MachineSets and MachineDeployments only) and wait for core caches to sync including infra.

**Phase 2**: Call `wireCAPITemplateInformer()`, which reads the platform from the now-warm infra lister, calls `capiInformerFactory.ForResource(templateGVR)` for that platform only, starts the factory again (starting the newly registered informer), and waits for it to sync.

This avoids registering all four template informers, which would generate continuous "resource not found" errors on platforms where those CRDs don't exist.

### `syncMAPIMachineSets` change — skip migrated sets

In `syncMAPIMachineSet`, before any provider logic:

```go
switch machineSet.Spec.AuthoritativeAPI {
case machinev1beta1.MachineAuthorityClusterAPI, machinev1beta1.MachineAuthorityMigrating:
    // delete from mapiBootImageState, return patchSkipped=false
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
2. **MAPI authoritativeAPI check**: Look up the MAPI MachineSet with the same name. If it exists and `authoritativeAPI != ClusterAPI`, defer to the MAPI path (`patchSkipped=false`). Only proceed if MAPI is fully handed off or absent (pure-CAPI cluster).
3. **Stream label check**: Skip non-default OS streams.
4. **Windows check**: Skip MachineSets with the Windows OS label.
5. **Arch detection**: `getArchFromCAPIMachineSet` — reads arch annotation; defaults to control plane arch on single-arch clusters; errors on multi-arch clusters with no annotation.
6. **Release version guard**: Fetch the `coreos-bootimages` ConfigMap; skip if the OCP release version stored in it doesn't match the running MCO version (cluster upgrade in progress).
7. **Fetch infra template**: `ctrl.getCAPIInfraTemplate(name)` — looks up the template by name from `capiInfraTemplateLister`.
8. **Platform dispatch**: `checkCAPIMachineSet` dispatches to the per-platform reconcile function in `capi_platform_helpers.go`.
9. **Hot loop detection**: `checkCAPIMachineSetHotLoop` using `capiBootImageState`.
10. **Create + patch**: `patchCAPIMachineSet` — creates the new template (with `blockOwnerDeletion` stripped from ownerReferences), then patches the MachineSet's `infrastructureRef.name`.

---

## Skew Enforcement in Mixed Environments

Skew enforcement gates `updateClusterBootImage()` on the condition that no enrolled machine resource has a pending boot image update that was blocked. In a mixed MAPI/CAPI world, this check must span all three resource types: MAPI MachineSets, CAPI MachineSets, and CAPI MachineDeployments.

### Gate condition

`updateClusterBootImage()` is called from `syncAll`, after all three sync functions have completed:

```go
noSkips := ctrl.mapiStats.skippedCount == 0
if ctrl.fgHandler.Enabled(features.FeatureGateMachineAPIMigration) {
    // Only check CAPI stats when CAPI is active; avoids acting on stale
    // counts from a prior cycle where the gate was enabled but is now off.
    noSkips = noSkips &&
        ctrl.capiMachineSetStats.skippedCount == 0 &&
        ctrl.capiMachineDeploymentStats.skippedCount == 0
}
if noSkips {
    ctrl.updateClusterBootImage()
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

The API currently enforces that skew enforcement can only be set to `Automatic` mode when the MAPI MachineSet selection is `opv1.All`. This invariant does not yet extend to CAPI MachineSets or MachineDeployments — that is a required API change tracked above. Once extended, `updateClusterBootImage()` (which already bails early if `BootImageSkewEnforcementStatus.Mode != Automatic`) will carry the full guarantee across all three resource types, and the controller will not need to re-check selection modes — `skippedCount == 0` is the correct and sufficient condition.

The CAPI stats are guarded by `FeatureGateMachineAPIMigration` so that stale counts from a prior cycle where the gate was enabled cannot interfere if it is later disabled.

### Placement: `syncAll`, not `syncMAPIMachineSets`

The gate was previously embedded inside `syncMAPIMachineSets`, which ran before the CAPI syncs. Moving it to `syncAll`, after all three syncs complete, is what makes the multi-resource check possible. The old placement also meant that a zero MAPI skipped count alone could trigger `updateClusterBootImage()` even when CAPI resources had pending skips.

### Transitional state

During the `Migrating` transitional state, a MachineSet is removed from `mapiBootImageState` (MAPI returns `false`) and is not yet tracked by `capiBootImageState` (CAPI defers until `authoritativeAPI == ClusterAPI`). Stale entries from before the transition are cleared naturally without explicit migration-aware cleanup logic.

---

## Platform Support Mapping

| Platform | MAPI ProviderSpec field | CAPI infra template kind | Boot image field path | Status |
|---|---|---|---|---|
| AWS | `spec.ami.id` | `AWSMachineTemplate` | `spec.template.spec.ami.id` | Implemented |
| Azure | `spec.image` | `AzureMachineTemplate` | `spec.template.spec.image` | Implemented (Marketplace only) |
| GCP | `spec.disks[].image` | `GCPMachineTemplate` | `spec.template.spec.image` | Implemented |
| vSphere | `spec.template` | `VSphereMachineTemplate` | `spec.template.spec.template` | Stub (requires govc) |

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

| Feature gate | Scope | Enables |
|---|---|---|
| `MachineAPIMigration` | Tech Preview | Framework gate: enables `authoritativeAPI` field and the migration controllers in `cluster-capi-operator` |
| `MachineAPIMigrationAWS` | Tech Preview | Enables the AWS-specific migration controller in `cluster-capi-operator` |
| `MachineAPIMigrationOpenStack` | Tech Preview | Enables the OpenStack-specific migration controller |
| `MachineAPIMigrationVSphere` | Dev Preview | Enables the vSphere-specific migration controller (not yet Tech Preview as of May 2026) |
| `ClusterAPIMachineManagement` | Tech Preview | Enroll CAPI MachineDeployments on pure-CAPI clusters |
| (new) `ManagedBootImagesCAPI` | Tech Preview | Gate all CAPI reconciliation in the boot image controller (similar to `ManagedBootImagesCPMS`) |

`MachineAPIMigration` is a parent gate that enables the shared framework. The platform-specific gates (`MachineAPIMigrationAWS`, etc.) additionally gate the per-provider migration controllers in `cluster-capi-operator`. As of May 2026, only AWS and OpenStack migration controllers are implemented; Azure and GCP have no platform-specific gate defined. vSphere has a gate but it is Dev Preview only.

The practical implication for the MCO boot image controller: CAPI-authoritative MachineSets will only appear on AWS clusters (Tech Preview) in the near term. The Azure/GCP/vSphere CAPI reconcile paths in `capi_platform_helpers.go` are implemented ahead of the migration support to avoid a follow-up wiring change.

`ManagedBootImagesCAPI` is not an admin opt-in knob — it gates a new controller feature within MCO itself, analogous to how `ManagedBootImagesCPMS` gates the CPMS reconciliation path. `MachineAPIMigration` being enabled is a sufficient signal that CAPI infrastructure is present and operational (since `cluster-capi-operator` requires CAPI to be running before enabling the migration path). `ManagedBootImagesCAPI` is therefore purely about MCO feature enablement, not cluster capability detection.

> **Note:** `ClusterAPIMachineManagement` is defined in `openshift/api` but has zero references in `cluster-capi-operator` or `cluster-api` as of May 2026. The pure-CAPI management path it is meant to gate is not yet implemented in the CAPI ecosystem.

---

## RBAC

Two new rules added to `manifests/machineconfigcontroller/clusterrole.yaml`:

```yaml
- apiGroups: ["cluster.x-k8s.io"]
  resources: ["machinesets", "machinedeployments"]
  verbs: ["get", "list", "watch", "patch"]
- apiGroups: ["infrastructure.cluster.x-k8s.io"]
  resources: ["awsmachinetemplates", "azuremachinetemplates", "gcpmachinetemplates", "vspheremachinetemplates"]
  verbs: ["get", "list", "watch", "create"]
```

`create` is required on infrastructure templates because the update flow clones the existing template under a new name rather than patching in place. `patch` is not needed — templates are immutable. `delete` is not needed — old template cleanup is a follow-up.

---

## Decisions

1. **Template naming convention**: `<machineset-name>-<fnv32a-hash>` where the hash is FNV-1a 32-bit over the new template spec JSON. Matches `cluster-capi-operator`'s `GenerateInfraMachineTemplateNameWithSpecHash`. No OCP version in the name — it added length on nightly builds without uniqueness benefit (the hash already disambiguates).
2. **`blockOwnerDeletion` stripping**: New templates are created with `blockOwnerDeletion: nil` on copied ownerReferences. The owner is the CAPI `Cluster` object, which already carries `cluster.cluster.x-k8s.io` finalizer. `blockOwnerDeletion` would require MCO to have `update` on `clusters/finalizers`, which it doesn't need.
3. **Old template cleanup**: Before deleting an orphaned template, check whether any existing `Machine` objects still reference it — other MachineSets may also point to the same template. Cleanup is a follow-up; do not delete until all referencing Machines are gone.
4. **`ManagedBootImagesCAPI` gate**: Use a dedicated feature gate, for symmetry with `ManagedBootImagesCPMS`. This gates the controller feature within MCO, not admin enrollment of resources.
5. **CAPI namespace**: The controller needs visibility into `openshift-cluster-api`. List/watch CAPI resources from that namespace.
6. **Immutability enforcement**: Assume `Create` succeeds for now. Handle `Create` failures (e.g. AlreadyExists) as a follow-up if encountered in practice.
7. **Infrastructure template informer**: Wire only the platform-specific template informer, lazily in `Run()` after the infra cache syncs. Registering all four would cause reflector errors for CRDs that don't exist on the current platform.

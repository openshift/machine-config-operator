# MCO Domain Model - CRDs

All CRDs belong to API group `machineconfiguration.openshift.io/v1`. Types are defined in `vendor/github.com/openshift/api/machineconfiguration/v1/`. Compatibility Level 1 (stable within major release, min 12 months).

## MachineConfig

Cluster-scoped Ignition config fragment. No status subresource.

**Spec:** `config` (Ignition RawExtension), `kernelArguments` ([]string), `extensions` ([]string), `fips` (bool), `kernelType` (default|realtime|64k-pages), `osImageURL`, `baseOSExtensionsContainerImage`.

Multiple MachineConfigs are merged by the Render Controller into a single rendered config per pool. Merge order is deterministic (sorted by name). Conflicts in file paths use last-writer-wins.

## MachineConfigPool (short: mcp)

Groups nodes via `nodeSelector` and selects MachineConfigs via `machineConfigSelector`. Tracks update rollout status.

**Key spec:** `paused` (bool), `maxUnavailable` (IntOrString), `pinnedImageSets` (max 100), `configuration` (target rendered config).

**Key status:** `machineCount`, `updatedMachineCount`, `readyMachineCount`, `degradedMachineCount`, `conditions` (Updated, Updating, Degraded, NodeDegraded, RenderDegraded, Building, BuildSuccess, BuildFailed, etc.).

**Validation rules:** machineCount >= updatedMachineCount, machineCount >= readyMachineCount, etc.

Default pools: `master`, `worker`. Custom pools inherit from worker via `machineConfigSelector`.

## ControllerConfig

Cluster-wide configuration consumed by Template Controller to generate base MachineConfigs.

**Key spec:** `clusterDNSIP`, `rootCAData`, `kubeAPIServerServingCAData`, `additionalTrustBundle`, `pullSecret`, `images` (map), `baseOSContainerImage`, `releaseImage`, `proxy`, `infra`, `dns`, `ipFamilies` (IPv4|IPv6|DualStack|DualStackIPv6Primary), `networkType`.

**Status conditions:** TemplateControllerRunning, TemplateControllerCompleted, TemplateControllerFailing.

## KubeletConfig

Applies upstream kubelet configuration overrides per pool.

**Key spec:** `machineConfigPoolSelector` (LabelSelector), `kubeletConfig` (RawExtension - upstream KubeletConfiguration), `autoSizingReserved` (bool), `logLevel` (0-10), `tlsSecurityProfile`.

Controller generates a MachineConfig with the merged kubelet config and drops file at `/etc/kubernetes/kubelet.conf`.

## ContainerRuntimeConfig (short: ctrcfg)

Tunes CRI-O and container runtime settings per pool.

**Key spec:** `machineConfigPoolSelector`, `containerRuntimeConfig` with fields: `pidsLimit`, `logLevel` (fatal..debug), `logSizeMax` (>= 8192), `overlaySize` (default 10GB), `defaultRuntime` (crun|runc), `additionalLayerStores` (max 5), `additionalImageStores` (max 10), `additionalArtifactStores` (max 10).

Store paths validated: absolute, 1-256 chars, pattern `^/[a-zA-Z0-9/._-]+$`, no consecutive slashes.

## MachineConfigNode

Per-node update tracking. Created/managed by Node Controller and updated by MCD.

**Spec:** `configVersion.desired` (target MC ref), `pool.name`, `node.name`.

**Status conditions:** Updated, UpdatePrepared, UpdateExecuted, UpdatePostActionComplete, UpdateComplete, Resumed, UpdateCompatible, AppliedFiles, AppliedFilesAndOS, Cordoned, Uncordoned, Drained, RebootedNode, ReloadedCRIO, PinnedImageSetsDegraded, PinnedImageSetsProgressing.

## MachineOSConfig

Defines on-cluster layering (OCL) build inputs for a pool.

**Key spec:** `machineConfigPool` (ref), `buildInputs`, `containerFile` ([]ContainerFile with `content` and optional `containerfileArch`).

## MachineOSBuild

Tracks execution of an OCL build job.

**Key spec:** `machineOSConfig` (ref), `machineConfig` (ref).

**Key status:** `digestedImagePushSpec` (built image), `builder.job` (Job ref with name/namespace).

## OSImageStream

OS image references for the release. Managed by the osimagestream binary.

**Status:** `defaultStream`, `availableStreams` ([]OSImageStreamSpec with `name`, `osImage`, `osExtensionsImage`).

## PinnedImageSet

Pre-pulls container images to nodes in matching pools.

**Spec:** `machineConfigPoolSelector` (LabelSelector), `pinnedImages` ([]PinnedImage with `name`).

## Feature-Gated CRD Variants

CRDs ship in multiple variants for feature gates: Default, DevPreviewNoUpgrade, CustomNoUpgrade, TechPreviewNoUpgrade, OKD, Hypershift, SelfManagedHA. Located in `vendor/github.com/openshift/api/machineconfiguration/v1/zz_generated.crd-manifests/`.

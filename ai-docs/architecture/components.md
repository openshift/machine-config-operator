# MCO Architecture - Component Internals

## Binaries

MCO produces 7 binaries from `cmd/`:

| Binary | Deployment | Purpose |
|--------|-----------|---------|
| `machine-config-operator` | Deployment | Top-level operator: syncs all sub-components, manages ClusterOperator status |
| `machine-config-controller` | Deployment | Runs render, template, node, kubelet-config, container-runtime-config, certrotation, drain controllers |
| `machine-config-daemon` | DaemonSet | Node agent: fetches and applies configs, handles reboot/drain |
| `machine-config-server` | DaemonSet (masters) | Serves Ignition configs to bootstrapping/joining nodes via HTTPS |
| `machine-os-builder` | Deployment | Orchestrates OCL builds (MachineOSBuild jobs) |
| `machine-config-osimagestream` | Job | Generates OSImageStream from release payload |
| `apiserver-watcher` | DaemonSet | Monitors local apiserver, writes cloud-routes downfiles |

## Controller Details

### Template Controller (`pkg/controller/template/`)
- **Watches:** ControllerConfig, APIServer, Secrets, ConfigMaps
- **Produces:** Base MachineConfig objects from Go templates in `/etc/mcc/templates`
- **Templates organized by role:** `common/`, `master/`, `worker/`
- Merges IRI (Internal Release Image) secrets into generated configs
- Template rendering uses Go `text/template` with cluster state as input

### Render Controller (`pkg/controller/render/`)
- **Watches:** MachineConfig, MachineConfigPool, OSImageStream, ControllerConfig, ContainerRuntimeConfig
- **Produces:** Rendered (merged) MachineConfig per pool
- Merge is deterministic: MachineConfigs sorted by name, last-writer-wins for file conflicts
- Uses content-based hashing for rendered config naming (avoids unnecessary updates)
- 5-second render delay to batch rapid changes

### Node Controller (`pkg/controller/node/`)
- **Watches:** MachineConfigPool, MachineConfig, MachineOSConfig, MachineOSBuild, Node
- **Manages:** Node annotations (desired/current config), drain coordination, MachineConfigNode status
- Largest controller (~81KB). Orchestrates the full node update lifecycle:
  1. Detects config drift between desired and current
  2. Respects `maxUnavailable` for rolling updates
  3. Coordinates with MCD for drain, apply, reboot
  4. Updates MachineConfigPool status counters

### Build Controller (`pkg/controller/build/`)
- **Watches:** MachineOSBuild, MachineOSConfig, Job, MachineConfigPool
- **Manages:** OCL build lifecycle via Kubernetes Jobs
- Subdirectories: `buildrequest/` (build job creation), `imagebuilder/` (podman/buildah), `imagepruner/` (cleanup)
- Creates Jobs that run `podman build` with user-provided Containerfiles
- Tracks build status through MachineOSBuild conditions

### KubeletConfig Controller (`pkg/controller/kubelet-config/`)
- **Watches:** KubeletConfig, MachineConfigPool, ClusterOperator
- Merges upstream KubeletConfiguration into MachineConfig objects
- Handles feature gate propagation to kubelet config
- Supports `autoSizingReserved` for automatic resource reservation

### ContainerRuntimeConfig Controller (`pkg/controller/container-runtime-config/`)
- **Watches:** ContainerRuntimeConfig, MachineConfigPool
- Generates CRI-O config files (`/etc/crio/crio.conf.d/`), storage config, registries config
- Large helper surface (~63KB helpers.go) for registry mirror configuration

### Boot Image Controller (`pkg/controller/bootimage/`)
- **Watches:** MachineSet, ControlPlaneMachineSet, Infrastructure, ClusterVersion
- Updates cloud provider boot images (AMIs, Azure images, GCP images)
- Platform-specific logic for AWS, Azure, GCP, vSphere, OpenStack
- AMI mapping in `ami.go` (228KB of region-to-AMI mappings)

### Other Controllers
- **CertRotation** (`pkg/controller/certrotation/`): Rotates MCO serving certificates
- **PinnedImageSet** (`pkg/controller/pinnedimageset/`): Ensures images are pre-pulled to nodes
- **Drain** (`pkg/controller/drain/`): Coordinates node drain operations
- **InternalReleaseImage** (`pkg/controller/internalreleaseimage/`): Extracts image refs from release payloads

## Machine Config Daemon (`pkg/daemon/`)

The node-level agent. Core file `daemon.go` is ~110KB, `update.go` is ~135KB.

**Responsibilities:**
- Watches node annotations for desired config changes
- Applies Ignition configs: writes files, creates systemd units, sets kernel arguments
- Manages OS updates via rpm-ostree (traditional) or bootc (container-native)
- Decides reboot vs. service-reload based on change type
- Config drift monitoring (detects unauthorized file changes)
- Reports progress via MachineConfigNode conditions
- Pinned image set management (pre-pulling images)
- On-disk validation of applied configs

**Update decision tree:**
- File-only changes with no OS change: service reload (CRIO, kubelet)
- OS image change or kernel args: full node reboot
- FIPS change: reboot required
- Extension changes: reboot required

## Machine Config Server (`pkg/server/`)

HTTPS server on master nodes serving Ignition configs to joining nodes.

- `AppendersBuilder` pattern composes config from multiple sources
- Appenders: KubeConfig, certificates, node annotations
- Bootstrap mode: serves from filesystem during cluster install
- Cluster mode: serves from rendered MachineConfigs via API

## Operator Sync (`pkg/operator/`)

Top-level orchestration in `sync.go` (~113KB):

1. Syncs OSImageStream (image references)
2. Renders and deploys controller, daemon, server components
3. Manages RBAC, ServiceAccounts, ConfigMaps, Secrets
4. Reports ClusterOperator status and conditions
5. Handles upgrades: version skew, feature gate transitions

## Shared Infrastructure

### `pkg/controller/common/`
- `controller_context.go`: Shared informer factory, client sets, feature gate state
- `helpers.go` (~56KB): MC manipulation, Ignition utilities, annotation helpers
- `layered_node_state.go`: State tracking for OCL nodes
- `metrics.go`: Prometheus metric definitions
- `featuregates.go`: Feature gate detection and propagation

### `lib/resourceapply/` and `lib/resourceread/`
- Idempotent resource application (create-or-update pattern)
- Type-safe resource deserialization from YAML/JSON

## ValidatingAdmissionPolicies

Located in `manifests/`:
- `custom-machine-config-pool-selector`: Custom MCPs must inherit from worker
- `internalreleaseimage-deletion-guard`: Protects InternalReleaseImage from deletion
- `machineconfigpool-osimagestream-reference`: OSImageStream ref validation
- `machineconfiguration-guards`: Global MachineConfiguration guards
- `mcn-guards`: MachineConfigNode guards
- `osimagestream-deletion-guard`: Protects OSImageStream from deletion
- `update-bootimages`: Boot image update validation

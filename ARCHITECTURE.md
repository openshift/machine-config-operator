# Architecture Overview

## System Context

**This operator is**: Core Platform Operator (managed by cluster-version-operator)

**External Integrations:**

| System | Direction | Interface | Purpose |
|--------|-----------|-----------|---------|
| cluster-version-operator | Inbound | ClusterOperator status | Reports MCO health, manages MCO updates |
| machine-api-operator | Peer | MachineConfigPool watches Machine labels | Provisions machines, MCO configures them |
| Kubernetes API Server | Bidirectional | Watch/Update CRDs | Manages MachineConfig/Pool resources |
| etcd (on masters) | Protected | Static pods | MCO ensures etcd stability during updates |
| Individual Nodes | Outbound | MachineConfigDaemon (DaemonSet) | Applies OS configuration |
| Image Registry | Outbound | Container image pulls | Fetches OS update images (rpm-ostree) |

## Domain Architecture

### Package Layering

```
vendor/
  └── github.com/openshift/api/machineconfiguration/v1
        ↓ (API types only - imported by all packages)
pkg/
  ├── apihelpers/         → API validation and conversion utilities
  ├── controller/         → MachineConfig controllers (template, render, update, kubelet-config, container-runtime-config)
  │   ├── template/       → Generates OpenShift-owned MachineConfigs from templates
  │   ├── render/         → Merges MachineConfigs for each pool
  │   ├── node/           → Coordinates node updates with MCD
  │   ├── kubelet-config/ → KubeletConfig CRD controller
  │   └── container-runtime/ → ContainerRuntimeConfig CRD controller
  ├── daemon/             → MachineConfigDaemon (applies configs on nodes)
  │   ├── update.go       → Orchestrates updates, reboots, draining
  │   ├── rpm_ostree.go   → OS update operations
  │   └── drift.go        → Config drift detection (fsnotify)
  ├── operator/           → MCO main operator (manages component lifecycle)
  ├── server/             → MachineConfigServer (serves Ignition to new nodes)
  └── helpers/            → Shared utilities (OS detection, etc.)
```

### Dependency Rules (ENFORCED BY CODE REVIEW)

1. **pkg/controller** MUST NOT import **pkg/daemon** (separation of concerns)
2. **pkg/daemon** MUST NOT import **pkg/controller** (separation of concerns)
3. **pkg/operator** MAY import controller and server packages (orchestration)
4. All packages MAY import **pkg/apihelpers** and **pkg/helpers** (shared utilities)
5. Cross-component communication via Kubernetes API (node annotations, CRD status)

## Components

| Component | Entry Point | Critical Code | Purpose | Details |
|-----------|-------------|---------------|---------|---------|
| machine-config-operator | cmd/machine-config-operator/main.go | pkg/operator/sync.go | Manages lifecycle of other components, reports ClusterOperator status | [MCO](./agentic/design-docs/components/machine-config-operator.md) |
| machine-config-controller | cmd/machine-config-controller/main.go | pkg/controller/render/render_controller.go | Renders configs, coordinates updates | [MCC](./docs/MachineConfigController.md) |
| machine-config-daemon | cmd/machine-config-daemon/main.go | pkg/daemon/update.go | Applies configs on nodes, manages OS updates | [MCD](./docs/MachineConfigDaemon.md) |
| machine-config-server | cmd/machine-config-server/main.go | pkg/server/server.go | Serves Ignition configs to new nodes | [MCS](./docs/MachineConfigServer.md) |
| machine-os-builder | cmd/machine-os-builder/main.go | pkg/controller/build/ | Builds layered OS images (on-cluster layering) | [MOB](./docs/MachineOSBuilderDesign.md) |

## Data Flow

### Initial Node Provisioning

```
1. machine-api-operator provisions bare machine
   ↓
2. machine-config-server serves Ignition config (via /config/{pool} endpoint)
   ↓
3. Ignition runs on first boot, configures OS
   ↓
4. kubelet starts, node joins cluster
   ↓
5. machine-config-daemon starts, validates config matches MachineConfigPool
```

### Configuration Update Flow

```
User creates/updates MachineConfig
   ↓
RenderController (pkg/controller/render/) discovers all MachineConfigs for pool
   ↓
RenderController merges configs → rendered-{pool}-{hash}
   ↓
UpdateController (pkg/controller/node/) sets desiredConfig annotation on nodes
   ↓
MachineConfigDaemon (pkg/daemon/) detects annotation change
   ↓
MCD applies changes (files, units, OS update)
   ↓
MCD drains node (if needed), reboots (if needed)
   ↓
MCD validates post-reboot, sets currentConfig=desiredConfig
   ↓
UpdateController moves to next node (respecting maxUnavailable)
```

### OS Update Flow

```
cluster-version-operator updates release image
   ↓
MCO detects new OSImageURL in release payload
   ↓
TemplateController updates rendered MachineConfigs with new OSImageURL
   ↓
MCD calls rpm-ostree to pull new OS image
   ↓
rpm-ostree creates new deployment
   ↓
MCD reboots into new OS
   ↓
MCD validates OS version matches expected
```

## Critical Code Locations

| Function | File | Why Critical |
|----------|------|--------------|
| Merge MachineConfigs | pkg/controller/render/render_controller.go:Render() | Combines all configs for a pool |
| Coordinate node updates | pkg/controller/node/node_controller.go:syncMachineConfigPool() | Orchestrates rolling updates |
| Apply configuration | pkg/daemon/update.go:update() | Core update logic (files, units, OS) |
| Detect config drift | pkg/daemon/drift.go:Run() | Monitors filesystem for manual changes |
| Serve Ignition | pkg/server/server.go:getMachineConfig() | Bootstraps new nodes |
| Generate templates | pkg/controller/template/render.go:generateMachineConfigs() | Creates OpenShift-owned configs |
| OS updates | pkg/daemon/rpm_ostree.go:UpdateOS() | Calls rpm-ostree for OS updates |
| Node drain | pkg/daemon/drain.go:drain() | Safely evicts pods before reboot |

## OpenShift Ecosystem Dependencies

| Component | Relationship | Purpose | Where to Find Examples |
|-----------|--------------|---------|------------------------|
| openshift/api | Type definitions | MachineConfiguration API group | vendor/github.com/openshift/api/machineconfiguration/v1 |
| openshift/library-go | Shared patterns | Operator utilities, leader election, events | vendor/github.com/openshift/library-go/pkg/operator |
| cluster-version-operator | Coordinates with | Manages MCO updates, provides OS image | github.com/openshift/cluster-version-operator |
| machine-api-operator | Peer | Provisions machines that MCO configures | github.com/openshift/machine-api-operator |
| cluster-kube-apiserver-operator | Coordinates with | Kubelet CA cert rotation | github.com/openshift/cluster-kube-apiserver-operator |

**For implementation patterns**, check:
- **openshift/library-go**: Leader election, event recording, status reporting
- **cluster-version-operator**: ClusterOperator status patterns
- **cluster-kube-controller-manager-operator**: Similar operator lifecycle patterns

## Related Documentation

- [Design Philosophy](./agentic/DESIGN.md)
- [Domain Concepts](./agentic/domain/)
- [Component Details](./agentic/design-docs/components/)
- [ADRs](./agentic/decisions/)
- [Legacy Docs](./docs/) - Existing documentation (preserved)

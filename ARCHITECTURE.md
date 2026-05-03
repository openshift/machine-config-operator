# Architecture Overview

## System Context

**External Integrations:**

| System | Direction | Interface | File |
|--------|-----------|-----------|------|
| Kubernetes API | Bidirectional | Watch/Update CRDs | pkg/controller/common/layered_pool_state.go |
| Node OS (RHCOS) | Outbound | rpm-ostree, systemd | pkg/daemon/rpm-ostree.go |
| Ignition | Outbound | Config generation | pkg/server/server.go |
| Container Registry | Inbound | OS image pulls | pkg/daemon/pivot.go |

## Domain Architecture

### Package Layering (ENFORCED)

```
vendor/
  └── github.com/openshift/api/machineconfiguration/v1
        ↓ (types only)
pkg/
  ├── apihelpers/      → Helpers for MachineConfig API
  ├── controller/      → Reconciles pools, renders configs
  ├── daemon/          → Runs on nodes, applies configs
  ├── server/          → Serves Ignition at first boot
  └── operator/        → Manages the 3 sub-components
```

### Dependency Rules (ENFORCED BY LINTER)

1. `pkg/daemon/` MUST NOT import `pkg/controller/`
2. `pkg/server/` MUST NOT import `pkg/controller/`
3. Cross-component communication via Kubernetes API only

## Components

| Component | Entry Point | Critical Code | Purpose | Details |
|-----------|-------------|---------------|---------|---------|
| Machine Config Operator | cmd/machine-config-operator/main.go | pkg/operator/sync.go | Manages sub-components lifecycle | [link](./agentic/design-docs/components/machine-config-operator.md) |
| Machine Config Controller | cmd/machine-config-controller/main.go | pkg/controller/template/render.go | Renders MachineConfigs for pools | [link](./agentic/design-docs/components/machine-config-controller.md) |
| Machine Config Daemon | cmd/machine-config-daemon/main.go | pkg/daemon/update.go | Applies configs on nodes | [link](./agentic/design-docs/components/machine-config-daemon.md) |
| Machine Config Server | cmd/machine-config-server/main.go | pkg/server/server.go | Serves Ignition at boot | [link](./agentic/design-docs/components/machine-config-server.md) |

## Data Flow

```
User creates MachineConfig → API validates → Controller renders
  ↓
MachineConfigPool status updated (UpdateAvailable)
  ↓
Daemon on node detects new rendered config (pkg/daemon/update.go)
  ↓
Daemon applies changes (rpm-ostree, files, systemd)
  ↓
Status updated (Updated=true)
```

## Critical Code Locations

| Function | File | Why Critical |
|----------|------|--------------|
| Config rendering | pkg/controller/template/render.go | Merges multiple MachineConfigs into rendered version |
| Node update orchestration | pkg/daemon/update.go | Coordinates OS updates and reboots |
| Ignition generation | pkg/server/api.go | Provides first-boot configuration |
| rpm-ostree integration | pkg/daemon/rpm-ostree.go | Manages OS image updates |

See [complete package map](./agentic/generated/package-map.md) for details.

## Related Documentation

- [Design docs](./agentic/design-docs/)
- [Domain concepts](./agentic/domain/)
- [ADRs](./agentic/decisions/)

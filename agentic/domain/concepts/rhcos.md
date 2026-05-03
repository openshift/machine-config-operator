---
concept: RHCOS
type: Operating System
related: [rpm-ostree, Ignition, MachineConfig]
---

# RHCOS (Red Hat Enterprise Linux CoreOS)

## Definition

Red Hat Enterprise Linux CoreOS is a container-optimized operating system combining CoreOS technologies (Ignition, rpm-ostree) with Red Hat Enterprise Linux components, designed for OpenShift nodes.

## Purpose

Provides a minimal, immutable, automatically updating operating system purpose-built for running containerized workloads in OpenShift clusters.

## Key Technologies

### Ignition
**Purpose**: First-boot provisioning
**When**: Once at initial boot
**What**: Sets up disks, users, files, systemd units

### rpm-ostree
**Purpose**: OS updates and package layering
**When**: Runtime updates managed by MachineConfigDaemon
**What**: Atomic OS image updates with rollback

### CRI-O
**Purpose**: Container runtime
**What**: OCI-compatible runtime for Kubernetes

### systemd
**Purpose**: Init system and service management
**What**: Manages all system services

## Lifecycle

```
1. Node boots with RHCOS image
2. Ignition runs (first boot only)
3. Ignition configures system from MachineConfig
4. CRI-O and kubelet start
5. Node joins cluster
6. MachineConfigDaemon monitors for updates
7. Updates applied via rpm-ostree
8. Node reboots for OS changes
```

## Version Management

**Location**: MachineConfig spec.osImageURL
**Format**: Container image reference
**Example**: `quay.io/openshift-release-dev/ocp-v4.0-art-dev@sha256:abc123`

**Code**: pkg/daemon/pivot.go (RebaseLayered function)

## Package Layering

RHCOS supports layering additional RPM packages via MachineConfig extensions:

```yaml
spec:
  extensions:
    - kernel-devel
    - usbguard
```

**Code**: pkg/daemon/rpm-ostree.go
**Mechanism**: rpm-ostree install
**Note**: Triggers node reboot

## Read-Only Root Filesystem

- `/usr` is read-only (immutable OS image)
- `/etc` is writable (configuration)
- `/var` is writable (data, logs)

**Implication**: System files cannot be edited directly; use MachineConfig

## Common Patterns

### Checking RHCOS Version
```bash
oc get nodes -o jsonpath='{.items[*].status.nodeInfo.osImage}'
```

### Viewing OS Image
```bash
oc get machineconfig rendered-worker-abc123 -o jsonpath='{.spec.osImageURL}'
```

## Related Concepts

- [rpm-ostree](./rpm-ostree.md) - Update mechanism
- [Ignition](./ignition.md) - First-boot configuration
- [MachineConfig](./machineconfig.md) - Declarative OS configuration

## Implementation Details

- **OS Updates**: pkg/daemon/pivot.go
- **Layering**: pkg/daemon/rpm-ostree.go
- **Version Tracking**: pkg/version/version.go

## References

- [RHCOS docs](https://docs.openshift.com/container-platform/latest/architecture/architecture-rhcos.html)
- [OS Upgrades design](../../../docs/OSUpgrades.md)

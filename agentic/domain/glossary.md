# Glossary - machine-config-operator

> **Purpose**: Canonical definitions for all domain concepts.
> **Format**: Alphabetical order. Link to detailed docs.
> **OpenShift Markers**: 🔴 OpenShift-specific | ⚫ Kubernetes core | 🟡 Extended

## C

### ControllerConfig 🔴

**Definition**: OpenShift-specific CRD that provides configuration to the MachineConfigController, including cluster-wide settings and platform information.

**Type**: CRD

**Related**: MachineConfigController, MachineConfigPool

**Details**: [./concepts/controllerconfig.md]

## I

### Ignition ⚫

**Definition**: CoreOS configuration format for provisioning machines at first boot, specifying files, systemd units, users, and storage.

**Type**: Configuration Format

**Related**: MachineConfig, MachineConfigServer

**Details**: [./concepts/ignition.md]

## M

### MachineConfig 🔴

**Definition**: OpenShift CRD defining declarative specifications for node configuration including Ignition config, FIPS mode, kernel arguments, and extensions.

**Type**: CRD (API group: machineconfiguration.openshift.io/v1)

**Related**: MachineConfigPool, Ignition

**Details**: [./concepts/machineconfig.md]

### MachineConfigController 🔴

**Definition**: OpenShift component that reconciles MachineConfigPools, renders configurations, and coordinates updates across node pools.

**Type**: Controller Component

**Related**: MachineConfig, MachineConfigPool, ControllerConfig

**Details**: [../design-docs/components/machine-config-controller.md]

### MachineConfigDaemon 🔴

**Definition**: OpenShift daemon running on each node that applies MachineConfig changes to the operating system, including OS updates, file modifications, and systemd unit changes.

**Type**: Daemon Component

**Related**: MachineConfig, rpm-ostree

**Details**: [../design-docs/components/machine-config-daemon.md]

### MachineConfigPool 🔴

**Definition**: OpenShift CRD that groups nodes (via label selectors) and applies targeted MachineConfigs, managing rolling updates with maxUnavailable constraints.

**Type**: CRD (API group: machineconfiguration.openshift.io/v1)

**Related**: MachineConfig, Node

**Details**: [./concepts/machineconfigpool.md]

### MachineConfigServer 🔴

**Definition**: OpenShift component serving Ignition configurations to nodes during first boot, based on their pool membership.

**Type**: Server Component

**Related**: Ignition, MachineConfig

**Details**: [../design-docs/components/machine-config-server.md]

## R

### RHCOS (Red Hat CoreOS) ⚫

**Definition**: Container-optimized operating system combining CoreOS technologies with RHEL components, using rpm-ostree for updates.

**Type**: Operating System

**Related**: rpm-ostree, Ignition

**Details**: [./concepts/rhcos.md]

### rpm-ostree ⚫

**Definition**: Hybrid image/package system combining libostree (git-like OS snapshots) with RPM, enabling atomic OS updates and rollbacks.

**Type**: Package Management System

**Related**: RHCOS, MachineConfigDaemon

**Details**: [./concepts/rpm-ostree.md]

### Rendered Config 🔴

**Definition**: Immutable MachineConfig (prefixed with `rendered-`) that is the merged result of all applicable MachineConfigs for a pool, with remote content embedded.

**Type**: Concept

**Related**: MachineConfig, MachineConfigPool

**Details**: [./concepts/machineconfig.md#rendered-configs]

## OpenShift vs Kubernetes

| OpenShift | Kubernetes | Notes |
|-----------|------------|-------|
| MachineConfig | No equivalent | OpenShift extends node management to OS level |
| MachineConfigPool | No equivalent | Similar concept to node pools but manages OS config |
| rpm-ostree | Various package managers | RHCOS-specific, atomic updates |
| Ignition | cloud-init | CoreOS configuration format, runs once at first boot |

## See Also

- [Domain concepts](./concepts/) - Detailed explanations
- [Workflows](./workflows/) - How concepts interact
- [ARCHITECTURE.md](../../ARCHITECTURE.md) - System structure

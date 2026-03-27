# Glossary - machine-config-operator

> **Purpose**: Canonical definitions for all domain concepts.
> **Format**: Alphabetical order. Link to detailed docs.
> **Markers**: 🔴 OpenShift-specific | ⚫ Kubernetes core | 🟡 Extended

## C

### ClusterOperator 🔴

**Definition**: OpenShift CRD for reporting operator status to cluster-version-operator

**Type**: OpenShift API (config.openshift.io/v1)

**Related**: machine-config-operator, cluster-version-operator

**Details**: Used by MCO to report Available/Progressing/Degraded status

### ControllerConfig 🔴

**Definition**: Global configuration for machine-config-controller components

**Type**: OpenShift CRD (machineconfiguration.openshift.io/v1)

**Related**: MachineConfigController, TemplateController

**Details**: [./concepts/controller-config.md](./concepts/controller-config.md)

## I

### Ignition ⚫

**Definition**: Declarative configuration format for provisioning and configuring machines (developed by CoreOS)

**Type**: Configuration Format

**Related**: MachineConfig, CoreOS, Red Hat CoreOS

**Details**: [./concepts/ignition.md](./concepts/ignition.md)

## M

### MachineConfig 🔴

**Definition**: Kubernetes Custom Resource defining declarative operating system configuration (files, systemd units, users)

**Type**: OpenShift CRD (machineconfiguration.openshift.io/v1)

**Related**: MachineConfigPool, Ignition, RenderController

**Details**: [./concepts/machine-config.md](./concepts/machine-config.md)

### MachineConfigController (MCC) 🔴

**Definition**: Component that renders MachineConfigs and coordinates updates across nodes

**Type**: OpenShift Component

**Related**: MachineConfigOperator, MachineConfigPool, RenderController, UpdateController

**Details**: [../design-docs/components/machine-config-controller.md](../design-docs/components/machine-config-controller.md)

### MachineConfigDaemon (MCD) 🔴

**Definition**: DaemonSet that applies configuration and OS updates on each node

**Type**: OpenShift Component (DaemonSet)

**Related**: MachineConfig, rpm-ostree, UpdateController

**Details**: [../design-docs/components/machine-config-daemon.md](../design-docs/components/machine-config-daemon.md)

### MachineConfigOperator (MCO) 🔴

**Definition**: Main operator managing lifecycle of MCC, MCD, MCS components and reporting ClusterOperator status

**Type**: OpenShift Operator

**Related**: MachineConfigController, MachineConfigDaemon, MachineConfigServer

**Details**: [../design-docs/components/machine-config-operator.md](../design-docs/components/machine-config-operator.md)

### MachineConfigPool 🔴

**Definition**: Groups of nodes with shared configuration (e.g., master, worker)

**Type**: OpenShift CRD (machineconfiguration.openshift.io/v1)

**Related**: MachineConfig, Node, maxUnavailable

**Details**: [./concepts/machine-config-pool.md](./concepts/machine-config-pool.md)

### MachineConfigServer (MCS) 🔴

**Definition**: Component that serves Ignition configs to new nodes during bootstrap

**Type**: OpenShift Component (DaemonSet on masters)

**Related**: Ignition, MachineConfig, node bootstrap

**Details**: [../design-docs/components/machine-config-server.md](../design-docs/components/machine-config-server.md)

### maxUnavailable 🔴

**Definition**: Maximum number of nodes in a pool that can be updating simultaneously

**Type**: MachineConfigPool field

**Related**: MachineConfigPool, UpdateController, controlled rollout

**Details**: Default is 1, prevents cluster-wide outages during updates

## O

### OSTree ⚫

**Definition**: Filesystem and versioning system for immutable operating systems (like git for /usr)

**Type**: Technology

**Related**: rpm-ostree, Red Hat CoreOS

**Details**: Foundation for rpm-ostree, provides atomic filesystem operations

## R

### Red Hat CoreOS (RHCOS) 🔴

**Definition**: Container-optimized operating system used by OpenShift, based on RHEL + rpm-ostree

**Type**: Operating System

**Related**: rpm-ostree, Ignition, OSTree

**Details**: Immutable base OS, configured via Ignition, updated via rpm-ostree

### RenderController 🔴

**Definition**: Controller that merges multiple MachineConfigs into a single rendered config per pool

**Type**: Sub-controller within MachineConfigController

**Related**: MachineConfig, MachineConfigPool, TemplateController

**Details**: Creates `rendered-{pool}-{hash}` MachineConfigs

### rpm-ostree 🟡

**Definition**: Hybrid package/image system combining RPM and OSTree for atomic OS updates

**Type**: Technology (upstream project extended by Red Hat)

**Related**: OSTree, Red Hat CoreOS, MachineConfigDaemon

**Details**: [./concepts/rpm-ostree.md](./concepts/rpm-ostree.md)

## T

### TemplateController 🔴

**Definition**: Controller that generates OpenShift-owned MachineConfigs from internal templates

**Type**: Sub-controller within MachineConfigController

**Related**: MachineConfig, ControllerConfig, RenderController

**Details**: Creates configs like `00-master`, `00-worker` from templates

## U

### UpdateController 🔴

**Definition**: Controller that coordinates rolling updates across nodes in a pool

**Type**: Sub-controller within MachineConfigController

**Related**: MachineConfigPool, MachineConfigDaemon, maxUnavailable

**Details**: Respects maxUnavailable, coordinates with MCD via node annotations

---

## OpenShift vs Kubernetes

| OpenShift (MCO) | Kubernetes Equivalent | Notes |
|-----------------|----------------------|-------|
| MachineConfig | No equivalent | Declarative OS config is OpenShift-specific |
| MachineConfigPool | No equivalent | Pool-based config management is OpenShift-specific |
| Ignition | cloud-init | Ignition is declarative, cloud-init is imperative |
| rpm-ostree | apt/yum | rpm-ostree provides atomic updates, apt/yum do not |
| ClusterOperator | No equivalent | OpenShift operator status reporting pattern |

---

## See Also

- [Domain Concepts](./concepts/) - Detailed explanations
- [Workflows](./workflows/) - How concepts interact
- [ARCHITECTURE.md](../../ARCHITECTURE.md) - System structure
- [Core Beliefs](../design-docs/core-beliefs.md) - Operating principles

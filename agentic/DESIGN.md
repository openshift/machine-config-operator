# Design Philosophy - machine-config-operator

## Overview

The machine-config-operator treats the operating system as a declaratively managed Kubernetes component. OS configuration and updates flow through the same GitOps workflows as application deployments, enabling reproducible, auditable infrastructure management at scale.

## Design Principles

### 1. Operating System as API

The OS is not infrastructure to be manually managed—it's a Kubernetes resource with a spec and status.

**Why**: Enables GitOps, auditability, and consistency across hundreds/thousands of nodes

**Example**: Changing kubelet configuration happens via `kubectl apply -f kubeletconfig.yaml`, not SSH + manual edits

### 2. Immutability with Flexibility

Base OS is immutable (rpm-ostree), but configuration is declarative (Ignition) and can be layered (Containerfile).

**Why**: Balance between security/reliability (immutable base) and operational needs (declarative config)

**Example**: Cannot `yum install` packages, but can add packages via layering or configure via MachineConfig

### 3. Atomic, Auditable Changes

All changes are atomic (succeed or rollback), versioned, and trackable in git/Kubernetes API.

**Why**: Prevents partial failures, enables rollback, provides audit trail

**Example**: OS update either fully succeeds or automatically rolls back to previous deployment

### 4. Gradual Rollout as Default

Never update all nodes simultaneously—controlled rollout with `maxUnavailable` prevents cluster-wide failures.

**Why**: Preserves workload availability, catches issues before affecting entire cluster

**Example**: 100-node pool with `maxUnavailable: 5` updates 5 nodes at a time

### 5. Declarative Over Imperative

Describe desired state (MachineConfig), not steps to achieve it (bash scripts).

**Why**: Idempotent, easier to reason about, validates before applying

**Example**: Ignition configs (declarative) vs. cloud-init scripts (imperative)

## Architecture Decisions

Key architectural decisions that shape this codebase:

1. **Four-Component Architecture**: Operator, Controller, Daemon, Server separation
   - See: [ADR-0003](./decisions/adr-0003-four-component-architecture.md)

2. **Ignition for Configuration**: Declarative OS config format
   - See: [ADR-0001](./decisions/adr-0001-use-ignition-for-configuration.md)

3. **rpm-ostree for Updates**: Image-based, atomic OS updates
   - See: [ADR-0002](./decisions/adr-0002-use-rpm-ostree-for-updates.md)

4. **Annotation-Based Coordination**: MCC and MCD coordinate via node annotations
   - See: [Core Beliefs](./design-docs/core-beliefs.md#annotation-based-coordination)

## Design Patterns

### Template + Render + User Pattern

**What**: Three sources of MachineConfigs merged by RenderController

**When to use**: Organizing MachineConfig ownership

**Example**:
- Template: `00-worker` (OpenShift-generated)
- Controller: `99-worker-generated-kubelet` (from KubeletConfig CRD)
- User: `99-my-custom-config` (admin-created)

RenderController merges all → `rendered-worker-{hash}`

### Controller-Per-CRD Pattern

**What**: Separate controllers for each high-level CRD (KubeletConfig, ContainerRuntimeConfig, etc.)

**When to use**: Extending MCO with new configuration types

**Example**: KubeletConfigController watches KubeletConfig CRD, generates MachineConfig with kubelet settings

### Config Drift as Error State

**What**: Filesystem changes are detected and marked as degraded state

**When to use**: Always - MCD monitors all files defined in MachineConfig

**Example**: Manual edit to `/etc/kubernetes/kubelet.conf` triggers `state=Degraded` within seconds

## Anti-Patterns to Avoid

### ❌ Manual Node Configuration

**Don't**: SSH to nodes and manually edit config files

**Do**: Create MachineConfig and let MCO apply it

**Why**: Breaks declarative model, causes config drift, bypasses auditability

### ❌ Bypassing Update Coordination

**Don't**: Manually drain/reboot nodes or skip MachineConfigPool

**Do**: Let UpdateController coordinate rollout

**Why**: Risk of exceeding maxUnavailable, breaking workload availability

### ❌ Imperative Scripts in Ignition

**Don't**: Use Ignition to run bash scripts that perform configuration

**Do**: Use Ignition declarative primitives (files, units)

**Why**: Scripts are harder to validate, not idempotent, error-prone

## Trade-offs

### 1. Immutable OS vs. Traditional Package Management

**What we chose**: Immutable base OS (rpm-ostree), configuration via MachineConfig

**What we gave up**: Ability to `yum install` individual packages on nodes

**Why**: Atomic updates and rollback are more valuable than per-package flexibility. Layering provides escape hatch when needed.

### 2. Reboot Required vs. In-Place Updates

**What we chose**: Support both reboot and select rebootless updates

**What we gave up**: Simplicity of always rebooting

**Why**: Reducing unnecessary reboots improves availability, but adds complexity to determine when reboot is needed

### 3. Controller Coordination vs. Direct Daemon Control

**What we chose**: MCC coordinates, MCD executes

**What we gave up**: Simpler direct control of daemons

**Why**: Centralized coordination enables rollout control, status aggregation, but requires annotation-based state machine

## Related Documentation

- [Core Beliefs](./design-docs/core-beliefs.md) - Operating principles and patterns
- [Architecture](../ARCHITECTURE.md) - System structure and components
- [ADRs](./decisions/) - Point-in-time architectural decisions
- [Domain Concepts](./domain/) - Key concepts like MachineConfig, rpm-ostree, Ignition

---
id: ADR-0003
title: Four-Component Architecture (MCO/MCC/MCD/MCS)
date: 2025-03-25
status: accepted
deciders: [machine-config-operator team]
supersedes: null
superseded-by: null
---

# Four-Component Architecture (MCO/MCC/MCD/MCS)

## Status

Accepted (implemented)

## Context

The machine-config-operator needs to manage OS configuration across potentially hundreds or thousands of nodes in an OpenShift cluster. Different responsibilities require different deployment patterns:
- Managing operator lifecycle and reporting status
- Rendering and coordinating configuration updates
- Applying configuration on individual nodes
- Bootstrapping new nodes during provisioning

A monolithic operator would run all logic in one place, leading to:
- Difficult separation of concerns
- Challenging to scale different functions independently
- Hard to debug and maintain

## Decision

Split the MCO into four separate components:

1. **machine-config-operator (MCO)**: Manages lifecycle of other components, reports ClusterOperator status
2. **machine-config-controller (MCC)**: Renders MachineConfigs, coordinates updates across pools
3. **machine-config-daemon (MCD)**: Runs on each node (DaemonSet), applies configs locally
4. **machine-config-server (MCS)**: Serves Ignition configs to new nodes during bootstrap

## Rationale

### Why This?

- **Separation of concerns**: Each component has a single, well-defined responsibility
- **Deployment flexibility**: DaemonSet for MCD, Deployment for others
- **Security isolation**: MCD runs privileged on nodes, others don't
- **Scalability**: MCC can scale independently of MCD
- **Debugging**: Easy to identify which component is failing
- **Testability**: Can test each component independently

### Why Not Alternatives?

- **Monolithic operator**: All logic in one binary
  - Rejected: Too complex, hard to maintain, can't scale components independently

- **Only two components** (controller + daemon):
  - Rejected: Operator lifecycle and server bootstrap are distinct concerns

- **More fine-grained components** (6+ components):
  - Rejected: Over-engineering, unnecessary complexity for current needs

## Consequences

### Positive

- ✅ Clear separation of concerns (operator management vs. config rendering vs. node updates vs. bootstrap)
- ✅ Can scale MCC replicas without affecting MCD
- ✅ MCD runs privileged only on nodes (not in control plane)
- ✅ Easy to identify and debug component-specific issues
- ✅ Can update/restart components independently
- ✅ MCS can be scaled independently for large cluster bootstraps

### Negative

- ❌ More components to maintain and monitor
- ❌ More inter-component communication (via Kubernetes API)
- ❌ More complex deployment (4 sets of manifests)
- ❌ Need to coordinate versioning across components

### Neutral

- ℹ️ All components built from same repository (versioning stays in sync)
- ℹ️ Communication via Kubernetes API (node annotations, CRD status)
- ℹ️ MCO manages deployment of MCC, MCD, MCS (ensures version compatibility)

## Implementation

- **Components**:
  - **MCO**: `cmd/machine-config-operator/main.go`, deployed as Deployment (1 replica)
  - **MCC**: `cmd/machine-config-controller/main.go`, deployed as Deployment (3 replicas typical)
  - **MCD**: `cmd/machine-config-daemon/main.go`, deployed as DaemonSet (1 per node)
  - **MCS**: `cmd/machine-config-server/main.go`, deployed as DaemonSet on masters

- **Communication**:
  - MCO → MCC/MCD/MCS: Manages Deployments/DaemonSets
  - MCC → Kubernetes API: Updates MachineConfigPool status
  - MCC → MCD: Via node annotations (`desiredConfig`, etc.)
  - MCD → MCC: Via node annotations (`currentConfig`, `state`)
  - MCS → Nodes: Serves Ignition configs via HTTPS

- **Status**: Implemented since MCO inception

## Alternatives Considered

### Alternative 1: Monolithic Operator

**Pros**:
- Single binary to build/deploy
- Simpler deployment
- No inter-component versioning issues

**Cons**:
- All logic in one place (hard to maintain)
- Cannot scale different functions independently
- Privileged code mixed with unprivileged code
- Difficult to test and debug

**Why rejected**: Violates separation of concerns, limits scalability

### Alternative 2: Microservices Architecture (6+ components)

**Pros**:
- Maximum separation of concerns
- Each tiny piece can be independently scaled

**Cons**:
- Over-engineering for current needs
- Too many moving parts
- Complex orchestration
- Difficult to understand system behavior

**Why rejected**: Unnecessary complexity, diminishing returns

### Alternative 3: Two Components (Controller + Daemon)

**Pros**:
- Simpler than four components
- Clear controller vs. agent split

**Cons**:
- Operator lifecycle management is distinct from config rendering
- Bootstrap server is distinct from update coordination
- Mixing responsibilities in controller

**Why rejected**: Operator management and bootstrap are distinct enough to warrant separate components

## References

- [MachineConfigController Documentation](../../docs/MachineConfigController.md)
- [MachineConfigDaemon Documentation](../../docs/MachineConfigDaemon.md)
- [MachineConfigServer Documentation](../../docs/MachineConfigServer.md)
- [Architecture Overview](../../ARCHITECTURE.md)

## Notes

- MCO was later extended with **machine-os-builder** (5th component) for on-cluster layering (OCP 4.12+)
- All components use shared code from `pkg/` packages
- MCO ensures all components run compatible versions (prevents version skew)
- Sub-controllers within MCC (Template, Render, Update, KubeletConfig, etc.) are logical divisions, not separate processes

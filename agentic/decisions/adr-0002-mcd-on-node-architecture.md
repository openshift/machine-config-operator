---
id: ADR-0002
title: MachineConfigDaemon On-Node Architecture
date: 2020-02-10
status: accepted
deciders: [OpenShift MCO Team]
supersedes: null
superseded-by: null
enhancement-refs: []
---

# MachineConfigDaemon On-Node Architecture

## Status

Accepted (implemented)

## Context

OpenShift needs to apply configuration changes and OS updates to nodes. Configuration needs to be applied consistently, and updates need to happen in a controlled, safe manner. The question was: how should the agent running on each node be architected?

Options considered:
1. Centralized controller directly modifying nodes via SSH
2. Lightweight agent executing commands from controller
3. Autonomous daemon with reconciliation loop

## Decision

Implement MachineConfigDaemon (MCD) as an autonomous DaemonSet on each node with a reconciliation loop that compares desired state (from MachineConfig API) against current state and takes corrective action.

## Rationale

### Why This?
- **Kubernetes-native**: Runs as DaemonSet, managed by cluster lifecycle
- **Declarative**: Reads desired state from API, reconciles current state
- **Resilient**: Continues to work even if control plane is temporarily unavailable
- **Observable**: Reports status via API, visible to cluster operators
- **Autonomous**: Makes local decisions, reduces control plane load
- **Consistent with Kubernetes patterns**: Follows standard controller/reconciliation model

### Why Not Alternatives?
- **SSH-based**: Requires SSH credentials, complex key management, not Kubernetes-native, poor observability
- **Command-execution agent**: Requires complex orchestration logic in controller, agent is not autonomous, higher coupling

## Consequences

### Positive
- ✅ Autonomous operation survives control plane disruptions
- ✅ Status reporting via Kubernetes API (standard observability)
- ✅ No special credential management (uses node's service account)
- ✅ Scales with cluster (DaemonSet automatically deploys to new nodes)
- ✅ Consistent with Kubernetes operational model

### Negative
- ❌ More complex implementation (full reconciliation logic in daemon)
- ❌ Daemon runs privileged on each node (security surface)
- ❌ Requires careful error handling (daemon must not crash-loop)

### Neutral
- ℹ️ Daemon size is larger (contains full reconciliation logic)
- ℹ️ Requires understanding of Kubernetes controller patterns

## Implementation

- **Location**: pkg/daemon/daemon.go (main reconciliation loop), cmd/machine-config-daemon/main.go (entry point)
- **Migration**: Existing from OpenShift 4.0
- **Rollout**: Deployed as DaemonSet to all nodes

**Key components**:
- **Main loop**: pkg/daemon/daemon.go:Run() - Reconciliation loop
- **Config comparison**: pkg/daemon/update.go:reconcilable() - Determines if update needed
- **Update execution**: pkg/daemon/update.go:updateOSAndReboot() - Applies changes
- **Status reporting**: pkg/daemon/writer.go:UpdateStateOnDisk() - Reports to API

## Alternatives Considered

### Alternative 1: SSH-Based Centralized Updates
**Pros**: Simple central orchestration, no daemon needed
**Cons**: SSH key management complex, not Kubernetes-native, poor observability, doesn't scale
**Why rejected**: Not aligned with Kubernetes operational model, security concerns

### Alternative 2: Thin Command-Execution Agent
**Pros**: Smaller daemon, simpler implementation
**Cons**: All logic in central controller (single point of failure), tight coupling, not resilient to control plane issues
**Why rejected**: Lacks autonomy, reduces resilience

### Alternative 3: Ansible-Based
**Pros**: Mature configuration management tool, large ecosystem
**Cons**: Not Kubernetes-native, requires inventory management, harder to integrate with cluster lifecycle
**Why rejected**: Too much operational overhead, not declarative Kubernetes-style

## References

- [MachineConfigDaemon design doc](../../docs/MachineConfigDaemon.md)
- [DaemonSet manifest](../../manifests/machineconfigdaemon.yaml)
- [Kubernetes DaemonSet docs](https://kubernetes.io/docs/concepts/workloads/controllers/daemonset/)

## Notes

This decision follows the established Kubernetes pattern of autonomous agents (kubelet, kube-proxy, etc.) running on nodes as DaemonSets. The reconciliation loop pattern has proven robust across millions of node-hours in production.

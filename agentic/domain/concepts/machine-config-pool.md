---
concept: MachineConfigPool
type: CRD
related: [MachineConfig, Node, UpdateController]
---

# MachineConfigPool

## Definition

MachineConfigPool (MCP) is a Kubernetes Custom Resource that groups nodes with shared operating system configuration. It defines which MachineConfigs apply to which nodes and controls how updates roll out across the pool.

## Purpose

Enables operators to:
- Group nodes by role (master, worker, or custom pools)
- Apply different OS configurations to different node groups
- Control update rollout speed (`maxUnavailable`)
- Pause updates for maintenance windows
- Monitor update progress and node health

Why this exists: Different node groups need different configurations (masters vs. workers), and updates must roll out gradually to prevent cluster-wide outages.

## Location in Code

- **API Definition**: `vendor/github.com/openshift/api/machineconfiguration/v1/types.go:MachineConfigPool`
- **Controller**: `pkg/controller/node/node_controller.go` (UpdateController)
- **Status Updates**: `pkg/controller/render/render_controller.go`
- **Tests**: `pkg/controller/node/*_test.go`

## Lifecycle

```
1. MachineConfigPool created (typically by installer for master/worker)
   ↓
2. RenderController watches pool, discovers matching MachineConfigs (via machineConfigSelector)
   ↓
3. RenderController merges configs → rendered-{pool}-{hash}
   ↓
4. RenderController updates pool.status.configuration with rendered config name
   ↓
5. UpdateController watches pool, coordinates node updates (respecting maxUnavailable)
   ↓
6. Pool status updated: machineCount, updatedMachineCount, readyMachineCount, degradedMachineCount
```

## Key Fields / Properties

### spec.machineConfigSelector

**Type**: metav1.LabelSelector
**Purpose**: Selects which MachineConfigs apply to this pool
**Example**:
```yaml
spec:
  machineConfigSelector:
    matchLabels:
      machineconfiguration.openshift.io/role: worker
```

**How it works**: All MachineConfigs with matching label are merged for this pool

### spec.nodeSelector

**Type**: metav1.LabelSelector
**Purpose**: Selects which nodes belong to this pool
**Example**:
```yaml
spec:
  nodeSelector:
    matchLabels:
      node-role.kubernetes.io/worker: ""
```

**How it works**: Nodes with matching label are part of this pool

### spec.maxUnavailable

**Type**: int or string (percentage)
**Purpose**: Maximum nodes updating simultaneously
**Example**:
```yaml
spec:
  maxUnavailable: 1  # Update 1 node at a time
  # OR
  maxUnavailable: "10%"  # Update 10% of nodes at a time
```

**Default**: 1 (safest, slowest)
**Impact**: Higher values = faster updates but more risk

### spec.paused

**Type**: bool
**Purpose**: Stop all updates to this pool
**Example**:
```yaml
spec:
  paused: true
```

**When to use**: Maintenance windows, investigating issues, controlled change windows

### status.observedGeneration

**Type**: int64
**Purpose**: Last generation processed by controller
**Use**: Detect if controller has processed latest spec

### status.configuration

**Type**: MachineConfigPoolConfiguration
**Purpose**: Current and desired MachineConfig for pool
**Example**:
```yaml
status:
  configuration:
    name: rendered-worker-abc123
    source:
    - apiVersion: machineconfiguration.openshift.io/v1
      kind: MachineConfig
      name: 00-worker
    - apiVersion: machineconfiguration.openshift.io/v1
      kind: MachineConfig
      name: 99-worker-custom
```

**Fields**:
- `name`: Rendered MachineConfig name
- `source`: List of MachineConfigs merged

### status.machineCount

**Type**: int32
**Purpose**: Total nodes in pool
**Example**: `machineCount: 5`

### status.updatedMachineCount

**Type**: int32
**Purpose**: Nodes with currentConfig == pool's rendered config
**Example**: `updatedMachineCount: 3` (3 out of 5 nodes updated)

### status.readyMachineCount

**Type**: int32
**Purpose**: Nodes that are Ready (per Kubernetes node status)
**Example**: `readyMachineCount: 5`

### status.unavailableMachineCount

**Type**: int32
**Purpose**: Nodes that are not Ready or updating
**Example**: `unavailableMachineCount: 1`

### status.degradedMachineCount

**Type**: int32
**Purpose**: Nodes marked Degraded (config drift, update failed)
**Example**: `degradedMachineCount: 0` (good!)

### status.conditions

**Type**: []MachineConfigPoolCondition
**Purpose**: Pool health status
**Example**:
```yaml
status:
  conditions:
  - type: Updated
    status: "True"
    reason: AllNodesUpdated
  - type: Updating
    status: "False"
  - type: Degraded
    status: "False"
  - type: NodeDegraded
    status: "False"
```

**Condition Types**:
- `Updated`: All nodes have current config
- `Updating`: Update in progress
- `Degraded`: Pool or nodes degraded
- `NodeDegraded`: One or more nodes degraded

## Common Patterns

### Pattern 1: Custom Pool for Special Workloads

```yaml
apiVersion: machineconfiguration.openshift.io/v1
kind: MachineConfigPool
metadata:
  name: infra
spec:
  machineConfigSelector:
    matchExpressions:
    - key: machineconfiguration.openshift.io/role
      operator: In
      values: [worker, infra]  # Include worker AND infra configs
  nodeSelector:
    matchLabels:
      node-role.kubernetes.io/infra: ""
  maxUnavailable: 1
```

**When to use**: Infrastructure nodes with different configuration than workers

See: [docs/custom-pools.md](../../../docs/custom-pools.md)

### Pattern 2: Pause Updates for Maintenance

```bash
# Pause pool
oc patch mcp/worker --type merge -p '{"spec":{"paused":true}}'

# Resume later
oc patch mcp/worker --type merge -p '{"spec":{"paused":false}}'
```

**When to use**: During maintenance windows, change freezes

### Pattern 3: Faster Updates for Non-Critical Pools

```yaml
spec:
  maxUnavailable: 5  # Update 5 nodes at once
```

**When to use**: Large worker pools where faster updates are acceptable

**Warning**: Higher risk of workload disruption

## State Machine

**States**:

- **All Updated**: `updatedMachineCount == machineCount`, all nodes have current config
- **Updating**: `updatedMachineCount < machineCount`, rollout in progress
- **Degraded**: `degradedMachineCount > 0`, one or more nodes failed
- **Paused**: `spec.paused == true`, updates stopped

**Transitions**:

- **All Updated → Updating**: New MachineConfig created/updated
- **Updating → All Updated**: Last node successfully updated
- **Updating → Degraded**: Node update fails, marked degraded
- **Any → Paused**: `spec.paused` set to `true`
- **Paused → (previous state)**: `spec.paused` set to `false`

## Monitoring Pool Status

```bash
# View all pools
oc get machineconfigpool

# Detailed status
oc describe machineconfigpool/worker

# Watch updates
watch oc get mcp
```

**Key fields to monitor**:
- **Updated**: Should be `True` when updates complete
- **Updating**: `True` during rollout
- **Degraded**: Should always be `False`
- **MachineCount**: Total nodes
- **UpdatedMachineCount**: Nodes with current config

## Related Concepts

- [MachineConfig](./machine-config.md) - Configuration applied to pool
- [Node](https://kubernetes.io/docs/concepts/architecture/nodes/) - Individual machines in pool
- [UpdateController](../design-docs/components/machine-config-controller.md) - Coordinates updates
- [RenderController](../design-docs/components/machine-config-controller.md) - Merges configs

## Implementation Details

- **Update Logic**: `pkg/controller/node/node_controller.go:syncMachineConfigPool()`
- **Rendering**: `pkg/controller/render/render_controller.go:syncMachineConfigPool()`
- **Status Updates**: Both controllers update pool status

## References

- [Product Docs](https://docs.openshift.com/container-platform/latest/post_installation_configuration/machine-configuration-tasks.html)
- [Custom Pools](../../../docs/custom-pools.md) - Creating custom pools
- [Legacy Docs](../../../docs/MachineConfigController.md) - MachineConfigPool details

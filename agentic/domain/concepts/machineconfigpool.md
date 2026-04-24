---
concept: MachineConfigPool
type: CRD
related: [MachineConfig, Node, MachineConfigDaemon]
---

# MachineConfigPool

## Definition

A MachineConfigPool groups nodes via label selectors and manages the rollout of MachineConfig changes to those nodes with controlled update semantics.

## Purpose

Enables targeted configuration for different node groups (e.g., master vs worker) with independent update policies and rollout control.

## Location in Code

- **API Definition**: vendor/github.com/openshift/api/machineconfiguration/v1/types.go
- **Controller**: pkg/controller/node/node_controller.go
- **Status Updates**: pkg/daemon/writer.go
- **Tests**: pkg/controller/node/node_controller_test.go

## Lifecycle

```
1. Created by installer or user (master, worker, custom pools)
2. Controller selects matching nodes via nodeSelector
3. Controller selects matching MachineConfigs via machineConfigSelector
4. Controller renders selected configs into rendered-<pool>-<hash>
5. Nodes update to rendered config (respecting maxUnavailable)
6. Pool status reflects update progress
```

## Key Fields / Properties

### spec.nodeSelector
**Type**: metav1.LabelSelector
**Purpose**: Selects which nodes belong to this pool
**Example**:
```yaml
nodeSelector:
  matchLabels:
    node-role.kubernetes.io/worker: ""
```

### spec.machineConfigSelector
**Type**: metav1.LabelSelector
**Purpose**: Selects which MachineConfigs apply to this pool
**Example**:
```yaml
machineConfigSelector:
  matchExpressions:
    - key: machineconfiguration.openshift.io/role
      operator: In
      values: [worker]
```

### spec.maxUnavailable
**Type**: int or string (percentage)
**Purpose**: Max nodes that can be updating simultaneously
**Example**: `1` or `"10%"`

### spec.paused
**Type**: bool
**Purpose**: Halt updates to this pool
**Example**: `true`

### status.configuration
**Type**: MachineConfigPoolStatus
**Purpose**: Current and desired rendered config
**Example**:
```yaml
status:
  configuration:
    name: rendered-worker-abc123
    source:
      - name: 00-worker
      - name: 99-custom
```

### status.conditions
**Type**: []Condition
**Purpose**: Update progress (Updated, Updating, Degraded)

## State Machine

```yaml
states:
  - Updated: All nodes at desired config
  - Updating: Rollout in progress
  - Degraded: Update failures detected

transitions:
  - Updated → Updating: New rendered config available
  - Updating → Updated: All nodes successfully updated
  - Updating → Degraded: Update failures exceed threshold
```

## Common Patterns

### Creating Custom Pool
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
        values: [worker, infra]
  nodeSelector:
    matchLabels:
      node-role.kubernetes.io/infra: ""
  maxUnavailable: 1
```

**When to use**: Separate configuration for infrastructure nodes

## Related Concepts

- [MachineConfig](./machineconfig.md) - Applied to pools
- [MachineConfigDaemon](../design-docs/components/machine-config-daemon.md) - Executes updates on nodes

## Implementation Details

- **Pool Controller**: pkg/controller/node/node_controller.go
- **Rendering**: pkg/controller/template/render.go
- **Status**: pkg/daemon/writer.go

## References

- [Upstream docs](https://docs.openshift.com/container-platform/latest/architecture/architecture-rhcos.html)
- [Design doc](../../../docs/MachineConfigController.md)

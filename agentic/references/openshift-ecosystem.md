# OpenShift Ecosystem Context

## Repository Category

**This repo is**: Core Platform Operator

**Justification**:
- Reports status via ClusterOperator CR (config.openshift.io/v1)
- Managed by cluster-version-operator
- Deployed during cluster installation
- Located in install/ manifests of CVO
- Critical for cluster operation (manages node OS)

## Related Operators and Components

### Direct Dependencies

| Component | Relationship | Purpose |
|-----------|-------------|---------|
| cluster-version-operator | Parent | Manages MCO lifecycle and upgrades |
| openshift/api | API definitions | Type definitions for MachineConfig, MachineConfigPool, etc. |
| openshift/library-go | Shared library | Operator patterns, resource syncing, status reporting |

### Peer Operators (for Pattern Examples)

| Operator | Similarity | What to Check |
|----------|-----------|---------------|
| cluster-node-tuning-operator | Node configuration | Similar DaemonSet pattern for node-level config |
| cluster-network-operator | Core operator | ClusterOperator status reporting pattern |
| cluster-storage-operator | Core operator | Operator lifecycle management patterns |

### Downstream Dependencies

| Component | Dependency on MCO | Why |
|-----------|------------------|-----|
| kubelet | MachineConfig | Kubelet configuration via MachineConfig |
| CRI-O | MachineConfig/ContainerRuntimeConfig | Container runtime configuration |
| NetworkManager | MachineConfig | Network configuration at boot |
| systemd | MachineConfig | System service management |

## Dependencies for Pattern Examples

### openshift/library-go

**When to check**: Implementing operator patterns

**Key packages**:
- `pkg/operator/resource/resourceapply` - Resource syncing and ownership
- `pkg/operator/events` - Event recording
- `pkg/operator/status` - ClusterOperator status reporting
- `pkg/operator/loglevel` - Log level configuration

**Example usage in MCO**:
- `pkg/operator/sync.go` - Uses resourceapply for sub-component deployment
- `pkg/operator/status.go` - ClusterOperator status management

### openshift/api

**When to check**: Understanding API types

**Key packages**:
- `machineconfiguration/v1` - MCO-specific APIs (MachineConfig, MachineConfigPool)
- `config/v1` - Cluster-wide config (Infrastructure, Network, ClusterOperator)

**Example usage in MCO**:
- `pkg/controller/template/render.go` - Uses Infrastructure and Network types
- `pkg/operator/status.go` - Uses ClusterOperator type

### Peer Operators for Similar Patterns

**cluster-node-tuning-operator** (https://github.com/openshift/cluster-node-tuning-operator):
- **Pattern**: DaemonSet running on nodes
- **Check for**: Node-level configuration application
- **Similar to**: MachineConfigDaemon

**cluster-network-operator** (https://github.com/openshift/cluster-network-operator):
- **Pattern**: ClusterOperator status reporting
- **Check for**: Status condition management
- **Similar to**: MachineConfigOperator status

## OpenShift vs Standalone Kubernetes

### What's OpenShift-Specific

- **MachineConfig API**: No Kubernetes equivalent
- **ClusterOperator status**: OpenShift core operator pattern
- **Managed by CVO**: Cluster-version-operator lifecycle
- **RHCOS**: OpenShift-specific OS (Fedora CoreOS upstream)

### What's Kubernetes-Native

- **Controller pattern**: Standard Kubernetes reconciliation loop
- **DaemonSet**: Standard Kubernetes workload type
- **RBAC**: Standard Kubernetes authorization
- **CRD usage**: Standard Kubernetes extension mechanism

## Finding Implementation Examples

**For OpenShift-specific patterns**:
1. Check `openshift/library-go` for shared patterns
2. Check peer core operators (cluster-network-operator, etc.)
3. Check OpenShift enhancements repository

**For Kubernetes patterns**:
1. Check controller-runtime documentation
2. Check Kubernetes sample-controller
3. Check kubebuilder documentation

**For MCO-specific patterns**:
1. Check existing controllers in `pkg/controller/`
2. Check ADRs in `agentic/decisions/`
3. Check design docs in `docs/`

## Related Documentation

- [Architecture](../ARCHITECTURE.md) - MCO component interactions
- [Design Philosophy](../DESIGN.md) - Core beliefs and patterns
- [OpenShift APIs](./openshift-apis.yaml) - API inventory

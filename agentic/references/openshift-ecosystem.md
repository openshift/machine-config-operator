# OpenShift Ecosystem Context

## Repository Category

**This repo is**: Core Platform Operator

**Core Operators**: Report to ClusterOperator, managed by cluster-version-operator (CVO)
**Ecosystem Operators**: Use OperatorCondition, installed via OLM

## Dependencies for Pattern Examples

### Where to Find Implementation Patterns

**For operator lifecycle patterns**, check:
- **openshift/library-go**: Standard patterns for OpenShift operators
  - Leader election: `pkg/operator/leaderelection/`
  - Event recording: `pkg/operator/events/`
  - Status reporting: `pkg/operator/status/`
  - Resource syncing: `pkg/operator/resource/`

**For similar operator implementations**, check:
- **openshift/cluster-kube-apiserver-operator**: Similar component lifecycle management
- **openshift/cluster-version-operator**: ClusterOperator status reporting patterns
- **openshift/cluster-etcd-operator**: Managing critical control plane components

**For API definitions**, check:
- **openshift/api**: All OpenShift CRD definitions
  - MachineConfiguration APIs: `machineconfiguration/v1/`
  - Config APIs: `config/v1/`
  - Operator APIs: `operator/v1/`

## Related Repositories

| Repository | Relationship | Purpose | What to Look For |
|------------|--------------|---------|------------------|
| openshift/api | Type definitions | MachineConfiguration CRDs | API field definitions, validation |
| openshift/library-go | Shared patterns | Operator utilities | Leader election, events, status |
| cluster-version-operator | Coordinates with | Manages MCO updates | ClusterOperator patterns |
| machine-api-operator | Peer | Provisions machines | Machine provisioning flow |
| cluster-kube-apiserver-operator | Peer | Kubelet CA rotation | Similar operator patterns |
| installer | Uses MCO | Cluster bootstrap | Bootstrap flow, initial configs |

## When to Check Each Repository

**Check openshift/api when**:
- Adding/modifying CRD fields
- Understanding API validation rules
- Looking up CRD documentation

**Check openshift/library-go when**:
- Implementing leader election
- Recording Kubernetes events
- Updating operator status
- Managing certificates

**Check cluster-version-operator when**:
- Understanding ClusterOperator status reporting
- Learning how operators are upgraded
- Understanding release image structure

**Check machine-api-operator when**:
- Understanding machine provisioning flow
- Seeing how machines join cluster
- Learning machine lifecycle

**Check cluster-kube-apiserver-operator when**:
- Similar operator lifecycle patterns
- Managing critical components
- Certificate management patterns

**Check installer when**:
- Understanding bootstrap process
- Learning initial cluster setup
- Seeing how MachineConfigs are used during install

## Node Annotation Patterns

MCO coordinates MCC and MCD via node annotations (standard OpenShift pattern):

```
machineconfiguration.openshift.io/currentConfig: "rendered-worker-abc123"
machineconfiguration.openshift.io/desiredConfig: "rendered-worker-def456"
machineconfiguration.openshift.io/state: "Done" | "Working" | "Degraded"
machineconfiguration.openshift.io/reason: "error message if degraded"
```

**Similar pattern used by**:
- cluster-node-tuning-operator (node tuning annotations)
- cluster-network-operator (network configuration annotations)

## ClusterOperator Status Pattern

MCO reports status via ClusterOperator (standard for core operators):

```yaml
apiVersion: config.openshift.io/v1
kind: ClusterOperator
metadata:
  name: machine-config
status:
  conditions:
  - type: Available
    status: "True" | "False"
  - type: Progressing
    status: "True" | "False"
  - type: Degraded
    status: "True" | "False"
  - type: Upgradeable
    status: "True" | "False"
```

**Similar pattern used by**: All core operators (kube-apiserver, etcd, network, etc.)

**Implementation**: `pkg/operator/status.go`

## DaemonSet Pattern

MCD runs as DaemonSet on all nodes (standard for node agents):

**Similar pattern used by**:
- cluster-node-tuning-operator (tuned daemonset)
- cluster-network-operator (ovn-kubernetes daemonset)
- openshift-sdn (network daemonset)

**Why**: Need one pod per node to manage node-local state

## ServiceAccount and RBAC Pattern

Each component has dedicated ServiceAccount with least-privilege RBAC:

**Similar pattern used by**: All OpenShift operators

**Best practices from library-go**:
- One ServiceAccount per component
- ClusterRole for cluster-scoped resources
- Role for namespaced resources
- Explicit permissions (no wildcards in production)

## Additional Resources

- [OpenShift Operator Development](https://docs.openshift.com/container-platform/latest/operators/operator_sdk/osdk-about.html)
- [library-go Patterns](https://github.com/openshift/library-go)
- [Enhancement Process](https://github.com/openshift/enhancements)

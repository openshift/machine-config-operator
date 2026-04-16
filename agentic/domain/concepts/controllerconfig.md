---
concept: ControllerConfig
type: CRD
related: [MachineConfigController, MachineConfig]
---

# ControllerConfig

## Definition

ControllerConfig is an OpenShift CRD that provides cluster-wide configuration to the MachineConfigController, including platform details, network configuration, and infrastructure information.

## Purpose

Supplies rendering context for MachineConfigs, allowing platform-specific and cluster-specific configuration to be injected during rendering.

## Location in Code

- **API Definition**: vendor/github.com/openshift/api/machineconfiguration/v1/types.go
- **Controller**: pkg/controller/controller_context.go
- **Usage**: pkg/controller/template/render.go
- **Tests**: pkg/controller/template/render_test.go

## Lifecycle

```
1. Created by installer during cluster bootstrap
2. Updated by machine-config-operator with infrastructure details
3. MachineConfigController watches for changes
4. Changes trigger re-rendering of all pools
5. New rendered configs pushed to pools
```

## Key Fields / Properties

### spec.cloudProviderConfig
**Type**: string
**Purpose**: Cloud provider configuration data
**Example**: AWS credentials, Azure config

### spec.clusterDNSIP
**Type**: string
**Purpose**: IP address of cluster DNS service
**Example**: `172.30.0.10`

### spec.platform
**Type**: string
**Purpose**: Platform type (AWS, Azure, GCP, BareMetal, etc.)
**Example**: `AWS`

### spec.etcdDiscoveryDomain
**Type**: string
**Purpose**: Domain for etcd member discovery
**Example**: `cluster.local`

### spec.kubeAPIServerServingCAData
**Type**: []byte
**Purpose**: CA bundle for kube-apiserver
**Example**: PEM-encoded CA certificate

### spec.rootCAData
**Type**: []byte
**Purpose**: Root CA bundle
**Example**: PEM-encoded root CAs

### spec.pullSecret
**Type**: corev1.ObjectReference
**Purpose**: Reference to pull secret for container images
**Example**: `{"name": "pull-secret", "namespace": "openshift-config"}`

### spec.infra
**Type**: configv1.Infrastructure
**Purpose**: Cluster infrastructure details
**Example**: Platform-specific configuration

### spec.network
**Type**: configv1.Network
**Purpose**: Cluster network configuration
**Example**: Pod/service CIDRs, network type

## State Machine

```yaml
states:
  - Initializing: Created by installer
  - Active: Providing config to controllers
  - Updating: Infrastructure changes detected

transitions:
  - Initializing → Active: All required fields populated
  - Active → Updating: Infrastructure or network changes
  - Updating → Active: Controllers reconciled
```

## Common Patterns

### Used in Rendering Context
```go
// pkg/controller/template/render.go
func generateMachineConfig(
  controllerConfig *mcfgv1.ControllerConfig,
  machinePool *mcfgv1.MachineConfigPool,
  // ...
) (*ign3types.Config, error) {
  // Uses controllerConfig for platform-specific rendering
}
```

**When to use**: Controller needs cluster context for rendering

## Related Concepts

- [MachineConfigController](../design-docs/components/machine-config-controller.md) - Consumes ControllerConfig
- [MachineConfig](./machineconfig.md) - Rendered using ControllerConfig context

## Implementation Details

- **Creation**: pkg/operator/bootstrap.go
- **Updates**: pkg/operator/sync.go
- **Consumption**: pkg/controller/template/render.go

## References

- [Design doc](../../../docs/MachineConfigController.md)

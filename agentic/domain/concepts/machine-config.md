---
concept: MachineConfig
type: CRD
related: [MachineConfigPool, Ignition, RenderController]
enhancement: "openshift/enhancements#various"
---

# MachineConfig

## Definition

MachineConfig is a Kubernetes Custom Resource that defines declarative operating system configuration for OpenShift nodes. It wraps Ignition configuration in a Kubernetes-native API, enabling OS management through standard Kubernetes workflows (GitOps, RBAC, versioning).

## Purpose

Enables cluster administrators to configure operating systems declaratively:
- Write configuration files to nodes (e.g., `/etc/my-config.conf`)
- Create/enable/configure systemd units
- Set user SSH keys (limited scope: core user only)
- Configure OS-level settings (sysctl, modules, etc.)

Why this exists: Traditional OS configuration (SSH + manual edits) doesn't scale, isn't auditable, and causes config drift. MachineConfig brings OS configuration into Kubernetes declarative model.

## Location in Code

- **API Definition**: `vendor/github.com/openshift/api/machineconfiguration/v1/types.go:MachineConfig`
- **Controller**: `pkg/controller/render/render_controller.go` (merges configs)
- **Application**: `pkg/daemon/update.go` (applies to nodes)
- **Validation**: `pkg/apihelpers/validate.go`
- **Tests**: `pkg/controller/render/*_test.go`

## Lifecycle

```
1. User creates MachineConfig
   ↓
2. RenderController discovers all MachineConfigs for pool (via label selector)
   ↓
3. RenderController merges configs lexicographically → rendered-{pool}-{hash}
   ↓
4. UpdateController sets desiredConfig annotation on nodes
   ↓
5. MachineConfigDaemon applies config (files, units, OS update)
   ↓
6. MCD sets currentConfig=desiredConfig after successful application
```

## Key Fields / Properties

### metadata.name

**Type**: string
**Purpose**: Unique identifier, determines merge order (lexicographic)
**Example**:
```yaml
metadata:
  name: 99-worker-custom-sysctl  # Higher numbers override lower numbers
```

**Convention**: `NN-{role}-{purpose}` where NN is 00-99
- `00-*`: OpenShift-generated (templates)
- `01-98`: OpenShift-generated (from controllers)
- `99-*`: User-created

### metadata.labels

**Type**: map[string]string
**Purpose**: Selectors for MachineConfigPool matching
**Example**:
```yaml
metadata:
  labels:
    machineconfiguration.openshift.io/role: worker  # Matches worker pool
```

**Required label**: `machineconfiguration.openshift.io/role: {pool-name}`

### spec.config (Ignition)

**Type**: Ignition v3 config
**Purpose**: Declarative OS configuration
**Example**:
```yaml
spec:
  config:
    ignition:
      version: 3.2.0
    storage:
      files:
      - path: /etc/sysctl.d/99-custom.conf
        mode: 0644
        contents:
          source: data:,vm.max_map_count%3D262144
    systemd:
      units:
      - name: my-service.service
        enabled: true
        contents: |
          [Unit]
          Description=My Service
          [Service]
          ExecStart=/usr/bin/my-binary
```

See [Ignition concept](./ignition.md) for details.

### spec.osImageURL

**Type**: string (container image pull spec with digest)
**Purpose**: OS image for rpm-ostree updates
**Example**:
```yaml
spec:
  osImageURL: quay.io/openshift-release-dev/ocp-v4.0-art-dev@sha256:abc123...
```

**Note**: Typically set by cluster-version-operator, not manually. Must use digest (@sha256:...) not tag (:latest).

### spec.kernelArguments

**Type**: []string
**Purpose**: Kernel boot arguments (added to GRUB config)
**Example**:
```yaml
spec:
  kernelArguments:
  - nosmt  # Disable simultaneous multithreading
  - console=ttyS0
```

**Note**: Requires reboot to take effect.

### spec.fips

**Type**: bool
**Purpose**: Enable FIPS mode (Federal Information Processing Standards)
**Example**:
```yaml
spec:
  fips: true
```

**Note**: Cluster-wide setting, requires cluster reinstall to change.

## Common Patterns

### Pattern 1: Add Configuration File

```yaml
apiVersion: machineconfiguration.openshift.io/v1
kind: MachineConfig
metadata:
  name: 99-worker-custom-config
  labels:
    machineconfiguration.openshift.io/role: worker
spec:
  config:
    ignition:
      version: 3.2.0
    storage:
      files:
      - path: /etc/my-app/config.yaml
        mode: 0644
        contents:
          source: data:,key%3Avalue%0Aother%3Avalue
```

**When to use**: Adding configuration files to nodes

**Note**: Content must be URL-encoded in `data:,` source

### Pattern 2: Enable systemd Unit

```yaml
apiVersion: machineconfiguration.openshift.io/v1
kind: MachineConfig
metadata:
  name: 99-worker-enable-service
  labels:
    machineconfiguration.openshift.io/role: worker
spec:
  config:
    ignition:
      version: 3.2.0
    systemd:
      units:
      - name: my-service.service
        enabled: true
```

**When to use**: Enabling existing systemd units

### Pattern 3: Configure sysctl

```yaml
apiVersion: machineconfiguration.openshift.io/v1
kind: MachineConfig
metadata:
  name: 99-worker-sysctl
  labels:
    machineconfiguration.openshift.io/role: worker
spec:
  config:
    ignition:
      version: 3.2.0
    storage:
      files:
      - path: /etc/sysctl.d/99-custom.conf
        mode: 0644
        contents:
          source: data:,vm.max_map_count%3D262144
```

**When to use**: Tuning kernel parameters

**Note**: Changes apply after reboot

## Related Concepts

- [MachineConfigPool](./machine-config-pool.md) - Groups of nodes with shared config
- [Ignition](./ignition.md) - Configuration format used within MachineConfig
- [RenderController](../design-docs/components/machine-config-controller.md) - Merges MachineConfigs
- [UpdateController](../design-docs/components/machine-config-controller.md) - Coordinates application

## Implementation Details

- **Merging Logic**: `pkg/controller/render/render_controller.go:MergeMachineConfigs()`
- **Validation**: `pkg/apihelpers/validate.go:ValidateMachineConfig()`
- **Application**: `pkg/daemon/update.go:reconcilable()`

## References

- [ADR](../../decisions/adr-0001-use-ignition-for-configuration.md) - Why Ignition format
- [Product Docs](https://docs.openshift.com/container-platform/latest/post_installation_configuration/machine-configuration-tasks.html)
- [Legacy Docs](../../../docs/MachineConfig.md) - Additional details

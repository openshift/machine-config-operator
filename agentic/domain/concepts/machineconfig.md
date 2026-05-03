---
concept: MachineConfig
type: CRD
related: [MachineConfigPool, Ignition, ControllerConfig]
---

# MachineConfig

## Definition

A MachineConfig is an OpenShift CRD that provides declarative specification for node configuration, including Ignition config, FIPS mode, kernel arguments, OS image, and extensions.

## Purpose

MachineConfigs enable cluster-managed OS configuration and updates, making the operating system "just another Kubernetes component" manageable via `oc` commands.

## Location in Code

- **API Definition**: vendor/github.com/openshift/api/machineconfiguration/v1/types.go
- **Rendering Logic**: pkg/controller/template/render.go
- **Application**: pkg/daemon/update.go
- **Tests**: pkg/controller/template/render_test.go

## Lifecycle

```
1. Created by user or platform controller
2. MachineConfigController selects applicable configs for each pool
3. Controller renders configs into single "rendered-<pool>-<hash>" config
4. MachineConfigDaemon on each node detects new rendered config
5. Daemon applies configuration (files, units, OS image)
6. Node reboots if required
7. Status updated to reflect current config
```

## Key Fields / Properties

### spec.config
**Type**: Ignition v3 config
**Purpose**: Defines files, systemd units, users, storage
**Example**:
```yaml
config:
  ignition:
    version: 3.2.0
  storage:
    files:
      - path: /etc/my-config
        contents:
          source: data:,hello%20world
```

### spec.osImageURL
**Type**: string
**Purpose**: Container image URL for OS update
**Example**: `quay.io/openshift-release-dev/ocp-v4.0-art-dev@sha256:...`

### spec.kernelArguments
**Type**: []string
**Purpose**: Kernel command-line arguments
**Example**: `["nosmt", "audit=1"]`

### spec.fips
**Type**: bool
**Purpose**: Enable FIPS 140-2 mode
**Example**: `true`

### spec.extensions
**Type**: []string
**Purpose**: Additional software packages to layer
**Example**: `["kernel-devel", "usbguard"]`

## State Machine

```yaml
states:
  - Created: MachineConfig object exists
  - Selected: Included in pool rendering
  - Rendered: Merged into rendered-<pool>-<hash>
  - Applied: Configuration active on nodes

transitions:
  - Created → Selected: Matches pool's machineConfigSelector
  - Selected → Rendered: Controller merges all selected configs
  - Rendered → Applied: Daemon applies to node
```

## Common Patterns

### Adding a System File
```yaml
apiVersion: machineconfiguration.openshift.io/v1
kind: MachineConfig
metadata:
  name: 99-custom-file
  labels:
    machineconfiguration.openshift.io/role: worker
spec:
  config:
    ignition:
      version: 3.2.0
    storage:
      files:
        - path: /etc/my-app.conf
          mode: 0644
          contents:
            source: data:,key%3Dvalue
```

**When to use**: Adding configuration files to nodes

### Adding a Systemd Unit
```yaml
apiVersion: machineconfiguration.openshift.io/v1
kind: MachineConfig
metadata:
  name: 99-custom-service
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
          contents: |
            [Unit]
            Description=My Service
            [Service]
            ExecStart=/usr/local/bin/my-service
            [Install]
            WantedBy=multi-user.target
```

**When to use**: Running custom services on nodes

## Rendered Configs

Rendered MachineConfigs (prefix: `rendered-`) are the immutable result of merging all applicable MachineConfigs for a pool:

**Location**: pkg/controller/template/render.go
**Naming**: `rendered-<pool>-<hash>`
**Purpose**: Provides single source of truth for pool configuration
**Key Property**: All remote content is fetched and embedded (no dynamic references)

## Related Concepts

- [MachineConfigPool](./machineconfigpool.md) - Groups nodes and selects MachineConfigs
- [Ignition](./ignition.md) - Configuration format used in spec.config
- [ControllerConfig](./controllerconfig.md) - Provides rendering context

## Implementation Details

- **Rendering Logic**: pkg/controller/template/render.go
- **Validation**: pkg/controller/common/helpers.go
- **Application**: pkg/daemon/update.go

## References

- [ADR-0003: Ignition as Configuration Format](../../decisions/adr-0003-ignition-config-format.md)
- [Upstream docs](https://docs.openshift.com/container-platform/latest/post_installation_configuration/machine-configuration-tasks.html)
- [Design doc](../../../docs/MachineConfig.md)

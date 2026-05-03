---
concept: Ignition
type: Configuration Format
related: [MachineConfig, MachineConfigServer, RHCOS]
---

# Ignition

## Definition

Ignition is a CoreOS provisioning utility that runs only on first boot to manipulate disks, create users, write files, and configure systemd units.

## Purpose

Provides declarative, immutable first-boot configuration for RHCOS nodes, separating provisioning from runtime configuration management.

## Location in Code

- **Generation**: pkg/server/api.go
- **Conversion**: pkg/controller/common/conversion.go
- **Validation**: vendor/github.com/coreos/ignition/v2
- **Serving**: pkg/server/server.go

## Lifecycle

```
1. MachineConfigServer generates Ignition from rendered config
2. Node boots and retrieves Ignition via HTTPS
3. Ignition runs (once, at first boot)
4. Ignition writes files, creates users, formats disks
5. Ignition enables systemd units
6. Ignition never runs again (immutable first boot)
7. MachineConfigDaemon handles runtime updates
```

## Key Sections

### storage
**Purpose**: Define filesystems, files, directories, links
**Example**:
```json
{
  "storage": {
    "files": [{
      "path": "/etc/hostname",
      "contents": { "source": "data:,node1" },
      "mode": 420
    }]
  }
}
```

### systemd
**Purpose**: Define and enable systemd units
**Example**:
```json
{
  "systemd": {
    "units": [{
      "name": "example.service",
      "enabled": true,
      "contents": "[Service]\nType=oneshot\n..."
    }]
  }
}
```

### passwd
**Purpose**: Define users and SSH keys
**Example**:
```json
{
  "passwd": {
    "users": [{
      "name": "core",
      "sshAuthorizedKeys": ["ssh-rsa AAAA..."]
    }]
  }
}
```

## Ignition Version

RHCOS uses **Ignition v3.2.0**
- Spec: https://coreos.github.io/ignition/configuration-v3_2/
- Location in code: pkg/server/api.go (version constant)

## Common Patterns

### File with Inline Content
```yaml
config:
  ignition:
    version: 3.2.0
  storage:
    files:
      - path: /etc/myconfig
        mode: 0644
        contents:
          source: data:,hello%20world
```

### File from URL
```yaml
storage:
  files:
    - path: /etc/remote-config
      contents:
        source: https://example.com/config
        verification:
          hash: sha512-abc...
```

**Note**: Remote content is fetched and embedded at render time (pkg/controller/template/render.go)

## Related Concepts

- [MachineConfig](./machineconfig.md) - Embeds Ignition in spec.config
- [MachineConfigServer](../design-docs/components/machine-config-server.md) - Serves Ignition to nodes
- [RHCOS](./rhcos.md) - Runs Ignition at first boot

## Implementation Details

- **Generation**: pkg/server/api.go
- **Conversion to v3**: pkg/controller/common/conversion.go
- **Serving**: pkg/server/server.go

## References

- [ADR-0003: Ignition as Configuration Format](../../decisions/adr-0003-ignition-config-format.md)
- [Ignition spec](https://coreos.github.io/ignition/)
- [Design doc](../../../docs/MachineConfigServer.md)

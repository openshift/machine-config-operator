---
concept: rpm-ostree
type: Package System
related: [RHCOS, MachineConfigDaemon, OS Updates]
---

# rpm-ostree

## Definition

rpm-ostree is a hybrid image/package system combining libostree (git-like OS versioning) with RPM, enabling atomic, bootable OS updates with automatic rollback.

## Purpose

Provides transactional OS updates where the entire OS is treated as an immutable image, allowing safe updates with automatic rollback on failure.

## Location in Code

- **Integration**: pkg/daemon/rpm-ostree.go
- **Upgrade Logic**: pkg/daemon/pivot.go
- **Rebase**: pkg/daemon/daemon.go (RunDaemon -> updateOSAndReboot)
- **Tests**: pkg/daemon/rpm-ostree_test.go

## Lifecycle

```
1. MachineConfigDaemon detects new osImageURL
2. Daemon invokes rpm-ostree rebase to new image
3. rpm-ostree downloads new image
4. rpm-ostree stages new image for next boot
5. Node reboots
6. New image becomes active (atomically)
7. If failure, automatic rollback to previous image
```

## Key Operations

### rpm-ostree status
**Purpose**: Show current and staged deployments
**Example**:
```
State: idle
Deployments:
● pivot://quay.io/openshift-release-dev/ocp-v4@sha256:abc123
    Version: 414.92.202401...
  pivot://quay.io/openshift-release-dev/ocp-v4@sha256:def456
    Version: 414.92.202312...
```

### rpm-ostree rebase
**Purpose**: Switch to new OS image
**Code**: pkg/daemon/rpm-ostree.go (NodeUpdaterClient.Rebase)
**Example**: `rpm-ostree rebase --experimental pivot://quay.io/...@sha256:...`

### rpm-ostree ex apply-live
**Purpose**: Apply changes without reboot (limited use cases)
**Code**: pkg/daemon/update.go (applyOSChanges)

### rpm-ostree rollback
**Purpose**: Revert to previous deployment
**Code**: Automatic on boot failure

## State Machine

```yaml
states:
  - Idle: No pending updates
  - Downloading: Fetching new image
  - Staged: New image ready for next boot
  - Rebooting: Applying staged image
  - Active: New image booted successfully
  - Rollback: Previous image restored on failure

transitions:
  - Idle → Downloading: New osImageURL detected
  - Downloading → Staged: Image download complete
  - Staged → Rebooting: Reboot initiated
  - Rebooting → Active: Successful boot
  - Rebooting → Rollback: Boot failure
```

## Common Patterns

### OS Image Update
```yaml
apiVersion: machineconfiguration.openshift.io/v1
kind: MachineConfig
metadata:
  name: 99-new-os-version
spec:
  osImageURL: quay.io/openshift-release-dev/ocp-v4@sha256:newversion
```

**When to use**: Updating OS version across cluster
**Code**: pkg/daemon/pivot.go (RebaseLayered)

### Layering Additional Packages
```yaml
spec:
  extensions:
    - kernel-devel
    - usbguard
```

**Code**: pkg/daemon/rpm-ostree.go (Install function)

## Related Concepts

- [RHCOS](./rhcos.md) - Uses rpm-ostree as update mechanism
- [MachineConfigDaemon](../design-docs/components/machine-config-daemon.md) - Invokes rpm-ostree
- [OS Upgrades](../workflows/os-upgrade.md) - Workflow using rpm-ostree

## Implementation Details

- **Client**: pkg/daemon/rpm-ostree.go (NodeUpdaterClient)
- **Pivoting**: pkg/daemon/pivot.go
- **Daemon Integration**: pkg/daemon/daemon.go

## References

- [ADR-0001: Use rpm-ostree for OS Updates](../../decisions/adr-0001-use-rpm-ostree.md)
- [rpm-ostree docs](https://coreos.github.io/rpm-ostree/)
- [Design doc](../../../docs/OSUpgrades.md)

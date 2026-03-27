---
id: ADR-0002
title: Use rpm-ostree for OS Updates
date: 2025-03-25
status: accepted
deciders: [machine-config-operator team]
supersedes: null
superseded-by: null
---

# Use rpm-ostree for OS Updates

## Status

Accepted (implemented)

## Context

OpenShift nodes need a reliable way to update the operating system while minimizing risk:
- OS updates must be atomic (either fully succeed or fully roll back)
- Updates should not break running nodes
- Need ability to roll back if issues are detected
- Updates must be coordinated across the cluster

Traditional package managers (yum/dnf) update packages individually, creating opportunity for partial/broken state.

## Decision

Use rpm-ostree for all OS updates on Red Hat CoreOS nodes. OS updates are delivered as container images containing full OSTree commits, applied atomically via rpm-ostree.

## Rationale

### Why This?

- **Atomic updates**: Either entire OS updates or nothing (no partial state)
- **Automatic rollback**: If new deployment fails to boot, system rolls back to previous
- **Image-based**: OS packaged as container image, versioned immutably
- **Transactional**: Uses OSTree for filesystem-level transactions
- **Tested deployments**: New OS staged as bootloader entry before reboot
- **A/B system**: Can keep multiple OS versions, boot into either

### Why Not Alternatives?

- **yum/dnf package updates**: Updates packages individually, can leave system in broken state
  - Rejected: Not atomic, no automatic rollback

- **Full disk image replacement**: Replace entire disk with new image
  - Rejected: Destroys data, no rollback, too heavyweight

- **Container-based updates** (without rpm-ostree): Run OS in container
  - Rejected: Still need host OS to run containers, doesn't solve the problem

## Consequences

### Positive

- ✅ Atomic updates prevent broken intermediate states
- ✅ Automatic rollback on boot failure (no manual intervention needed)
- ✅ Can verify new OS before finalizing (kargs, etc.)
- ✅ Integrates with OSTree for efficient storage (deduplication)
- ✅ Aligned with Red Hat CoreOS architecture

### Negative

- ❌ Only works with rpm-ostree-based systems (Red Hat CoreOS)
- ❌ Requires reboot to activate new OS (cannot avoid for kernel updates)
- ❌ Updates entire OS, not individual packages (larger download)
- ❌ Cannot use traditional `yum install` for packages (by design)

### Neutral

- ℹ️ OS images distributed as containers (pulled from registry)
- ℹ️ OSImageURL in MachineConfig points to container image
- ℹ️ rpm-ostree handles conversion from container image to OSTree commit

## Implementation

- **Location**:
  - rpm-ostree operations: `pkg/daemon/rpm_ostree.go`
  - OS update orchestration: `pkg/daemon/update.go:UpdateOS()`
  - OSImageURL tracking: MachineConfig `.spec.osImageURL` field

- **How it works**:
  1. Cluster-version-operator updates release image with new OS container image
  2. MCO detects new OSImageURL in release payload
  3. TemplateController updates MachineConfigs with new OSImageURL
  4. MCD calls `rpm-ostree rebase` with new container image
  5. rpm-ostree creates new deployment (bootloader entry)
  6. MCD reboots node
  7. Node boots into new OS
  8. MCD validates OS version matches expected

- **Validation**:
  - `rpm-ostree status` checked after reboot
  - If booted OS doesn't match expected, node marked Degraded

## Alternatives Considered

### Alternative 1: Traditional Package Updates (yum/dnf)

**Pros**:
- Familiar to sysadmins
- Can update individual packages
- No reboot required for non-kernel updates

**Cons**:
- Not atomic (can leave system in broken state mid-update)
- No automatic rollback
- Cannot guarantee reproducible state across nodes
- Conflicts with immutable infrastructure philosophy

**Why rejected**: Lack of atomicity and rollback makes it too risky for production clusters

### Alternative 2: Full Disk Image Replacement

**Pros**:
- Simplest conceptually
- Guaranteed fresh state

**Cons**:
- Loses all node-local data and state
- No rollback mechanism
- Extremely heavyweight (full disk write)
- Requires re-provisioning node

**Why rejected**: Too disruptive, destroys state, no rollback

## References

- [rpm-ostree Project](https://github.com/coreos/rpm-ostree)
- [OSTree Documentation](https://ostreedev.github.io/ostree/)
- [MCO OS Upgrades Documentation](../../docs/OSUpgrades.md)
- [Red Hat CoreOS](https://docs.openshift.com/container-platform/latest/architecture/architecture-rhcos.html)

## Notes

- rpm-ostree is a hybrid system: uses RPM for packages, OSTree for delivery
- OS container images are built by CoreOS pipeline, included in OpenShift release images
- MCO calls rpm-ostree via CLI (not library), parses JSON output
- Layering (OCP 4.12+) allows adding packages on top of base OS via Dockerfile/Containerfile

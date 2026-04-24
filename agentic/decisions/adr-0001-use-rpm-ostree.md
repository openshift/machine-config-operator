---
id: ADR-0001
title: Use rpm-ostree for OS Updates
date: 2020-01-15
status: accepted
deciders: [OpenShift MCO Team]
supersedes: null
superseded-by: null
enhancement-refs:
  - repo: "openshift/enhancements"
    number: 201
    title: "Machine Config Operator OS Upgrades"
---

# Use rpm-ostree for OS Updates

## Status

Accepted (implemented)

## Context

OpenShift 4 needed a mechanism to update the operating system on nodes in a safe, atomic manner that integrates with cluster lifecycle management. Traditional package managers (yum/dnf) provide individual package updates but lack:

- Atomic updates (all-or-nothing)
- Automatic rollback on failure
- Image-based versioning
- Transactional guarantees

RHCOS is based on CoreOS which uses rpm-ostree, an established technology combining libostree (git-like OS snapshots) with RPM packages.

## Decision

Use rpm-ostree as the primary mechanism for operating system updates in OpenShift nodes running RHCOS.

## Rationale

### Why This?
- **Atomic updates**: Entire OS treated as immutable image, no partial update states
- **Automatic rollback**: Boot fails → previous image automatically restored
- **Image-based**: OS versions are container images, versioned and signed
- **Proven technology**: CoreOS/Fedora CoreOS production-tested
- **RPM compatibility**: Supports layering additional RPM packages when needed

### Why Not Alternatives?
- **Traditional package managers (yum/dnf)**: No atomic updates, complex dependency resolution at runtime, harder to reproduce states
- **Container-based root**: More radical departure from RHEL tooling, harder adoption for enterprise
- **Custom image builder**: Reinventing existing, tested technology

## Consequences

### Positive
- ✅ OS updates are transactional and safe
- ✅ Failed updates automatically roll back
- ✅ OS state is reproducible (image SHA)
- ✅ Integration with container registry infrastructure
- ✅ Layering support for additional packages

### Negative
- ❌ Updates require node reboot (cannot update running system)
- ❌ Root filesystem is read-only (must use /etc or /var for changes)
- ❌ Additional complexity vs traditional RHEL updates

### Neutral
- ℹ️ Requires understanding of ostree concepts
- ℹ️ Different mental model from traditional Linux distributions

## Implementation

- **Location**: pkg/daemon/rpm-ostree.go (client wrapper), pkg/daemon/pivot.go (upgrade orchestration)
- **Migration**: Existing from OpenShift 4.0, no migration needed
- **Validation**: Nodes use rpm-ostree status to report deployment state

**Key functions**:
- `pkg/daemon/rpm-ostree.go:Rebase()` - Switch to new OS image
- `pkg/daemon/pivot.go:RebaseLayered()` - Upgrade with layered packages
- `pkg/daemon/update.go:updateOSAndReboot()` - Orchestrate update and reboot

## Alternatives Considered

### Alternative 1: Traditional Package Manager (yum/dnf)
**Pros**: Familiar to RHEL users, fine-grained package control
**Cons**: No atomic updates, complex state management, dependency resolution issues
**Why rejected**: Lacks safety guarantees required for automated cluster updates

### Alternative 2: Full Container-Based Root Filesystem
**Pros**: Ultimate immutability, consistent with containerized workloads
**Cons**: Too radical departure from RHEL, difficult troubleshooting, limited OS tools
**Why rejected**: Too different from RHEL operational model, adoption barrier

### Alternative 3: Image Builder + dd/imaging
**Pros**: Simple concept, full control
**Cons**: Slow updates, no rollback, requires reimaging entire disk
**Why rejected**: Too slow, no safety mechanism, poor operational experience

## References

- [rpm-ostree documentation](https://coreos.github.io/rpm-ostree/)
- [OS Upgrades design doc](../../docs/OSUpgrades.md)
- [rpm-ostree concept](../domain/concepts/rpm-ostree.md)
- [Fedora CoreOS](https://docs.fedoraproject.org/en-US/fedora-coreos/)

## Notes

This decision predates the MCO repository and was made during the OpenShift 4 architecture design phase. The decision has proven sound with millions of node updates performed safely across OpenShift clusters worldwide.

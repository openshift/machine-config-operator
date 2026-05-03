---
id: ADR-0003
title: Ignition as Configuration Format
date: 2020-01-10
status: accepted
deciders: [OpenShift MCO Team]
supersedes: null
superseded-by: null
enhancement-refs: []
upstream-kep: null
---

# Ignition as Configuration Format

## Status

Accepted (implemented)

## Context

OpenShift needs a way to configure nodes at first boot before Kubernetes components are running. The configuration format must:

- Support provisioning disks, files, users, and systemd units
- Be declarative and immutable
- Work at first boot (before any cluster services are available)
- Integrate with RHCOS (CoreOS-based operating system)
- Be validated and deterministic

Cloud-init is a common alternative used in many Linux distributions, but RHCOS is based on CoreOS which uses Ignition.

## Decision

Use Ignition (v3.2.0) as the configuration format for first-boot provisioning of RHCOS nodes in OpenShift clusters.

## Rationale

### Why This?
- **RHCOS native**: RHCOS already includes Ignition, no additional components needed
- **First-boot only**: Runs once at initial boot, immutable provisioning
- **Declarative**: JSON/YAML schema, machine-readable and validatable
- **Secure**: Supports file verification (checksums), HTTPS with certificate validation
- **Deterministic**: Same config produces same result every time
- **Disk manipulation**: Can partition disks, format filesystems, create RAID arrays
- **Upstream support**: Maintained by CoreOS/Fedora CoreOS community

### Why Not Alternatives?
- **cloud-init**: Not included in RHCOS, different operational model (multi-run vs single-run), less deterministic
- **Custom init scripts**: Difficult to validate, error-prone, not portable
- **Ansible**: Requires Python runtime, complex dependencies, not first-boot focused

## Consequences

### Positive
- ✅ Consistent with RHCOS/CoreOS ecosystem
- ✅ Validated configuration schema (compile-time errors vs runtime failures)
- ✅ Immutable first-boot (no unexpected changes after initial provisioning)
- ✅ Secure content fetching (HTTPS, checksums)
- ✅ Well-tested in Fedora CoreOS, OpenShift 4

### Negative
- ❌ Only runs at first boot (runtime changes require MachineConfigDaemon)
- ❌ JSON/YAML syntax less familiar than shell scripts
- ❌ Limited to first-boot provisioning (not runtime configuration management)

### Neutral
- ℹ️ Requires understanding Ignition schema
- ℹ️ Different from cloud-init (learning curve for users familiar with cloud-init)

## Implementation

- **Location**: 
  - Ignition generation: pkg/server/api.go
  - Ignition serving: pkg/server/server.go
  - Conversion from MachineConfig: pkg/controller/common/conversion.go
- **Validation**: vendor/github.com/coreos/ignition/v2 (upstream library)
- **Status**: Implemented since OpenShift 4.0

**Key functions**:
- `pkg/server/api.go:getAppendix()` - Generate Ignition from rendered MachineConfig
- `pkg/server/server.go:handleConfig()` - Serve Ignition via HTTPS to nodes
- `pkg/controller/common/conversion.go:ConvertRawExtToIgn()` - Convert v2 to v3 Ignition

## Alternatives Considered

### Alternative 1: cloud-init
**Pros**: Widely used in Linux distributions, familiar to many users, supports multiple cloud providers
**Cons**: Not included in RHCOS (would require adding), multi-run model (less deterministic), weaker validation
**Why rejected**: Not aligned with CoreOS/RHCOS design, additional dependency, less deterministic

### Alternative 2: Custom Bash Init Scripts
**Pros**: Extremely flexible, familiar shell syntax
**Cons**: No validation, error-prone, hard to test, not portable, no schema
**Why rejected**: Lacks safety guarantees, difficult to validate correctness

### Alternative 3: Ansible Playbooks
**Pros**: Mature configuration management, large ecosystem, reusable roles
**Cons**: Requires Python and Ansible runtime, complex dependencies, not designed for first-boot, slower
**Why rejected**: Too heavyweight for first-boot provisioning, operational overhead

## References

- [Ignition specification](https://coreos.github.io/ignition/)
- [Ignition concept doc](../domain/concepts/ignition.md)
- [MachineConfigServer design doc](../../docs/MachineConfigServer.md)
- [RHCOS documentation](https://docs.openshift.com/container-platform/latest/architecture/architecture-rhcos.html)

## Notes

Ignition's single-run, immutable model complements MachineConfigDaemon's ongoing reconciliation. Ignition handles first-boot provisioning; MCD handles runtime updates. This separation of concerns provides both immutable first boot and flexible runtime updates.

---
id: ADR-0001
title: Use Ignition for OS Configuration
date: 2025-03-25
status: accepted
deciders: [machine-config-operator team]
supersedes: null
superseded-by: null
enhancement-refs:
  - repo: "coreos/ignition"
    title: "Ignition v2/v3 Specification"
---

# Use Ignition for OS Configuration

## Status

Accepted (implemented in initial MCO design)

## Context

The machine-config-operator needs a declarative, validated way to configure operating systems on OpenShift nodes. This includes:
- Writing configuration files to disk
- Creating/enabling systemd units
- Managing users and SSH keys (limited scope)

Traditional approaches like bash scripts or configuration management tools (Ansible, Puppet) are either:
- Imperative (bash scripts)
- Complex/heavyweight (Ansible, Puppet)
- Difficult to validate before execution

CoreOS developed Ignition specifically for immutable infrastructure, designed to run once at provision time with strong validation guarantees.

## Decision

Use CoreOS Ignition as the configuration format for MachineConfig resources. All OS configuration is expressed as Ignition JSON/YAML and applied either:
- At first boot (via machine-config-server)
- In-place (via machine-config-daemon for supported changes)

## Rationale

### Why This?

- **Declarative**: Ignition configs describe desired state, not imperative steps
- **Validated**: Ignition validates syntax at parse time, before applying to nodes
- **Idempotent**: Safe to apply multiple times (though MCO only applies once per config)
- **Atomic**: File writes use rename-over for atomicity
- **Immutable-first**: Designed for systems where `/usr` is read-only
- **OpenShift standard**: All CoreOS-based OpenShift nodes use Ignition

### Why Not Alternatives?

- **Bash scripts in cloud-init**: Imperative, no validation, error-prone
  - Rejected: Cannot validate before execution, debugging is difficult

- **Ansible/Puppet**: Too heavyweight, requires agent, not atomic
  - Rejected: Adds complexity, not designed for immutable systems

- **Custom config format**: Reinventing the wheel
  - Rejected: Ignition already exists, tested, and maintained by CoreOS team

## Consequences

### Positive

- ✅ Strong validation before applying configs (catches errors early)
- ✅ Declarative model aligns with Kubernetes philosophy
- ✅ Reuses battle-tested CoreOS technology
- ✅ Natural fit for Red Hat CoreOS (the OS used by OpenShift)
- ✅ JSON schema validation available

### Negative

- ❌ Learning curve for Ignition syntax (different from cloud-init)
- ❌ Not all Ignition features are supported in-place (only files/units, not users/disks)
- ❌ Configuration must fit Ignition data model (cannot express arbitrary operations)

### Neutral

- ℹ️ Ignition v2 initially, migrated to v3 (backward compatible conversion exists)
- ℹ️ MachineConfig wraps Ignition (provides Kubernetes-native interface)

## Implementation

- **Location**:
  - API: `vendor/github.com/openshift/api/machineconfiguration/v1/types.go` (MachineConfigSpec.Config field)
  - Parsing: `pkg/daemon/ignition.go`
  - Application: `pkg/daemon/update.go` (calls Ignition libraries)

- **Status**: Fully implemented since MCO inception

- **Validation**:
  - API validation ensures Ignition config is parseable
  - MCD validates Ignition before applying

## Alternatives Considered

### Alternative 1: cloud-init

**Pros**:
- Widely used in cloud environments
- Supports bash scripts directly

**Cons**:
- Imperative model (bash scripts)
- No pre-execution validation
- Not designed for immutable systems
- Does not align with CoreOS philosophy

**Why rejected**: Imperative model conflicts with declarative Kubernetes approach, no validation guarantees

### Alternative 2: Custom MCO Configuration Format

**Pros**:
- Could be tailored exactly to MCO needs
- Simpler than Ignition for common cases

**Cons**:
- Requires designing, implementing, and maintaining our own format
- Need to write our own validation, application logic
- No community/upstream support
- Reinventing existing, proven technology

**Why rejected**: Ignition already exists and solves our exact use case

## References

- [Ignition Specification](https://coreos.github.io/ignition/)
- [MCO MachineConfig Documentation](../../docs/MachineConfig.md)
- [Supported Ignition Changes](../../docs/MachineConfigDaemon.md#supported-vs-unsupported-ignition-config-changes)
- [CoreOS Ignition GitHub](https://github.com/coreos/ignition)

## Notes

- Ignition v3 added improvements like better error messages and new features
- Not all Ignition sections can be applied in-place (see MachineConfigDaemon docs)
- MCO uses `coreos/ign-converter` to convert between Ignition v2 and v3 formats

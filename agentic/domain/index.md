# Domain Documentation

## Purpose
Concepts, glossary, and workflows in this system's domain.

## Contents

- [Glossary](./glossary.md) - Term definitions

**Concepts**:
- [MachineConfig](./concepts/machineconfig.md) - Declarative specification for machine configuration
- [MachineConfigPool](./concepts/machineconfigpool.md) - Groups machines for targeted configuration
- [ControllerConfig](./concepts/controllerconfig.md) - Configuration for MCO controllers
- [Ignition](./concepts/ignition.md) - CoreOS configuration format
- [rpm-ostree](./concepts/rpm-ostree.md) - Operating system update mechanism

**Workflows**:
- [Configuration Update](./workflows/configuration-update.md) - How configuration changes flow through the system
- [OS Upgrade](./workflows/os-upgrade.md) - How operating system updates are applied

## When to Add Here

- **glossary.md**: All domain terms (alphabetical)
- **concepts/**: CRDs, packages, key interfaces (one file per concept)
- **workflows/**: Multi-step processes involving multiple components

## Related Sections

- [Components](../design-docs/components/) - Who implements these concepts
- [ADRs](../decisions/) - Why concepts are designed this way

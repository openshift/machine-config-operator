# Enhancement Index

## Repository-Local Enhancements

**Location**: `docs/` directory contains design documents for MCO features.

| Enhancement | Feature | Related ADR | Related Concepts |
|-------------|---------|-------------|------------------|
| [MachineConfig Design](../../docs/MachineConfig.md) | Core MachineConfig CRD | [ADR-0003](../decisions/adr-0003-ignition-config-format.md) | [MachineConfig](../domain/concepts/machineconfig.md) |
| [MachineConfigDaemon](../../docs/MachineConfigDaemon.md) | On-node daemon | [ADR-0002](../decisions/adr-0002-mcd-on-node-architecture.md) | [MachineConfigDaemon](../design-docs/components/machine-config-daemon.md) |
| [MachineConfigController](../../docs/MachineConfigController.md) | Pool controller | [ADR-0002](../decisions/adr-0002-mcd-on-node-architecture.md) | [MachineConfigController](../design-docs/components/machine-config-controller.md) |
| [MachineConfigServer](../../docs/MachineConfigServer.md) | Ignition server | [ADR-0003](../decisions/adr-0003-ignition-config-format.md) | [MachineConfigServer](../design-docs/components/machine-config-server.md) |
| [OS Upgrades](../../docs/OSUpgrades.md) | OS update mechanism | [ADR-0001](../decisions/adr-0001-use-rpm-ostree.md) | [rpm-ostree](../domain/concepts/rpm-ostree.md) |
| [On-Cluster Layering](../../docs/UsingLayering.md) | OS package layering | - | [rpm-ostree](../domain/concepts/rpm-ostree.md) |
| [KubeletConfig](../../docs/KubeletConfigDesign.md) | Kubelet configuration | - | - |
| [ContainerRuntimeConfig](../../docs/ContainerRuntimeConfigDesign.md) | CRI-O configuration | - | - |
| [MachineOSBuilder](../../docs/MachineOSBuilderDesign.md) | On-cluster builds | - | - |

## External Enhancements (openshift/enhancements)

**Note**: Check https://github.com/openshift/enhancements for MCO-related enhancements.

Notable external enhancements affecting MCO:

| Enhancement | Feature | Status | Impact |
|-------------|---------|--------|--------|
| TBD | (Search openshift/enhancements for machine-config) | - | - |

## How to Use This Index

- **When implementing a feature**: Check if there's an enhancement doc (local or external) for design context
- **When documenting a decision**: Link to the relevant enhancement
- **When onboarding**: Read enhancements to understand "why" behind implementation
- **When debugging**: Enhancement docs often contain troubleshooting context

## Adding New Enhancements

When adding new features:
1. Create design doc in `docs/` if substantial architectural change
2. Reference enhancement in ADR frontmatter
3. Link from relevant concept docs
4. Update this index

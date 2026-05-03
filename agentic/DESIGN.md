## Design Philosophy - machine-config-operator

## Overview

The machine-config-operator treats the operating system itself as a managed Kubernetes component, extending declarative cluster management to systemd, kubelet, kernel configuration, and the OS image itself.

## Design Principles

### 1. Operating System as Infrastructure Code
All OS configuration is declared via Kubernetes CRDs (MachineConfig, MachineConfigPool), not imperative commands or SSH access.

**Why**: Enables version control, audit trails, and declarative management consistent with Kubernetes philosophy.

**Example**: Adding SSH keys or modifying kubelet config uses `oc apply -f machineconfig.yaml` rather than SSH + manual edits.

### 2. Immutability with Controlled Mutability
The OS image is immutable (rpm-ostree), but configuration (/ etc, /var) is mutable in controlled ways via declared MachineConfigs.

**Why**: Prevents configuration drift while allowing necessary changes. Immutable base + declarative changes = predictable state.

**Example**: OS image updates are atomic (all-or-nothing), but file changes in /etc are applied incrementally and validated.

### 3. Snapshot-Based Rendering
Configuration is rendered into immutable snapshots (rendered-<pool>-<hash>) with all remote content embedded at render time.

**Why**: Ensures all nodes in a pool have identical configuration; prevents drift from remote source changes.

**Example**: pkg/controller/template/render.go fetches remote Ignition files and embeds them into rendered config.

### 4. Progressive Rollout with Safety
Updates roll out gradually (controlled by maxUnavailable), with health checks and automatic rollback on failure.

**Why**: Prevents cluster-wide outages; single node failure doesn't cascade.

**Example**: MachineConfigPool spec.maxUnavailable limits concurrent node updates.

## Architecture Decisions

Key architectural decisions that shape this codebase:

1. **Use rpm-ostree for OS Updates**: Atomic, image-based OS updates with automatic rollback
   - See: [ADR-0001](./decisions/adr-0001-use-rpm-ostree.md)

2. **MachineConfigDaemon On-Node Architecture**: Autonomous reconciliation loop on each node
   - See: [ADR-0002](./decisions/adr-0002-mcd-on-node-architecture.md)

3. **Ignition as Configuration Format**: First-boot provisioning via Ignition v3
   - See: [ADR-0003](./decisions/adr-0003-ignition-config-format.md)

## Design Patterns

### Rendered Config Pattern
**What**: Multiple input MachineConfigs merged into single rendered-<pool>-<hash> config per pool
**When to use**: When multiple sources contribute to node configuration
**Example**: pkg/controller/template/render.go merges platform configs + user configs
**Why**: Single source of truth, prevents config drift across nodes

### Operator-Managed Lifecycle
**What**: Single operator pod manages three sub-components (controller, server, daemon)
**When to use**: Coordinated lifecycle management of related components
**Example**: pkg/operator/sync.go manages sub-component deployments
**Why**: Version consistency, coordinated upgrades

### Daemon Reconciliation
**What**: On-node daemon continuously reconciles desired (API) vs actual (node) state
**When to use**: Need autonomous agents that survive control plane disruptions
**Example**: pkg/daemon/update.go compares current config vs rendered config
**Why**: Resilient to control plane issues, self-healing

## Anti-Patterns to Avoid

### ❌ Direct Node Modification
**Don't**: SSH to nodes and manually edit files or run commands
**Do**: Create MachineConfig declaring desired state
**Why**: Manual changes lost on reboot; breaks declarative model

### ❌ Dynamic Remote Configuration
**Don't**: Reference mutable remote Ignition configs or files
**Do**: Embed remote content at render time (snapshot)
**Why**: Prevents configuration drift between nodes and across time

### ❌ Uncontrolled Updates
**Don't**: Update all nodes simultaneously
**Do**: Use MachineConfigPool maxUnavailable for controlled rollout
**Why**: Prevents cluster-wide outages, enables safe rollback

## Trade-offs

### OS Updates Require Reboots
**What we chose**: rpm-ostree image-based updates requiring node reboot
**What we gave up**: Zero-downtime OS updates
**Why**: Atomic updates and guaranteed rollback capability worth the reboot cost; workload scheduling handles node unavailability

### Configuration Complexity
**What we chose**: Declarative MachineConfig CRDs with rendering pipeline
**What we gave up**: Simple imperative shell scripts
**Why**: Declarative model enables validation, version control, and automation; complexity is managed at platform level

### Read-Only Root Filesystem
**What we chose**: Immutable /usr, writable /etc and /var only
**What we gave up**: Ability to modify system files arbitrarily
**Why**: Prevents accidental or malicious corruption of OS; forces proper configuration management

## Related Documentation

- [Core Beliefs](./design-docs/core-beliefs.md) - Operating principles and patterns
- [Architecture](./ARCHITECTURE.md) - System structure and data flow
- [ADRs](./decisions/) - Detailed architectural decision records

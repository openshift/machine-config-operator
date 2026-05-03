# machine-config-operator - Agent Navigation

> **Purpose**: Table of contents for AI agents. Points to deeper knowledge.
> **Do not expand this file**. Keep under 150 lines. Link to details instead.

## What This Repository Does

Manages operating system configuration and updates for OpenShift nodes via Kubernetes operators, extending OpenShift control to systemd, kubelet, kernel, and NetworkManager.

## Quick Navigation by Intent

**I need to understand the system**
→ [ARCHITECTURE.md](./ARCHITECTURE.md)
→ [Core beliefs](./agentic/design-docs/core-beliefs.md)
→ [Components](./agentic/design-docs/components/)

**I'm implementing a feature** (MANDATORY WORKFLOW - follow in order)
0. INVESTIGATE the problem space first:
   - Read [ARCHITECTURE.md](./ARCHITECTURE.md) to understand system structure
   - Check relevant [design docs](./agentic/design-docs/) and [domain concepts](./agentic/domain/concepts/)
   - Review related [component documentation](./agentic/design-docs/components/)
   - VERIFY data structures/formats - Check reference docs for canonical output format
   - VALIDATE assumptions - Use grep/examples to confirm actual paths, field names, structures
   - Review reference implementations - Find similar code patterns in codebase
   - Ask clarifying questions if requirements are ambiguous
   - Only then read specific code files if needed
1. CREATE a plan in [active/](./agentic/exec-plans/active/) using [template](./agentic/exec-plans/template.md)
2. READ testing guide and relevant patterns before writing code
3. Implement with tests
4. Update plan status to completed

**I'm fixing a bug**
→ [Component map](./ARCHITECTURE.md#components)
→ [Debugging](./agentic/DEVELOPMENT.md#debugging)
→ [Tests](./agentic/TESTING.md)

**I need to understand a concept**
→ [Glossary](./agentic/domain/glossary.md)
→ [Concepts](./agentic/domain/concepts/)
→ [Workflows](./agentic/domain/workflows/)

## Repository Structure

```
cmd/                    # Component entry points
├── machine-config-daemon/
├── machine-config-controller/
├── machine-config-server/
└── machine-config-operator/

pkg/                    # Core packages
├── daemon/             # On-node daemon logic
├── controller/         # Controller reconciliation
├── server/             # Ignition server
└── operator/           # Operator lifecycle
```

## Component Boundaries

```
Operator → Controller (renders) + Server (boots) → Daemon (applies) → OS (RHCOS)
```

## Core Concepts (Domain Model)

| Concept | Definition | Docs |
|---------|-----------|------|
| MachineConfig | Declarative specification for node configuration (files, units, OS) | [./agentic/domain/concepts/machineconfig.md] |
| MachineConfigPool | Groups nodes for targeted configuration | [./agentic/domain/concepts/machineconfigpool.md] |
| Ignition | CoreOS first-boot configuration format | [./agentic/domain/concepts/ignition.md] |
| rpm-ostree | Image-based OS update system | [./agentic/domain/concepts/rpm-ostree.md] |
| ControllerConfig | Configuration for MCO controllers | [./agentic/domain/concepts/controllerconfig.md] |

## Key Invariants (ENFORCE THESE)

1. **Operating system is cluster-managed**: Nodes are configured via MachineConfig CRDs, not manual SSH changes
   - Validated by: MachineConfigDaemon reconciliation
   - Why: Ensures consistency and declarative management

2. **Rendered configs are immutable snapshots**: Remote content fetched and embedded at render time
   - Validated by: MachineConfigController rendering logic
   - Why: Prevents configuration drift across nodes

3. **All features require execution plans**: Must create plan in agentic/exec-plans/active/ before coding
   - Validated by: Code review
   - Why: Ensures design consideration and trackable decision history

## Critical Code Locations

| Purpose | File | Why Critical |
|---------|------|--------------|
| MachineConfig reconciliation | pkg/controller/template/render.go | Merges configs into rendered version |
| On-node updates | pkg/daemon/update.go | Applies configuration to running nodes |
| Ignition serving | pkg/server/server.go | Provides first-boot configs |
| OS upgrades | pkg/daemon/rpm-ostree.go | Manages rpm-ostree updates |

## External Dependencies

- **rpm-ostree**: Image-based OS update mechanism
- **Ignition**: CoreOS configuration format
- **openshift/api**: API type definitions (MachineConfig, MachineConfigPool)
- **openshift/library-go**: Shared operator patterns

## Build & Test

```bash
# Build
make

# Unit tests
make test-unit

# E2E tests
make test-e2e
```

## Documentation Structure

```
agentic/
├── design-docs/   # Architecture, components
├── domain/        # Concepts, workflows
├── exec-plans/    # Active work, tech debt
├── decisions/     # ADRs
├── references/    # External knowledge
├── generated/     # Auto-generated docs
├── DESIGN.md      # Design philosophy
├── DEVELOPMENT.md # Dev setup
├── TESTING.md     # Test strategy
├── RELIABILITY.md # SLOs, observability
├── SECURITY.md    # Security model
└── QUALITY_SCORE.md
```

## When You're Stuck

1. Check [tech debt tracker](./agentic/exec-plans/tech-debt-tracker.md)
2. Check [quality score](./agentic/QUALITY_SCORE.md)
3. File a plan in [active plans](./agentic/exec-plans/active/)

## Last Updated

This file is validated by CI on every commit.

# machine-config-operator - Agent Navigation

> **Purpose**: Table of contents for AI agents. Points to deeper knowledge.
> **Do not expand this file**. Keep under 150 lines. Link to details instead.

## What This Repository Does

Manages operating system configuration and updates for OpenShift nodes, bridging the gap between Kubernetes and the OS layer (systemd, cri-o, kubelet, NetworkManager, rpm-ostree).

## Quick Navigation by Intent

**I need to understand the system**
→ [ARCHITECTURE.md](./ARCHITECTURE.md)
→ [Core beliefs](./agentic/design-docs/core-beliefs.md)
→ [Components](./agentic/design-docs/components/)

**I'm implementing a feature** (MANDATORY WORKFLOW - follow in order)
0. INVESTIGATE the problem space first:
   - Read [ARCHITECTURE.md](./ARCHITECTURE.md) to understand 4-component structure
   - Check [design docs](./agentic/design-docs/) and [domain concepts](./agentic/domain/concepts/)
   - Review [component documentation](./agentic/design-docs/components/)
   - **VERIFY data structures** - Check MachineConfig/Ignition/rpm-ostree formats in references
   - **VALIDATE assumptions** - Use grep to confirm annotation names, API field names
   - **Review reference implementations** - Find similar code in existing controllers
   - Ask clarifying questions if requirements are ambiguous
1. CREATE a plan in [active/](./agentic/exec-plans/active/) using [template](./agentic/exec-plans/template.md)
2. READ testing guide and OpenShift operator patterns before writing code
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

## Component Boundaries

```
┌─────────────────────────────────────────────────────────────┐
│  machine-config-operator (MCO)                              │
│  - Manages lifecycle of other components                    │
│  - Reports ClusterOperator status                           │
└─────────────────────────────────────────────────────────────┘
         ↓ deploys
┌─────────────────────┬──────────────────────┬─────────────────┐
│ machine-config-     │ machine-config-      │ machine-config- │
│ controller (MCC)    │ daemon (MCD)         │ server (MCS)    │
│ - Template ctrl     │ - Applies configs    │ - Serves        │
│ - Render ctrl       │ - Manages OS updates │   Ignition to   │
│ - Update ctrl       │ - Runs on each node  │   new nodes     │
└─────────────────────┴──────────────────────┴─────────────────┘
```

## Core Concepts (Domain Model)

| Concept | Definition | Docs |
|---------|-----------|------|
| MachineConfig | Declarative OS configuration (Ignition format) | [./agentic/domain/concepts/machine-config.md](./agentic/domain/concepts/machine-config.md) |
| MachineConfigPool | Groups of nodes with shared configuration | [./agentic/domain/concepts/machine-config-pool.md](./agentic/domain/concepts/machine-config-pool.md) |
| Ignition | CoreOS config format for files/units/users | [./agentic/domain/concepts/ignition.md](./agentic/domain/concepts/ignition.md) |
| rpm-ostree | Image-based OS update mechanism | [./agentic/domain/concepts/rpm-ostree.md](./agentic/domain/concepts/rpm-ostree.md) |
| ControllerConfig | Global MCO configuration | [./agentic/domain/concepts/controller-config.md](./agentic/domain/concepts/controller-config.md) |

## Key Invariants (ENFORCE THESE)

1. **OS as Kubernetes Component**: The operating system is declaratively managed via Kubernetes APIs
   - Validated by: MachineConfig CRD, MachineConfigPool status
   - Why: Enables GitOps-style OS management

2. **Controlled Rollout**: Only `maxUnavailable` nodes update simultaneously
   - Validated by: UpdateController coordination
   - Why: Prevents cluster-wide outages during updates

3. **Config Drift Detection**: On-disk state must match MachineConfig
   - Validated by: MachineConfigDaemon fsnotify monitoring
   - Why: Prevents undocumented manual changes

4. **All features require execution plans**: Must create plan in agentic/exec-plans/active/ before coding
   - Validated by: Code review
   - Why: Ensures design consideration and trackable decision history

## Critical Code Locations

| Purpose | File | Why Critical |
|---------|------|--------------|
| Render MachineConfigs | pkg/controller/render/render_controller.go | Merges configs for pools |
| Apply node updates | pkg/daemon/update.go | Coordinates OS updates and reboots |
| Serve Ignition | pkg/server/server.go | Bootstraps new nodes |
| Template generation | pkg/controller/template/render.go | Creates OpenShift-owned configs |
| OS updates | pkg/daemon/rpm_ostree.go | Manages rpm-ostree operations |

## External Dependencies

**CoreOS Ignition** (config format) • **rpm-ostree** (OS updates) • **openshift/api** (CRDs) • **openshift/library-go** (operator patterns)

## Build & Test

```bash
make binaries              # Build all
make machine-config-daemon # Build specific component
make test-unit             # Unit tests
make test-e2e              # E2E tests (requires cluster)
```

## When You're Stuck

1. Check [tech debt tracker](./agentic/exec-plans/tech-debt-tracker.md)
2. Check [quality score](./agentic/QUALITY_SCORE.md)
3. Review [existing docs](./docs/) for legacy information
4. File a plan in [active plans](./agentic/exec-plans/active/)

## Last Updated

This file is validated by CI on every commit.

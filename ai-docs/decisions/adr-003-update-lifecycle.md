# ADR-003: Node Update Lifecycle

## Status
Active

## Context
Applying config changes to nodes requires coordination between the cluster-level controller and the node-level daemon to avoid disrupting workloads.

## Decision
Updates follow a multi-phase lifecycle tracked by MachineConfigNode conditions:

1. **Node Controller** detects config drift, respects `maxUnavailable`, sets target annotation
2. **MCD** picks up annotation change, cordons the node
3. **MCD** drains the node (evicts pods)
4. **MCD** applies config (files, systemd units, kernel args)
5. **MCD** performs OS update (rpm-ostree/bootc) if needed
6. **MCD** decides: reboot (OS/kernel changes) or service-reload (file-only changes)
7. **MCD** uncordons the node after successful apply
8. **Node Controller** updates pool status counters

The daemon reports granular progress via MachineConfigNode conditions (AppliedFiles, AppliedFilesAndOS, Drained, Cordoned, RebootedNode, ReloadedCRIO, etc.).

## Consequences
- `maxUnavailable` on MachineConfigPool controls rollout speed
- Degraded nodes block pool progress (require manual intervention)
- Config drift monitoring detects unauthorized changes between updates
- Pausing a pool (`paused: true`) halts all updates to that pool

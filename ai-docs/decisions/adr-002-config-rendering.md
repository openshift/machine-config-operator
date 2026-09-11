# ADR-002: Deterministic Config Rendering

## Status
Active

## Context
Multiple MachineConfigs can target a single MachineConfigPool. The system needs a predictable, reproducible way to merge them into one rendered config.

## Decision
The Render Controller sorts MachineConfigs by name and merges them sequentially. For file conflicts (same path in multiple MCs), last-writer-wins based on sort order. The rendered config name includes a content hash to avoid unnecessary rollouts when the effective config hasn't changed.

A 5-second render delay batches rapid changes (e.g., multiple MCs created simultaneously) to avoid churn.

## Consequences
- MC naming matters: alphabetically later MCs override earlier ones for conflicting files
- Content-based hashing means no-op updates don't trigger node rollouts
- The render delay means changes are not immediately visible (by design)
- Render failures set the `RenderDegraded` condition on the pool

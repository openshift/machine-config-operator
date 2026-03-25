---
status: [active | completed | abandoned]
owner: @[username]
created: YYYY-MM-DD
target: YYYY-MM-DD
related_issues: []
related_prs: []
enhancement: "openshift/enhancements#NNNN"
---

# Plan: [Feature/Project Name]

## Goal

[One sentence: what are we building and why?]

## Success Criteria

- [ ] Measurable outcome 1
- [ ] Measurable outcome 2
- [ ] Tests pass (unit, integration, e2e)
- [ ] Documentation updated
- [ ] No regressions in existing functionality

## Context

Why now? What's the business need or technical requirement?

**Related**:
- Product specs: [link]
- Design docs: [link]
- ADRs: [link]
- Enhancement: https://github.com/openshift/enhancements/pull/NNNN

## Technical Approach

### Architecture Changes

[What components change? What's the data flow? Include diagrams if helpful]

### New Abstractions

[What new types, interfaces, CRDs, or packages are being introduced?]

### API Changes

[Any changes to MachineConfig CRD, new fields, new CRDs?]

### Dependencies

[What external changes or approvals do we need? Upstream dependencies?]

## Implementation Phases

### Phase 1: [Name]

- [ ] Task 1
- [ ] Task 2
- [ ] Task 3

**Deliverable**: [What's complete after this phase]

### Phase 2: [Name]

- [ ] Task 4
- [ ] Task 5

**Deliverable**: [What's complete after this phase]

### Phase 3: [Name]

- [ ] Task 6
- [ ] Task 7

**Deliverable**: [What's complete after this phase]

## Testing Strategy

- **Unit tests**: [Coverage target, what to test]
- **Integration tests**: [Scenarios to cover]
- **E2E tests**: [User journeys to validate]
- **Upgrade tests**: [Validation that upgrades work]
- **Performance tests**: [If applicable, load/scale testing]

## Rollout Plan

- **Feature flag?**: [Yes/No - if yes, describe flag]
- **Tech preview first?**: [Yes/No]
- **Rollout phases**: [GA in one release? Gradual rollout?]
- **Rollback plan**: [How to safely revert if issues found]
- **Monitoring**: [What metrics/alerts to add]

## Decision Log

### YYYY-MM-DD: [Decision]

[Why we chose X instead of Y, what changed from original plan]

### YYYY-MM-DD: [Another Decision]

[Rationale for pivot or approach change]

## Progress Notes

### YYYY-MM-DD

**Completed**:
- [What was done]

**Blockers**:
- [Any blockers encountered]

**Next steps**:
- [What's next]

### YYYY-MM-DD

**Completed**:
- [Updates]

## Completion Checklist

- [ ] All tests pass (unit, integration, e2e)
- [ ] Documentation updated (user docs, dev docs)
- [ ] ADR filed if architectural decision made
- [ ] Tech debt addressed or tracked in tech-debt-tracker.md
- [ ] Enhancement PR updated with final design
- [ ] Plan moved to `completed/` directory
- [ ] Related issues closed

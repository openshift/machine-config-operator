---
status: [active | completed | abandoned]
owner: @[username]
created: YYYY-MM-DD
target: YYYY-MM-DD
related_issues: [#1234, #5678]
related_prs: []
---

# Plan: [Feature/Project Name]

## Goal

[One sentence: what are we building and why?]

## Success Criteria

- [ ] Measurable outcome 1
- [ ] Measurable outcome 2
- [ ] Tests pass
- [ ] Documentation updated

## Context

Why now? What's the business need?

Link to relevant:
- Product specs: [link]
- Design docs: [link]
- ADRs: [link]

## Technical Approach

### Architecture Changes

[What components change? What's the data flow?]

### New Abstractions

[What new types, interfaces, or packages?]

### Dependencies

[What external changes do we need?]

## Implementation Phases

### Phase 1: [Name]
- [ ] Task 1
- [ ] Task 2

### Phase 2: [Name]
- [ ] Task 3
- [ ] Task 4

## Testing Strategy

- Unit tests: [coverage target]
- Integration tests: [scenarios]
- E2E tests: [user journeys]

## Rollout Plan

- Feature flag? [yes/no]
- Tech preview first? [yes/no]
- Rollback plan? [description]

## Decision Log

### YYYY-MM-DD: [Decision]
[Why we chose X instead of Y]

## Progress Notes

### YYYY-MM-DD
- [What happened]
- [Blockers]
- [Next steps]

## Completion Checklist

- [ ] All tests pass
- [ ] Documentation updated
- [ ] ADR filed if needed
- [ ] Tech debt addressed or tracked
- [ ] Plan moved to `completed/`

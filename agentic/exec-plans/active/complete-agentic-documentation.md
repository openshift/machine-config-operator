---
status: active
owner: @mco-team
created: 2026-03-30
target: 2026-05-01
related_issues: []
related_prs: []
---

# Plan: Complete Agentic Documentation Framework

## Goal

Implement comprehensive agentic documentation for machine-config-operator to improve AI agent navigation and developer onboarding, achieving quality score of 90+/100.

## Success Criteria

- [ ] Quality score ≥ 90/100
- [ ] AGENTS.md under 150 lines
- [ ] All required top-level files populated (DESIGN, DEVELOPMENT, TESTING, RELIABILITY, SECURITY, QUALITY_SCORE)
- [ ] Minimum 3 ADRs documenting existing architectural decisions
- [ ] Minimum 5 concept docs for core domain concepts
- [ ] CI validation workflow passing
- [ ] No broken links
- [ ] All placeholders replaced with actual content

## Context

The machine-config-operator is a critical OpenShift component managing OS configuration and updates. With growing complexity and new contributors, we need structured documentation that serves both human developers and AI agents. Current documentation exists in docs/ but lacks:

- Structured navigation (AGENTS.md)
- Architectural decision records (ADRs)
- Domain concept documentation
- Cross-linking and progressive disclosure

Link to relevant:
- Rulebook: /home/psundara/ws/src/github.com/openshift/agentic-guide/AGENTIC_DOCS_RULEBOOK.md
- OpenShift guidance: /home/psundara/ws/src/github.com/openshift/agentic-guide/OPENSHIFT_SPECIFIC_GUIDANCE.md

## Technical Approach

### Architecture Changes

No code changes - documentation-only implementation.

### New Abstractions

New documentation structure under `agentic/`:
- design-docs/ - Architecture and component docs
- domain/ - Concepts, glossary, workflows
- decisions/ - ADRs
- exec-plans/ - Active work tracking
- references/ - OpenShift-specific refs (enhancements, APIs)
- generated/ - Auto-generated docs (metrics dashboard)

### Dependencies

- Metrics scripts from agentic-guide repository
- CI workflow for validation

## Implementation Phases

### Phase 1: Structure Creation (COMPLETE)
- [x] Create directory structure
- [x] Copy metrics scripts
- [x] Create index files

### Phase 2: Core Documents (COMPLETE)
- [x] Create AGENTS.md (under 150 lines)
- [x] Create ARCHITECTURE.md
- [x] Create core-beliefs.md

### Phase 3: Domain Documentation (COMPLETE)
- [x] Create glossary.md with OpenShift markers
- [x] Create 5+ concept docs (MachineConfig, MachineConfigPool, Ignition, rpm-ostree, ControllerConfig, RHCOS)

### Phase 4: ADRs and Templates (COMPLETE)
- [x] Create ADR template
- [x] Create 3 ADRs (rpm-ostree, MCD architecture, Ignition)
- [x] Create exec-plan template
- [x] Create tech debt tracker
- [x] Create this plan

### Phase 5: Required Top-Level Files (IN PROGRESS)
- [ ] Populate DESIGN.md
- [ ] Populate DEVELOPMENT.md
- [ ] Populate TESTING.md
- [ ] Populate RELIABILITY.md
- [ ] Populate SECURITY.md
- [ ] Create QUALITY_SCORE.md

### Phase 6: OpenShift-Specific References (IN PROGRESS)
- [ ] Create enhancement-index.md
- [ ] Create openshift-apis.yaml
- [ ] Create openshift-ecosystem.md
- [ ] Create openshift-operator-patterns-llms.txt
- [ ] Create openshift-docs-standards.md

### Phase 7: CI and Metrics (PENDING)
- [ ] Create .github/workflows/validate-agentic-docs.yml
- [ ] Run metrics dashboard generation
- [ ] Document actual measured quality score
- [ ] Fix any validation errors

## Testing Strategy

- Validation: Run ./agentic/scripts/measure-all-metrics.sh --html
- CI: Automated checks for structure, links, frontmatter
- Manual: Verify AGENTS.md navigation in ≤3 hops

## Rollout Plan

- Feature flag: No (documentation only)
- Tech preview: No
- Rollback plan: Documentation changes are non-breaking

## Decision Log

### 2026-03-30: Use Actual MCO Content, Not Placeholders
All documentation uses actual MCO component names, file paths, and architectural decisions rather than generic placeholders. This provides immediate value to users.

### 2026-03-30: Create 3 ADRs for Existing Decisions
Document decisions already made in codebase (rpm-ostree, MCD architecture, Ignition) rather than waiting for new decisions. This captures institutional knowledge.

## Progress Notes

### 2026-03-30
- Created complete directory structure
- Populated AGENTS.md (143 lines)
- Created ARCHITECTURE.md with actual components
- Created 6 concept docs with real code locations
- Created 3 ADRs documenting existing decisions
- Created templates and this plan
- Next: Populate remaining top-level files and OpenShift references

## Completion Checklist

- [ ] All tests pass (CI validation)
- [ ] Documentation updated (self-documenting)
- [ ] ADRs filed (3 created)
- [ ] Tech debt addressed or tracked (tracker created)
- [ ] Plan moved to `completed/` (when done)
- [ ] Quality score ≥ 90/100 measured and documented

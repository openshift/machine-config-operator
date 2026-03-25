---
status: active
owner: @ai-agent
created: 2025-03-25
target: 2025-04-25
related_issues: []
related_prs: []
---

# Plan: Complete Agentic Documentation to 95/100 Quality Score

## Goal

Reach documentation quality score of 95/100 by addressing gaps identified in initial implementation and adding comprehensive domain, component, and reference documentation.

## Success Criteria

- [ ] Quality score ≥ 95/100
- [ ] CI validation passes on all PRs
- [ ] All code references include specific file paths
- [ ] Component documentation complete for all 5 major components
- [ ] Domain concepts documented for all major CRDs and workflows
- [ ] No broken links (verified by CI)
- [ ] All OpenShift-specific references created
- [ ] Future enhancements tracked in tech debt tracker

## Context

Initial agentic documentation framework implemented on 2025-03-25 with baseline structure. Created:
- Complete directory structure
- AGENTS.md (119 lines) and ARCHITECTURE.md
- 3 initial ADRs documenting existing architectural decisions
- Core beliefs and design philosophy
- Exec-plan template and tech debt tracker
- Initial index files

**Initial gaps identified**:
- Missing component-specific documentation (5 components)
- Missing domain concept docs (MachineConfig, MachineConfigPool, Ignition, rpm-ostree, etc.)
- Missing workflow documentation (bootstrap, updates, recovery)
- Missing OpenShift-specific references
- Missing required top-level files (DESIGN.md, DEVELOPMENT.md, TESTING.md, RELIABILITY.md, SECURITY.md, QUALITY_SCORE.md)
- Missing glossary

**Related**:
- Quality Score: [../../QUALITY_SCORE.md](../../QUALITY_SCORE.md)
- Tech Debt Tracker: [../tech-debt-tracker.md](../tech-debt-tracker.md)
- Agentic Guide: https://github.com/openshift/agentic-guide

## Technical Approach

### Documentation Improvements

No code changes - documentation-only improvements.

### Tasks

1. Create required top-level files (DESIGN.md, DEVELOPMENT.md, TESTING.md, RELIABILITY.md, SECURITY.md, QUALITY_SCORE.md)
2. Create domain glossary with OpenShift markers
3. Create concept docs for major CRDs and technologies
4. Create component documentation for all 5 components
5. Create workflow documentation for common operations
6. Create OpenShift-specific references
7. Run CI validation and fix any errors

## Implementation Phases

### Phase 1: Required Top-Level Files (Week 1)

- [ ] Create agentic/DESIGN.md (design philosophy)
- [ ] Create agentic/DEVELOPMENT.md (dev setup, debugging)
- [ ] Create agentic/TESTING.md (test strategy)
- [ ] Create agentic/RELIABILITY.md (SLOs, observability, ClusterOperator status)
- [ ] Create agentic/SECURITY.md (RBAC, threat model)
- [ ] Create agentic/QUALITY_SCORE.md (initial scoring with progress tracking)

**Deliverable**: All 6 required top-level files exist and are populated

### Phase 2: Domain Documentation (Week 2)

- [ ] Create agentic/domain/glossary.md with all major terms
- [ ] Create concept doc for MachineConfig
- [ ] Create concept doc for MachineConfigPool
- [ ] Create concept doc for Ignition
- [ ] Create concept doc for rpm-ostree
- [ ] Create concept doc for ControllerConfig
- [ ] Create workflow docs (bootstrap, update, recovery)

**Deliverable**: Comprehensive domain documentation

### Phase 3: Component Documentation (Week 2-3)

- [ ] Create component doc for machine-config-operator
- [ ] Create component doc for machine-config-controller
- [ ] Create component doc for machine-config-daemon
- [ ] Create component doc for machine-config-server
- [ ] Create component doc for machine-os-builder

**Deliverable**: All 5 components documented

### Phase 4: OpenShift-Specific References (Week 3)

- [ ] Create agentic/references/openshift-apis.yaml
- [ ] Create agentic/references/openshift-ecosystem.md
- [ ] Create agentic/references/openshift-operator-patterns-llms.txt
- [ ] Create agentic/references/enhancement-index.md
- [ ] Create agentic/references/openshift-docs-standards.md

**Deliverable**: OpenShift-specific references complete

### Phase 5: CI and Validation (Week 4)

- [ ] Create .github/workflows/validate-agentic-docs.yml
- [ ] Run CI validation locally
- [ ] Fix any validation errors (broken links, missing frontmatter, etc.)
- [ ] Verify AGENTS.md stays under 150 lines
- [ ] Calculate final quality score
- [ ] Update QUALITY_SCORE.md with results

**Deliverable**: All validation checks pass, quality score ≥ 95/100

## Testing Strategy

- Run `./validate-agentic-docs.yml` equivalent checks locally
- Verify AGENTS.md line count: `wc -l AGENTS.md` (must be ≤ 150)
- Check all links with markdown-link-check
- Verify no placeholder text: `grep -r '\[REPO-NAME\]\|\[Component1\]' agentic/`
- Verify YAML frontmatter on all required files
- Verify at least 3 ADRs exist (not counting template)
- Verify at least 1 active exec-plan exists (this file)

## Rollout Plan

- **Feature flag?**: N/A (documentation only)
- **Rollout**: Single PR or multiple PRs as work progresses
- **Monitoring**: Quality score tracked in QUALITY_SCORE.md

## Decision Log

### 2025-03-25: Prioritized Required Files First

Initial implementation focused on creating required infrastructure (ADRs, exec-plans, index files) before detailed content. This ensures framework validation passes early.

**Why**: Framework requires specific files to exist for validation, so created those first before comprehensive domain/component docs.

## Progress Notes

### 2025-03-25

**Completed**:
- Created complete directory structure
- Created AGENTS.md (119 lines) and ARCHITECTURE.md
- Created 3 initial ADRs (Ignition, rpm-ostree, four-component architecture)
- Created core-beliefs.md with operating principles
- Created all index files
- Created exec-plan template and tech-debt-tracker.md
- Created this plan

**Next steps**:
- Create required top-level files (Phase 1)
- Create domain glossary and concept docs (Phase 2)

**Blockers**: None

## Completion Checklist

- [ ] Quality score ≥ 95/100
- [ ] All validation checks pass
- [ ] All required files exist and are populated
- [ ] No broken links
- [ ] No placeholder text remains
- [ ] Plan moved to `completed/` directory

# Technical Debt Tracker

> **Purpose**: Track known issues, workarounds, and improvements needed
> **Update**: Add new debt immediately, remove when resolved

## High Priority

### Bootstrap Test Coverage Gaps

**Status**: Open
**Owner**: @team
**Created**: 2025-03-25
**Impact**: Bootstrap and controller code paths could drift without detection
**Workaround**: Manual testing of bootstrap process
**Fix**: Expand bootstrap test coverage for all controllers
**Effort**: M
**Related**: test/e2e-bootstrap/

### Config Drift Detection Performance

**Status**: Open
**Owner**: @team
**Created**: 2025-03-25
**Impact**: fsnotify can be CPU-intensive with many watched files
**Workaround**: Currently acceptable performance
**Fix**: Investigate more efficient filesystem monitoring or batching
**Effort**: L
**Related**: pkg/daemon/drift.go

## Medium Priority

### Documentation for On-Cluster Layering

**Status**: In Progress
**Owner**: @team
**Created**: 2025-03-25
**Impact**: Users need better guidance on using layering features
**Workaround**: Existing docs in docs/UsingLayering.md
**Fix**: Expand documentation with more examples and troubleshooting
**Effort**: S
**Related**: docs/UsingLayering.md, docs/onclusterlayering-quickstart.md

## Low Priority / Nice to Have

### Refactor Controller Package Structure

**Status**: Open
**Owner**: @team
**Created**: 2025-03-25
**Impact**: Controller package could be better organized
**Workaround**: Current structure is functional
**Fix**: Reorganize controllers into clearer subdirectories
**Effort**: M
**Related**: pkg/controller/

## Resolved (Recent)

### Example Resolved Item

**Resolved**: YYYY-MM-DD
**How**: [PR link, description]

---

## How to Use This

**Adding debt**:
1. Add to appropriate priority section
2. Fill all fields (Status, Owner, Created, Impact, Workaround, Fix, Effort, Related)
3. Link to related issues/PRs if they exist

**Updating debt**:
1. Change status/owner as progress is made
2. Update workaround if changed
3. Move to "Resolved" when fixed (keep for 90 days)

**Cleaning up**:
- Archive resolved items after 90 days
- Re-prioritize monthly based on impact
- Ensure all high-priority items have owners

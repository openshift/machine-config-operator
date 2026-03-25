# Enhancement Index

> **Purpose**: Links features to OpenShift Enhancement Proposals (EPs)
> **Update**: When implementing features from enhancements

## Active Enhancements

| Enhancement | Feature | Status | ADR | Concepts |
|-------------|---------|--------|-----|----------|
| [enhancements#various](https://github.com/openshift/enhancements) | On-Cluster Layering | Implemented (4.12+) | N/A | [MachineOSBuilder](../domain/concepts/) |
| [enhancements#various](https://github.com/openshift/enhancements) | Node Disruption Policy | Implemented (4.17+) | N/A | [NodeDisruptionPolicy](../domain/concepts/) |
| [enhancements#various](https://github.com/openshift/enhancements) | Config Drift Detection | Implemented (4.10+) | N/A | [Config Drift](../design-docs/core-beliefs.md) |

## How to Find Enhancements

**Search enhancement repo**:
```bash
# By feature name
https://github.com/openshift/enhancements/search?q=machine-config

# By API group
https://github.com/openshift/enhancements/search?q=machineconfiguration
```

**Check code comments**:
```bash
# Look for enhancement references in code
grep -r "enhancement" --include="*.go" pkg/
grep -r "KEP-" --include="*.go" pkg/
```

## Enhancement Template

When implementing a new feature from an enhancement:

1. Create ADR referencing enhancement
2. Add to this index
3. Update concept docs with enhancement link
4. Create exec-plan with enhancement link in frontmatter

**Example**:
```yaml
---
enhancement: "openshift/enhancements#1234"
---
```

## Related

- [ADRs](../decisions/) - Architectural decisions
- [Exec Plans](../exec-plans/) - Implementation plans
- [OpenShift Enhancements](https://github.com/openshift/enhancements) - Full enhancement list

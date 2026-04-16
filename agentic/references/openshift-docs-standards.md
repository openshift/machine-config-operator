# OpenShift Documentation Standards

## Official Documentation

### Product Documentation
**URL**: https://docs.openshift.com/container-platform/latest/
**Use for**: User-facing features, configuration guides, troubleshooting

**Relevant Sections for MCO**:
- Post-installation machine configuration: https://docs.openshift.com/container-platform/latest/post_installation_configuration/machine-configuration-tasks.html
- Architecture - RHCOS: https://docs.openshift.com/container-platform/latest/architecture/architecture-rhcos.html
- Updating clusters: https://docs.openshift.com/container-platform/latest/updating/index.html

### API Reference
**URL**: https://docs.openshift.com/container-platform/latest/rest_api/
**Use for**: API field definitions, examples, versioning

**Relevant API Groups**:
- Machine Configuration APIs: https://docs.openshift.com/container-platform/latest/rest_api/machine_apis/machineconfig-machineconfiguration-openshift-io-v1.html
- Config APIs: https://docs.openshift.com/container-platform/latest/rest_api/config_apis/

## Enhancement Process

### Enhancement Repository
**URL**: https://github.com/openshift/enhancements
**Use for**: Design proposals, architectural decisions, feature planning

**Process**:
1. Create enhancement proposal in openshift/enhancements repo
2. Document design, alternatives, risks
3. Get approval from stakeholders
4. Implement feature
5. Reference enhancement in ADRs and docs

### Dev Guides
**URL**: https://github.com/openshift/enhancements/tree/master/dev-guide
**Use for**: Development conventions, operator patterns, best practices

**Relevant Dev Guides**:
- Operator development
- ClusterOperator status reporting
- Enhancement proposal guidelines

## Upstream Kubernetes Documentation

### Core Concepts
**URL**: https://kubernetes.io/docs/concepts/
**Use for**: Understanding Kubernetes primitives (Pods, DaemonSets, etc.)

**Relevant Topics**:
- Controllers: https://kubernetes.io/docs/concepts/architecture/controller/
- DaemonSets: https://kubernetes.io/docs/concepts/workloads/controllers/daemonset/
- RBAC: https://kubernetes.io/docs/reference/access-authn-authz/rbac/

### API Reference
**URL**: https://kubernetes.io/docs/reference/kubernetes-api/
**Use for**: Upstream Kubernetes API fields and behaviors

## Technology-Specific Documentation

### Ignition
**URL**: https://coreos.github.io/ignition/
**Use for**: Ignition configuration syntax, examples, versions

**Current Version Used**: v3.2.0
**Spec**: https://coreos.github.io/ignition/configuration-v3_2/

### rpm-ostree
**URL**: https://coreos.github.io/rpm-ostree/
**Use for**: OS update mechanism, package layering, troubleshooting

### CoreOS / Fedora CoreOS
**URL**: https://docs.fedoraproject.org/en-US/fedora-coreos/
**Use for**: Understanding RHCOS foundation, best practices

## Internal References

### Repository Documentation
**Location**: `docs/` directory in this repository
**Use for**: MCO-specific design docs, implementation details

**Key Documents**:
- MachineConfig.md - Core API design
- MachineConfigDaemon.md - Daemon architecture
- OSUpgrades.md - Update mechanism

### ADRs (Architectural Decision Records)
**Location**: `agentic/decisions/` in this repository
**Use for**: Understanding "why" behind architectural choices

### Enhancement Index
**Location**: `agentic/references/enhancement-index.md`
**Use for**: Links to relevant enhancements (local and external)

## Documentation Style Guidelines

### Code References
- Use file paths without line numbers (e.g., `pkg/daemon/update.go`)
- Link to functions when specific (e.g., `pkg/daemon/update.go:updateOSAndReboot()`)
- Avoid line numbers (they change frequently)

### API Examples
- Use YAML for user-facing examples
- Use Go for internal code examples
- Include namespace and labels in examples

### Links
- Use relative links for internal docs (e.g., `./concepts/machineconfig.md`)
- Use absolute URLs for external docs
- Keep links up-to-date (CI validates)

## When to Reference Which Docs

**User asks "How do I configure X?"**
→ OpenShift product docs (docs.openshift.com)

**User asks "What does field Y do in API Z?"**
→ OpenShift API reference or upstream K8s API docs

**Developer asks "How should I implement pattern P?"**
→ library-go, dev-guide, or peer operator examples

**Developer asks "Why is the architecture like this?"**
→ ADRs, design docs, enhancements

**Developer asks "How does component C work?"**
→ Component docs (agentic/design-docs/components/)

## Related Documentation

- [Enhancement Index](./enhancement-index.md) - Links to design docs
- [OpenShift Ecosystem](./openshift-ecosystem.md) - Related operators
- [Operator Patterns](./openshift-operator-patterns-llms.txt) - Implementation patterns

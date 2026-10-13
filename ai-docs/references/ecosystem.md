# MCO Ecosystem References

## Platform Documentation

Generic OpenShift operator patterns, testing practices, and security guidelines live in the platform hub. Reference these for patterns shared across all OpenShift operators:

- **Platform hub:** `openshift/enhancements/ai-docs/` (operator patterns, API conventions, testing standards)
- **OpenShift API types:** `github.com/openshift/api` (CRD definitions, shared types)
- **OpenShift library-go:** `github.com/openshift/library-go` (operator scaffolding, controller patterns)

## MCO-Specific References

- **Upstream docs:** `docs/` directory in this repo (HACKING.md, MachineConfig.md, MachineConfigDaemon.md, etc.)
- **Ignition spec:** [Ignition v2 specification](https://coreos.github.io/ignition/) - config format MCO uses for node configuration
- **rpm-ostree:** OS update mechanism for traditional RHCOS nodes
- **bootc:** Container-native OS update mechanism for layered nodes

## Related OpenShift Components

| Component | Relationship to MCO |
|-----------|-------------------|
| `openshift/installer` | Creates bootstrap MachineConfigs, provides initial ControllerConfig |
| `openshift/api` | Defines all MCO CRD types in `machineconfiguration/v1/` |
| `openshift/client-go` | Generated clients for MCO CRDs |
| `openshift/library-go` | Operator scaffolding, resource apply patterns |
| `openshift/cluster-version-operator` | Manages MCO operator deployment, coordinates upgrades |
| `kubernetes/kubernetes` | Kubelet config, node API, drain utilities |
| `coreos/ignition` | Config format for node provisioning |
| `coreos/rpmostree-client-go` | Go client for rpm-ostree operations on nodes |

## CI and Testing

- **CI system:** OpenShift CI (ci-operator), config in `.ci-operator.yaml`
- **Test framework:** controller-runtime envtest (real etcd + kube-apiserver)
- **E2E framework:** Ginkgo v2 + Gomega
- **Kubebuilder tools version:** 1.32.1 (for envtest)

## Internal Tools

Developer experience tools in `devex/cmd/`:
- `mcdiff` - Compare MachineConfigs
- `mco-push` - Push MCO changes to a live cluster
- `mco-builder` - Build OS container images
- `wait-for-mcp` - Wait for MachineConfigPool to finish updating
- `onclustertesting` - Run tests against a live cluster
- `run-on-all-nodes` - Execute commands across all cluster nodes

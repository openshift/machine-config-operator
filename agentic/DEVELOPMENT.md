# Development Guide

## Prerequisites

- Go 1.25.3+
- Podman or Docker
- OpenShift cluster (for testing) - Use [installer](https://github.com/openshift/installer/)
- kubectl/oc CLI
- git

## Initial Setup

1. Clone repository:
```bash
git clone https://github.com/openshift/machine-config-operator
cd machine-config-operator
```

2. Install dependencies:
```bash
make go-deps
```

3. Build components:
```bash
make binaries          # Build all components
# OR
make machine-config-daemon  # Build specific component
```

See [HACKING.md](../docs/HACKING.md) for comprehensive development instructions.

## Development Workflow

### Making Changes

1. Create a branch: `git checkout -b feature/my-feature`
2. Make your changes
3. Run tests locally: `make test-unit`
4. Build: `make binaries`
5. Test in cluster (see below)
6. Commit and push

### Running Tests

```bash
# Unit tests
make test-unit

# Specific package
go test -v github.com/openshift/machine-config-operator/pkg/daemon/...

# E2E tests (requires cluster)
make test-e2e

# Bootstrap tests
make test-bootstrap
```

### Local Testing

**Quick iteration on MCD** (without rebuilding images):
```bash
# One-time setup
./hack/prep-cluster-for-mcd-push.sh

# Push changes to MCD pods
./hack/push-to-mcd-pods.sh
```

See [docs/HACKING.md](../docs/HACKING.md#developing-the-mcd-without-building-images) for details.

## Debugging

### Debugging MachineConfigDaemon

1. Access node:
```bash
oc debug node/<node-name>
chroot /host
```

2. Check MCD status:
```bash
systemctl status machine-config-daemon
journalctl -u machine-config-daemon -f
```

3. Check current/desired config:
```bash
oc get node/<node-name> -o yaml | grep machineconfiguration.openshift.io
```

4. Check rpm-ostree status:
```bash
rpm-ostree status
```

### Debugging MachineConfigController

1. Check controller logs:
```bash
oc logs -n openshift-machine-config-operator deployment/machine-config-controller -f
```

2. Check MachineConfigPool status:
```bash
oc get machineconfigpool
oc describe machineconfigpool/<pool-name>
```

3. Check rendered configs:
```bash
oc get machineconfig | grep rendered
oc get machineconfig/<rendered-config-name> -o yaml
```

### Debugging Cluster Operator Status

```bash
# Check MCO status
oc get clusteroperator machine-config

# Describe for details
oc describe clusteroperator machine-config

# Check operator logs
oc logs -n openshift-machine-config-operator deployment/machine-config-operator -f
```

### Common Issues

**Issue**: MCD stuck in "Working" state

**Cause**: Update failed or node cannot drain

**Fix**: Check MCD logs, node events, and pod eviction status

**Issue**: Config drift detected

**Cause**: Manual file edits on node

**Fix**: Revert manual changes or create/update MachineConfig to match desired state

**Issue**: rpm-ostree upgrade fails

**Cause**: Network issues, invalid OS image, disk space

**Fix**: Check MCD logs, `rpm-ostree status`, disk space with `df -h`

## Code Organization

- `cmd/`: Entry points for each component
- `pkg/controller/`: MachineConfig controllers
- `pkg/daemon/`: MachineConfigDaemon implementation
- `pkg/operator/`: Main operator logic
- `pkg/server/`: MachineConfigServer
- `pkg/apihelpers/`: API utilities
- `test/`: E2E and integration tests

See [ARCHITECTURE.md](../ARCHITECTURE.md) for detailed package structure.

## Making a Pull Request

1. Ensure tests pass: `make test-unit`
2. Update documentation if needed
3. Create PR with clear description
4. Address review feedback
5. Wait for CI to pass

See [CONTRIBUTING.md](../CONTRIBUTING.md) for full process.

## Building Custom Images

```bash
# Build MCO image
make image

# Push to registry
REPO=quay.io/myuser ./hack/push-image.sh

# Create custom release payload
oc adm release new \
    --from-release quay.io/openshift-release-dev/ocp-release:4.XX.Y-x86_64 \
    --to-image quay.io/myuser/origin-release:v4.XX.Y \
    machine-config-operator=quay.io/myuser/machine-config-operator:latest
```

See [docs/HACKING.md#build-a-custom-release-payload](../docs/HACKING.md#build-a-custom-release-payload) for full instructions.

## Related Documentation

- [HACKING.md](../docs/HACKING.md) - Comprehensive development guide
- [TESTING.md](./TESTING.md) - Test strategy and organization
- [ARCHITECTURE.md](../ARCHITECTURE.md) - Code structure

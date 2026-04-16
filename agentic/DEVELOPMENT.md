# Development Guide

## Prerequisites

- Go 1.25.3+
- OpenShift cluster (or equivalent Kubernetes)
- oc CLI
- make
- git

## Initial Setup

1. Clone repository
```bash
git clone https://github.com/openshift/machine-config-operator
cd machine-config-operator
```

2. Install dependencies
```bash
make update-deps
```

3. Build
```bash
make
```

Binaries output to `_output/linux/amd64/`:
- machine-config-daemon
- machine-config-controller
- machine-config-server
- machine-config-operator

## Development Workflow

### Making Changes

1. Create a feature branch
```bash
git checkout -b my-feature
```

2. Make your changes

3. Run tests locally
```bash
make test-unit
```

4. Verify code style
```bash
make verify
```

5. Commit and push
```bash
git commit -m "Description of changes"
git push origin my-feature
```

### Running Tests

```bash
# Unit tests
make test-unit

# E2E tests (requires cluster)
make test-e2e

# Specific test
go test ./pkg/daemon -run TestSpecificFunction

# With coverage
make test-unit COVERAGE=true
```

### Local Testing

**Run daemon locally** (requires privileged access):
```bash
sudo _output/linux/amd64/machine-config-daemon start --node-name=$(hostname)
```

**Run controller locally**:
```bash
_output/linux/amd64/machine-config-controller --kubeconfig ~/.kube/config
```

**Deploying changes to cluster**:
```bash
# Build image
make image

# Push to registry
podman push localhost/machine-config-operator:latest quay.io/youruser/mco:test

# Update deployment
oc set image deployment/machine-config-operator -n openshift-machine-config-operator machine-config-operator=quay.io/youruser/mco:test
```

## Debugging

### Debugging MachineConfigDaemon

1. Check daemon logs on specific node:
```bash
oc logs -n openshift-machine-config-operator machine-config-daemon-<pod> -f
```

2. Debug on node:
```bash
oc debug node/<node-name>
chroot /host
systemctl status machine-config-daemon
journalctl -u machine-config-daemon -f
```

3. Check daemon state file:
```bash
cat /run/machine-config-daemon-state.json
```

### Debugging MachineConfigController

1. Check controller logs:
```bash
oc logs -n openshift-machine-config-operator machine-config-controller-<pod> -f
```

2. Check rendered configs:
```bash
oc get machineconfig | grep rendered
oc get machineconfig rendered-worker-<hash> -o yaml
```

3. Check pool status:
```bash
oc get machineconfigpool
oc describe machineconfigpool worker
```

### Common Issues

**Issue**: Daemon failing to apply config
**Cause**: Invalid Ignition config or file permissions
**Fix**: Check daemon logs for validation errors; verify MachineConfig spec

**Issue**: Pool stuck in "Updating" state
**Cause**: Node update failure, drain timeout, or maxUnavailable constraint
**Fix**: Check node conditions, daemon logs, and pool status.Conditions

**Issue**: Build fails with dependency errors
**Cause**: Outdated vendor/ directory
**Fix**: Run `make update-deps`

## Code Organization

```
cmd/                    # Entry points for components
pkg/daemon/            # On-node daemon logic
pkg/controller/        # Controller reconciliation
pkg/server/            # Ignition server
pkg/operator/          # Operator lifecycle management
test/                  # E2E test suites
vendor/                # Vendored dependencies
```

See [ARCHITECTURE.md](./ARCHITECTURE.md) for details.

## Making a Pull Request

1. Ensure all tests pass
2. Update documentation if changing user-facing behavior
3. Add ADR if making architectural decision
4. Create PR with clear description
5. Address review feedback

See docs/CONTRIBUTING.md for full process.

## Useful Commands

```bash
# Verify all checks (lint, format, tests)
make verify

# Update vendored dependencies
make update-deps

# Generate code (if changing API types)
make update

# Build specific component
WHAT=machine-config-daemon make build

# Cross-compile for different architecture
GOARCH=arm64 make
```

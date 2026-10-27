# MCO Development Guide

Component-specific development guidance. For generic OpenShift operator patterns, see the platform hub (`openshift/enhancements/ai-docs/`). For detailed upstream development instructions, see [docs/HACKING.md](docs/HACKING.md).

## Prerequisites

- Go 1.25.3+
- podman (for image builds)
- Access to an OpenShift cluster (for e2e tests)
- golangci-lint (installed automatically by `make lint`)

## Building

```bash
make binaries                          # All 8 binaries
make machine-config-operator           # Just the operator
make machine-config-daemon             # Just the daemon
make machine-config-controller         # Just the controller
make image                             # Container image via podman
make helpers                           # Developer tools (devex/cmd/)
```

Build embeds version info from `git describe`, commit hash, and build date via `hack/build-go.sh`.

## Dependencies

```bash
make go-deps                           # go mod tidy + vendor + verify + chmod scripts
```

Vendor directory is committed. All deps must be vendored. After updating `go.mod`, always run `make go-deps`.

## Templates

MCO generates MachineConfigs from Go templates in `templates/` (organized as `common/`, `master/`, `worker/`).

```bash
make update                            # Regenerate templates from vendor sources
make verify-templates                  # Check templates are current
```

Templates are generated from vendored systemd unit files. The Template Controller loads them from `/etc/mcc/templates` at runtime. When modifying templates, run `make update` followed by `make verify-templates`.

## Code Generation

CRD types are defined upstream in `github.com/openshift/api/machineconfiguration/v1/`. MCO vendors these types. To update:

1. Update the `openshift/api` dependency in `go.mod`
2. Run `make go-deps`
3. Run `make update` if template sources changed

Feature-gated CRD variants are generated upstream and vendored as `zz_generated.crd-manifests/`.

## Controller Development

All controllers live in `pkg/controller/`. Shared infrastructure is in `pkg/controller/common/`:

- `controller_context.go` - Shared informer factories, client sets
- `helpers.go` - MC manipulation, Ignition conversion, annotation helpers
- `metrics.go` - Prometheus metric registration
- `featuregates.go` - Feature gate detection

Controllers follow the standard pattern: watch resources via informers, enqueue on change, reconcile in `syncHandler`. Use `lib/resourceapply/` for idempotent create-or-update.

## Daemon Development

The MCD (`pkg/daemon/`) runs on every node. Key files:

- `daemon.go` (~110KB) - Main daemon loop, state machine
- `update.go` (~135KB) - Update orchestration, reboot/reload decisions
- `config_drift_monitor.go` - Unauthorized change detection
- `rpm-ostree.go` / `bootc.go` - OS update backends

To iterate on MCD without rebuilding the full image, see the "MCD Iteration" section in [docs/HACKING.md](docs/HACKING.md) which describes copying the binary directly to nodes.

## Developer Tools

Install with `make install-helpers`. Located in `devex/cmd/`:

| Tool | Purpose |
|------|---------|
| `mcdiff` | Diff two MachineConfigs to understand changes |
| `mco-push` | Push local MCO binary to a live cluster for testing |
| `wait-for-mcp` | Block until a MachineConfigPool finishes updating |
| `mco-builder` | Build custom OS images |
| `mco-sanitize` | Prepare a cluster for testing |
| `onclustertesting` | Run tests on a live cluster |
| `run-on-all-nodes` | Execute a command on all cluster nodes |

## Linting

```bash
make lint                              # golangci-lint (10m timeout)
```

Config in `.golangci.yml`. Supports `GOTAGS` for conditional compilation. CI produces checkstyle XML and JUnit output.

## Verification

```bash
make verify                            # All checks: lint, verify-e2e, verify-templates, verify-helpers
```

Run before submitting PRs. CI runs these same checks.

## AMI Updates

```bash
make update-amis                       # Sync AMI mappings from openshift/installer
```

Updates the AMI list in `pkg/controller/bootimage/ami.go` from the installer's region-to-AMI mappings.

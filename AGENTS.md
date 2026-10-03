# Machine Config Operator (MCO) - Agent Entry Point

## What This Component Does

MCO manages node-level OS and configuration for OpenShift clusters. It owns the full lifecycle: generating Ignition configs from CRDs, rendering merged configs per pool, serving configs to bootstrapping nodes, and applying them on each node via a daemonset agent (MCD). It also supports on-cluster layering (OCL) where custom OS images are built from Containerfiles.

## Repository Layout

```
cmd/                          # 7 binaries (operator, controller, daemon, server, machine-os-builder, osimagestream, apiserver-watcher)
pkg/controller/               # All controllers (render, template, node, build, kubelet-config, container-runtime-config, bootimage, certrotation, pinnedimageset, drain, internalreleaseimage)
pkg/controller/common/        # Shared controller context, helpers, metrics, feature gates
pkg/daemon/                   # Machine Config Daemon - node agent that applies configs
pkg/server/                   # Machine Config Server - serves Ignition to bootstrapping nodes
pkg/operator/                 # Top-level operator sync, status, rendering
pkg/osimagestream/            # OS image stream provider and metadata
templates/                    # Ignition/systemd templates (common/, master/, worker/)
manifests/                    # Deployed manifests, RBAC, ValidatingAdmissionPolicies
test/                         # E2E suites (e2e-1of2, e2e-2of2, e2e-techpreview, e2e-bootstrap, e2e-ocl-*, e2e-single-node)
test/framework/               # envtest setup, client initialization
test/helpers/                 # Test builders, assertions, utilities
devex/cmd/                    # Developer tools (mcdiff, mco-push, wait-for-mcp, etc.)
hack/                         # Build, test, lint, template generation scripts
lib/                          # resourceapply, resourceread utilities
```

## CRDs (API group: machineconfiguration.openshift.io/v1)

| CRD | Purpose | Key Controller |
|-----|---------|----------------|
| MachineConfig | Ignition config fragment (files, units, kernel args, extensions) | Render |
| MachineConfigPool | Groups nodes by label selector, tracks update status | Node |
| ControllerConfig | Cluster-wide config (DNS, proxy, images, certs) | Template |
| KubeletConfig | Kubelet tuning applied per pool | KubeletConfig |
| ContainerRuntimeConfig | CRI-O/podman tuning per pool | ContainerRuntimeConfig |
| MachineConfigNode | Per-node update progress and state | Node/Daemon |
| MachineOSConfig | OCL build inputs (Containerfile, pool ref) | Build |
| MachineOSBuild | OCL build execution and status | Build |
| OSImageStream | OS image references per release | OSImageStream |
| PinnedImageSet | Pre-pulled container images per pool | PinnedImageSet |

## Update Flow

1. Template Controller renders MachineConfigs from ControllerConfig + templates
2. KubeletConfig/ContainerRuntimeConfig controllers generate additional MachineConfigs
3. Render Controller merges all MachineConfigs for each pool into a rendered config
4. Node Controller assigns target config to nodes, coordinates drain/reboot
5. MCD (daemon) on each node fetches and applies the config (Ignition, rpm-ostree/bootc, reboot)
6. For OCL: Build Controller creates Jobs to build layered OS images from MachineOSConfig

## Key Development Commands

```bash
make binaries                 # Build all components
make test-unit                # Unit tests (no cluster needed)
make bootstrap-e2e            # Bootstrap tests via envtest
make test-e2e                 # Full e2e (requires cluster)
make lint                     # golangci-lint
make verify                   # All verification checks
make update                   # Regenerate templates from vendor
make go-deps                  # go mod tidy + vendor
make helpers                  # Build devex tools
```

## Component-Specific Documentation

- [ai-docs/domain/](ai-docs/domain/) - CRD details and domain model
- [ai-docs/architecture/components.md](ai-docs/architecture/components.md) - Component internals
- [ai-docs/decisions/](ai-docs/decisions/) - Architecture decisions
- [ai-docs/references/ecosystem.md](ai-docs/references/ecosystem.md) - Platform doc links
- [MCO_DEVELOPMENT.md](MCO_DEVELOPMENT.md) - Development guide
- [MCO_TESTING.md](MCO_TESTING.md) - Testing guide
- [docs/HACKING.md](docs/HACKING.md) - Upstream development guide

## Platform Documentation

Generic OpenShift operator patterns, testing practices, and security guidelines live in the platform hub at `openshift/enhancements/ai-docs/`. This repo documents only MCO-specific knowledge.

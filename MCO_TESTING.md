# MCO Testing Guide

Component-specific testing guidance. For generic OpenShift testing patterns, see the platform hub (`openshift/enhancements/ai-docs/`).

## Test Suites Overview

| Suite | Command | Cluster Required | Timeout | Description |
|-------|---------|-----------------|---------|-------------|
| Unit | `make test-unit` | No | - | Tests in `pkg/`, `cmd/`, `lib/`, `devex/`, `test/helpers/` |
| Bootstrap | `make bootstrap-e2e` | No (envtest) | - | Verifies bootstrap rendering matches controller rendering |
| E2E Full | `make test-e2e` | Yes | 190m | All e2e tests (2 shards, parallel) |
| E2E Shard 1 | `make test-e2e-1of2` | Yes | 100m | MCD, systemd, daemon tests |
| E2E Shard 2 | `make test-e2e-2of2` | Yes | 100m | Controller, containers, node OS, extensions |
| Tech Preview | `make test-e2e-techpreview` | Yes | 170m | Feature-gated functionality |
| Single Node | `make test-e2e-single-node` | Yes (SNO) | - | Single-node cluster scenarios |
| OCL Shard 1 | `make test-e2e-ocl-1of2` | Yes (OCL) | - | On-cluster layering tests (part 1) |
| OCL Shard 2 | `make test-e2e-ocl-2of2` | Yes (OCL) | - | On-cluster layering tests (part 2) |
| IRI | `make test-e2e-iri` | Yes | - | Internal Release Image runtime tests |

## Unit Tests

```bash
make test-unit
```

Runs via `hack/test-unit.sh`. Covers all Go packages except e2e directories. Generates JUnit XML output when `ARTIFACT_DIR` is set. Coverage reports are produced when running in CI.

Unit test files live alongside the code they test (standard Go convention).

## Bootstrap Tests (envtest)

```bash
make bootstrap-e2e
```

Uses controller-runtime's envtest (real etcd + kube-apiserver, no full cluster). Verifies that:
- Bootstrap manifest parsing works correctly
- Bootstrap-rendered configs match what the controller would produce
- Pre-built image validation is correct

Test data in `pkg/controller/bootstrap/testdata/bootstrap/`. Kubebuilder tools version: 1.32.1.

The envtest setup installs MCO CRDs, OpenShift config CRDs, and operator CRDs into the test environment.

## E2E Tests

E2E tests require a running OpenShift cluster with MCO deployed. Tests are split into shards for CI parallelism.

**Framework:** Ginkgo v2 + Gomega  
**Location:** `test/e2e-*of*/ directories`  
**Shared code:** `test/e2e-shared-tests/`, `test/helpers/`

### Test Organization

- `test/e2e-1of2/` - Daemon-focused: MCD apply, systemd units, file writes
- `test/e2e-2of2/` - Controller-focused: rendering, container runtime, node OS, extensions
- `test/e2e-techpreview/` - Feature-gated tests (require TechPreview cluster)
- `test/e2e-ocl-*of*/` - On-cluster layering tests (require OCL-enabled cluster)

### Test Helpers (`test/helpers/`)

- `assertions.go` - Custom Gomega matchers and assertion helpers
- `nodebuilder.go`, `poolbuilder.go` - Fluent builders for test objects
- `idempotent.go` - Idempotency verification utilities
- `archiveartifact.go` - Artifact collection for CI
- `emptyimage.go` - Creates empty container images for testing

### Running E2E Tests Locally

1. Ensure `KUBECONFIG` points to your cluster
2. Run desired suite: `make test-e2e-1of2`
3. Tests run sequentially (`-p 1`), fail-fast enabled
4. Artifacts collected to `ARTIFACT_DIR` if set

## Verification (Compile Checks)

```bash
make verify-e2e                        # Compile-check all e2e suites
make verify                            # All checks (lint + verify-e2e + verify-templates + verify-helpers)
```

`verify-e2e` compiles but does not run e2e tests. Catches build errors without requiring a cluster.

## CI Integration

CI uses OpenShift CI (ci-operator). Config in `.ci-operator.yaml`.

- Unit tests and linting run on every PR
- E2E tests run against provisioned clusters
- JUnit XML output via `hack/test-with-junit.sh`
- Artifacts collected to `ARTIFACT_DIR`

## Writing New Tests

**Unit tests:** Place `_test.go` files alongside the code. Use standard Go testing or testify.

**E2E tests:** Add to the appropriate shard directory. Use the test framework:
```go
import (
    "github.com/openshift/machine-config-operator/test/helpers"
    "github.com/openshift/machine-config-operator/test/framework"
)
```

Use builders from `test/helpers/` for creating test objects. Use `test/e2e-shared-tests/` for test logic shared across suites.

**Bootstrap tests:** Add test cases to `test/e2e-bootstrap/` using envtest framework from `test/framework/envtest.go`.

# Testing Strategy

## Test Pyramid

```
       /\
      /E2E\       Small number, full system validation
     /------\
    / Integ  \    Medium number, component integration
   /----------\
  / Unit Tests \  Large number, fast, isolated
 /--------------\
```

## Test Organization

### Unit Tests

**Location**: `*_test.go` files alongside implementation
**Run**: `make test-unit`
**Coverage Target**: >70% (check with `make test-unit WHAT=-cover`)

**Purpose**: Test individual functions and methods in isolation

**Pattern**:
```go
func TestRenderController_Render(t *testing.T) {
    // Arrange: Set up test data
    configs := []machineconfigv1.MachineConfig{...}

    // Act: Call function under test
    rendered, err := Render(configs)

    // Assert: Verify results
    assert.NoError(t, err)
    assert.Equal(t, expected, rendered)
}
```

**Example**: `pkg/controller/render/render_controller_test.go`

### Integration Tests

**Location**: `test/e2e-*` directories
**Run**: `make test-e2e` (requires cluster)

**Purpose**: Test interactions between MCO components and Kubernetes API

**Scenarios**:
- MachineConfig creation triggers rendering
- MachineConfigPool updates trigger node updates
- KubeletConfig creates corresponding MachineConfig

### Bootstrap Tests

**Location**: `test/e2e-bootstrap/`
**Run**: `make test-bootstrap`

**Purpose**: Verify bootstrap and controller code paths produce identical output

**Why critical**: Bootstrap runs during cluster install (offline), controllers run after (online). Divergence breaks installation.

**How it works**:
1. Start envtest environment (simulated API server)
2. Create test manifests (FeatureGates, KubeletConfigs, etc.)
3. Run actual MCC controllers
4. Run bootstrap process against same manifests
5. Compare rendered MachineConfig names

**Example**: `test/e2e-bootstrap/bootstrap_test.go`

See [docs/HACKING.md#bootstrap-unit-tests](../docs/HACKING.md#bootstrap-unit-tests) for details.

### E2E Tests

**Location**: `test/e2e*/`
**Run**: `make test-e2e`

**Purpose**: Validate end-to-end user journeys in real cluster

**Suites**:
- **e2e**: Core MCO functionality (MachineConfig creation, updates, pools)
- **e2e-bootstrap**: Bootstrap validation
- **e2e-techpreview**: Tech preview features

**Example test**: Create MachineConfig → verify rendered → verify applied to nodes → verify rollback works

## Writing Tests

### For New Features

1. **Unit tests** for new code (controllers, daemon logic)
2. **Integration tests** if touching Kubernetes API
3. **E2E tests** for user-facing changes (new CRDs, update behavior)
4. **Bootstrap tests** if affecting MachineConfig rendering

### For Bug Fixes

1. Write a **failing test** that reproduces the bug
2. Fix the bug
3. Verify test passes
4. Ensure no regressions with `make test-unit`

## Running Tests Locally

```bash
# All unit tests
make test-unit

# Specific package
go test -v ./pkg/daemon/...

# With coverage
make test-unit WHAT=-cover

# Single test
go test -v ./pkg/controller/render -run TestRender

# E2E (requires cluster)
make test-e2e

# Bootstrap tests
make test-bootstrap
```

## CI Test Execution

**Pull Request CI**:
- `unit`: Runs `make test-unit`
- `e2e-aws`: Creates cluster, runs OpenShift origin e2e tests
- `e2e-aws-op`: Creates cluster, runs `make test-e2e` (MCO-specific tests)

**What runs where**:
- Unit tests: Always (fast, no cluster needed)
- E2E tests: On PR (slower, requires cluster provisioning)
- Destructive tests: Only in `e2e-aws-op` (can break things)

See [docs/HACKING.md#the-test-suites](../docs/HACKING.md#the-test-suites) for details.

## Test Data

**Location**: Various `testdata/` directories

**Examples**:
- `pkg/controller/bootstrap/testdata/bootstrap/`: Bootstrap manifests
- `pkg/server/testdata/`: MCS test fixtures
- `pkg/controller/template/test_data/`: Controller config samples

## Troubleshooting Test Failures

**Flaky tests**: Re-run to confirm, file issue if consistently flaky
**Timeout issues**: Increase timeout or optimize test
**CI-only failures**: Check for timing issues, resource constraints

## Test Coverage

Check coverage with:
```bash
make test-unit WHAT=-cover
go tool cover -html=coverage.out  # View coverage report
```

**Target**: >70% coverage for new code

## Related Documentation

- [HACKING.md](../docs/HACKING.md) - Development and testing workflows
- [Bootstrap Tests](../docs/HACKING.md#bootstrap-unit-tests) - Bootstrap test details

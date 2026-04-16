# Testing Strategy

## Test Pyramid

```
       /\
      /E2E\       [Small number, full system, slow]
     /------\
    /  Integ \    [Medium number, component integration]
   /----------\
  /  Unit Tests \  [Large number, fast, isolated]
 /--------------\
```

## Test Organization

### Unit Tests

**Location**: `pkg/*_test.go` (alongside source files)
**Run**: `make test-unit`
**Coverage Target**: >70%

**Pattern**:
```go
func TestDaemonUpdate(t *testing.T) {
    // Setup
    mockClient := fakeclient.NewClient()
    daemon := NewDaemon(mockClient)
    
    // Exercise
    err := daemon.Update(newConfig)
    
    // Verify
    assert.NoError(t, err)
    assert.Equal(t, expectedState, daemon.State())
}
```

**Key test packages**:
- pkg/daemon/*_test.go - Daemon reconciliation logic
- pkg/controller/template/*_test.go - Config rendering
- pkg/server/*_test.go - Ignition serving

### Integration Tests

**Location**: `test/integration/`
**Run**: `make test-integration`

**Purpose**: Test component interactions without full cluster

**Example**:
- Controller + API server interaction
- Daemon + rpm-ostree client interaction

### E2E Tests

**Location**: `test/e2e*/`
**Run**: `make test-e2e` (requires OpenShift cluster)

**Suites**:
- **e2e-agnostic**: Platform-independent tests
  - MachineConfig creation and rendering
  - Pool updates and rollbacks
  - Daemon reconciliation
  
- **e2e-<platform>**: Platform-specific tests (AWS, Azure, GCP)
  - First-boot provisioning
  - Platform-specific configuration

**Test Framework**: Ginkgo + Gomega

**Example**:
```go
Describe("MachineConfig", func() {
    It("should apply configuration to nodes", func() {
        By("Creating a MachineConfig")
        mc := &mcfgv1.MachineConfig{...}
        Expect(client.Create(ctx, mc)).To(Succeed())
        
        By("Waiting for pool update")
        Eventually(func() bool {
            pool := &mcfgv1.MachineConfigPool{}
            client.Get(ctx, poolName, pool)
            return pool.Status.Updated == pool.Status.MachineCount
        }, 10*time.Minute).Should(BeTrue())
    })
})
```

## Writing Tests

### For New Features

1. Write unit tests for new functions/methods
2. Add integration tests for cross-component interactions
3. Add E2E tests for user-facing behavior
4. Ensure >70% code coverage

### For Bug Fixes

1. Write a failing test that reproduces the bug
2. Fix the bug
3. Verify test now passes
4. Add regression test to appropriate suite

## Running Tests Locally

```bash
# All unit tests
make test-unit

# Specific package
go test ./pkg/daemon/...

# Specific test
go test ./pkg/daemon -run TestUpdate

# With coverage
make test-unit COVERAGE=true
go tool cover -html=coverage.out

# E2E (requires cluster)
make test-e2e

# Specific E2E suite
make test-e2e-agnostic
```

## CI Test Execution

**GitHub Actions / CI Pipeline**:
1. **verify**: Linting, formatting, generated code checks
2. **unit**: All unit tests (`make test-unit`)
3. **e2e-agnostic**: Platform-independent E2E tests
4. **e2e-<platform>**: Platform-specific E2E tests (AWS, Azure, GCP)

**Triggers**:
- Every PR commit
- Merge to master
- Nightly on master

## Test Data

**Location**: `test/fixtures/`
**Format**: YAML manifests for MachineConfigs, Pools, etc.

**Example**:
```
test/fixtures/
  machineconfigs/
    - basic-mc.yaml
    - with-ignition.yaml
  machineconfigpools/
    - worker-pool.yaml
    - custom-pool.yaml
```

## Troubleshooting Test Failures

**Flaky tests**: 
- Check for race conditions
- Add explicit waits instead of sleep
- Use Eventually() for async operations

**Timeout issues**: 
- Increase timeout for slow operations (rpm-ostree, node reboots)
- Check cluster resource constraints
- Verify network connectivity

**E2E cleanup failures**:
- Ensure proper test teardown (defer cleanup)
- Check for resource leaks (pods, configs not deleted)

## Test Coverage Reports

Generate coverage report:
```bash
make test-unit COVERAGE=true
go tool cover -html=coverage.out -o coverage.html
```

View in browser: `firefox coverage.html`

**Coverage goals**:
- Critical packages (daemon, controller): >80%
- All packages: >70%
- New code: 100% (all new functions tested)

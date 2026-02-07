# Extended Privileged Tests

This directory contains extended test suites that run using the openshift-tests-extension framework. These tests are primarily designed to run in CI environments but can be run locally with some modifications.

## Running Tests Locally

### Prerequisites

1. A running OpenShift cluster with appropriate feature gates enabled
2. Valid KUBECONFIG exported to your environment
3. Golang installed

### Step 1: Temporary Code Modification (Required)

The `machine-config-tests-ext` binary uses the openshift-tests-extension framework which conflicts with the standard e2e test flag registration. To run locally, you must temporarily comment out one line:

**File:** `cmd/machine-config-tests-ext/main.go` (Line 104)

```go
// TODO: TEMPORARY - Comment out for local testing only. UNCOMMENT before commit!
// exutil.InitStandardFlags()
```
**This modification is ONLY for local testing. DO NOT commit this change. The line must remain uncommented in production code.**

### Step 2: Build the Test Binary

From the repository root run `go build ./cmd/machine-config-tests-ext/` This creates a machine-config-tests-ext binary in the current directory.

### Step 3: Export KUBECONFIG

`export KUBECONFIG=/path/to/your/kubeconfig`

### Step 4: Run Test Suites

Run a specific test suite
`./machine-config-tests-ext run-suite openshift/machine-config-operator/<suite-name>`

Examples:
```
./machine-config-tests-ext run-suite openshift/machine-config-operator/iri
./machine-config-tests-ext run-suite openshift/machine-config-operator/disruptive
./machine-config-tests-ext run-suite openshift/machine-config-operator/longduration
```

### Step 5: Cleanup After Testing

Before committing any changes:

- Restore the InitStandardFlags line
`git restore cmd/machine-config-tests-ext/main.go`

- Verify no changes remain
`git diff cmd/machine-config-tests-ext/main.go`

Or check git status
`git status`

## Test Suites

### InternalReleaseImage (IRI) Tests

Location: test/extended-priv/mco_internalreleaseimage.go

Suite: openshift/machine-config-operator/iri

*Special Requirements:*

- Verify the feature gate is enabled
`oc get featuregate cluster -o jsonpath='{.status.featureGates[0].enabled[*].name}' | grep NoRegistryClusterInstall`

- Verify IRI Resource Exists:
`oc get internalreleaseimage`

### Complete Local Testing Workflow for IRI:

1. Deploy an OVE compact IPv4 cluster (disconnected)

2. Export the kubeconfig

`export KUBECONFIG=/path/to/ove/cluster/kubeconfig`

3. Comment out `InitStandardFlags()` in `main.go` (see Step 1 above)

4. Build the test binary

`go build ./cmd/machine-config-tests-ext/`

5. Run IRI test suite

`./machine-config-tests-ext run-suite openshift/machine-config-operator/iri`

6. Before committing, restore main.go

`git restore cmd/machine-config-tests-ext/main.go`

### Other Test Suites

Additional test suites can be found in:
- Disruptive tests: Tests marked with `[Suite:openshift/machine-config-operator/disruptive]`
- Long duration tests: Tests marked with `[Suite:openshift/machine-config-operator/longduration]`

Each suite may have specific platform or feature gate requirements - check test tags for details.

### Alternative: Run Tests in CI

If you prefer not to modify code locally, the tests will automatically run in CI when you submit a PR. The CI environment is properly configured to run these tests without code modifications.

## Troubleshooting

1. `Error: "panic: flag redefined: kubeconfig"`

    *Cause:* You forgot to comment out exutil.InitStandardFlags() in main.go.

    *Solution:* Follow Step 1 above to comment out the line, then rebuild the test binary.


2. Test Skipped: "Featuregate NoRegistryClusterInstall is not enabled"

    *Cause:* The required feature gate is not enabled on your cluster.

    *Solution:* Enable the feature gate:
    ```
    oc patch featuregate cluster --type=merge -p '{
      "spec": {
        "customNoUpgrade": {
        "enabled": ["NoRegistryClusterInstall"]
        }
      }
    }'
    ```

3. Test Skipped: Platform Not Supported

    *Cause:* Some tests are platform-specific (e.g., BareMetal only for certain IRI tests).

    *Solution:* The test will skip automatically. This is expected behavior if you're not on the required platform.

4. Error: "internalreleaseimage 'cluster' not found"

    *Cause:* Your cluster doesn't have the IRI resource (likely not an OVE cluster or feature gate NoRegistryClusterInstall not enabled).

    *Solution:*
    1. Verify you're using an OVE cluster deployed with Agent-based Installer
    2. Verify the feature gate is enabled
    3. Check if the resource exists: oc get internalreleaseimage

## Contributing New Tests

When adding new tests to test/extended-priv/:

1. Follow existing test patterns:
See mco_bootimages.go, mco_machineconfignode.go, mco_internalreleaseimage.go
2. Use standard skip functions: `SkipIfNoFeatureGate(oc, "FeatureGateName")` , `SkipIfSNO(oc)`, `SkipTestIfWorkersCannotBeScaled(oc)`
3. Tag tests appropriately:
- `[OCPFeatureGate:FeatureName]` - requires specific feature gate
- `[Disruptive]` - may disrupt cluster operations
- `[Platform:PlatformName]` - platform-specific (aws, gcp, baremetal, etc.)
- `[Suite:openshift/machine-config-operator/suitename]` - suite grouping
4. Create helper functions in separate files:
- Example: internalreleaseimage.go for IRI test helpers
- Keep test files clean and focused on test logic
5. Register your test suite in `cmd/machine-config-tests-ext/main.go`:
    ```
    ext.AddSuite(e.Suite{
        Name:"openshift/machine-config-operator/mysuite",
        Qualifiers: []string{
            `name.contains("MyFeature")`,
        },
        ClusterStability: e.ClusterStabilityDisruptive,
        TestTimeout:&defaultTimeout,
        Description: `Description of what this suite tests`,
    })
    ```
6. Example test structure:
    ```
    var _ = g.Describe("[sig-mco][Suite:openshift/machine-config-operator/mysuite] MyFeature", func() {
    defer g.GinkgoRecover()

    var oc = exutil.NewCLI("my-test", exutil.KubeConfigPath())

    g.JustBeforeEach(func() {
        SkipIfNoFeatureGate(oc.AsAdmin(), "MyFeatureGate")
        PreChecks(oc)
    })

    g.It("should validate my feature [OCPFeatureGate:MyFeatureGate]", func() {
        // Test implementation
        exutil.By("Step 1: Setup")
        // ...

        exutil.By("Step 2: Verify")
            o.Expect(err).NotTo(o.HaveOccurred())
            logger.Infof("OK!\n")
        })
    })
    ```
Test Development Best Practices

1. Always run `PreChecks(oc)` in JustBeforeEach - ensures cluster is stable
2. Use defer for cleanup - ensures resources are cleaned up even on failure
3. Use descriptive test names - include PolarionID if available
4. Log progress with `exutil.By()` - helps with debugging failures
5. Use `logger.Infof()` for status updates - visible in test output

## Additional Resources
- OpenShift Tests Extension: https://github.com/openshift-eng/openshift-tests-extension
- MCO Documentation: https://docs.openshift.com/container-platform/latest/architecture/control-plane.html#machine-config-operator_control-plane
- Feature Gates: https://docs.openshift.com/container-platform/latest/nodes/clusters/nodes-cluster-enabling-features.html

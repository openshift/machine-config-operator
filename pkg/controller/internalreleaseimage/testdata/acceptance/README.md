# IRI Aggregation Acceptance Criteria

This directory contains CSV files that define the acceptance criteria for the InternalReleaseImage (IRI) aggregation feature. These scenarios were used to verify the implementation during development.

## Test Scenarios

### Passing Scenarios

These scenarios represent the expected behavior of the IRI aggregation controller:

- **happy-path.csv**: All registries are healthy and accessible
  - All MCNs report `InternalReleaseImageDegraded=False`
  - IRI cluster-level status: `Degraded=False`, `Reason=AllReleasesAvailable`
  - All releases are available via api-int

- **nodes-not-ready.csv**: One or more nodes are not ready
  - Some nodes have `Ready=False` condition
  - IRI cluster-level status: `Degraded=True`, `Reason=SomeNodesUnavailable`
  - Message includes list of not-ready nodes

- **registry-unavailable-on-api-int.csv**: The api-int registry is unreachable
  - api-int registry ping fails
  - IRI cluster-level status: `Degraded=True`, `Reason=ApiIntNotAvailable`
  - All releases marked unavailable

- **registry-unavailable-not-on-api-int.csv**: Registry unavailable on some nodes but api-int is accessible
  - Some MCNs report `InternalReleaseImageDegraded=True`
  - IRI cluster-level status: `Degraded=True`, `Reason=SomeRegistriesUnavailable`
  - Message includes list of degraded nodes

### Non-Passing Scenarios

- **all-registries-unavailable.csv**: All node registries are down
  - This is functionally equivalent to `registry-unavailable-on-api-int.csv`
  - Expected to produce the same result (api-int unavailable)
  - Kept for documentation purposes showing this edge case maps to existing scenario

## CSV Format

Each CSV defines:
1. **Scenario description**: Given/When/Then style acceptance criteria
2. **MachineConfigNode status**: Expected conditions and release status for each control plane node
3. **InternalReleaseImage status**: Expected cluster-level aggregated status

## Related Code

The implementation of these scenarios is in:
- `pkg/controller/internalreleaseimage/aggregation.go` - Aggregation logic
- `pkg/controller/internalreleaseimage/internalreleaseimage_controller.go` - Controller and event handlers

## Verification

To verify these scenarios manually:
1. Set up an OVE cluster with 3 control plane nodes
2. For each scenario, configure the cluster to match the "Given" conditions
3. Verify the MachineConfigNode and InternalReleaseImage resources match expected status

To verify programmatically, use the verify-iri-aggregation skill (if available):
```bash
/verify-iri-aggregation
```

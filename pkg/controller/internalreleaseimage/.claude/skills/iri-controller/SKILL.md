---
name: iri-controller
description: The InternalReleaseImage controller manages the IRI resource lifecycle, generates MachineConfigs for the IRI registry, updates status by aggregating from MachineConfigNodes, and handles deletion. Use when reviewing controller implementation or validating behaviors.
disable-model-invocation: true
allowed-tools: Read Grep
---

# Verify InternalReleaseImage Controller Implementation

Verify that the InternalReleaseImage (IRI) controller implementation correctly handles all acceptance criteria defined in test scenarios.

## IRI Aggregation Behavior

Verify that the IRI aggregation implementation correctly handles all acceptance criteria defined in the CSV test scenarios.

### Scenarios to Verify

See [testdata/acceptance/README.md](../../testdata/acceptance/README.md) for complete scenario descriptions.

The skill verifies the implementation matches these acceptance criteria by checking code paths, status constants, condition handling, and message formatting.

### Verification Steps

For each scenario:

1. **Read the CSV file** to understand the expected behavior
2. **Search aggregation.go** for the relevant code paths:
   - `aggregateMCNIRIStatus()` - main aggregation function
   - `checkAPIIntRegistryAvailability()` - api-int health check
   - `processMCNReleases()` - MCN status processing
   - `buildAggregatedReleases()` - final status construction
3. **Verify status constants** match CSV expectations:
   - `IRIStatusAllReleasesAvailable`
   - `IRIStatusAPIIntNotAvailable`
   - `IRIStatusSomeNodesNotAvailable`
   - `IRIStatusSomeRegistriesUnavailable`
4. **Check condition handling** in `updateDegradedCondition()`
5. **Verify message formatting** includes node lists in brackets with commas

### Code Locations to Check

Primary implementation:
- `pkg/controller/internalreleaseimage/aggregation.go`
- `pkg/controller/internalreleaseimage/internalreleaseimage_controller.go`

Event handlers that trigger aggregation:
- `updateMachineConfigNode()` - watches for MCN status changes
- `updateNode()` - watches for node Ready condition changes

### Report Format

For each scenario, report:
- ✅ **PASS**: Code correctly implements the scenario
- ⚠️  **PARTIAL**: Code partially implements but missing details
- ❌ **FAIL**: Code does not match expected behavior
- 📝 **Notes**: Any observations or edge cases

Include:
- Which code section handles the scenario
- How the expected status/reason/message is generated
- Any gaps or improvements needed

### Example Verification

For "happy-path.csv":
1. Read the CSV expectations
2. Verify `IRIStatusAllReleasesAvailable` is returned when:
   - All MCNs have `InternalReleaseImageDegraded=False`
   - api-int registry ping succeeds
   - All nodes are ready
3. Confirm message: "All the release images are available"
4. Check that releases use api-int URL format

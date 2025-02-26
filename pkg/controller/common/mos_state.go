package common

import (
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	"github.com/openshift/machine-config-operator/pkg/apihelpers"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// This is intended to provide a singular way to interrogate MachineConfigPool
// objects to determine if they're in a specific state or not. The eventual
// goal is to use this to mutate the MachineConfigPool object to provide a
// single and consistent interface for that purpose. In this current state, we
// do not perform any mutations.
type MachineOSBuildState struct {
	Build *mcfgv1.MachineOSBuild
}

type MachineOSConfigState struct {
	Config *mcfgv1.MachineOSConfig
}

func NewMachineOSConfigState(mosc *mcfgv1.MachineOSConfig) *MachineOSConfigState {
	return &MachineOSConfigState{
		Config: mosc,
	}
}

func NewMachineOSBuildState(mosb *mcfgv1.MachineOSBuild) *MachineOSBuildState {
	return &MachineOSBuildState{
		Build: mosb,
	}
}

func NewMachineOSBuildStateFromStatus(status mcfgv1.MachineOSBuildStatus) *MachineOSBuildState {
	return &MachineOSBuildState{
		Build: &mcfgv1.MachineOSBuild{
			Status: status,
		},
	}
}

// Returns the OS image, if one is present.
func (c *MachineOSConfigState) GetOSImage() string {
	osImage := string(c.Config.Status.CurrentImagePullSpec)
	return osImage
}

// Determines if a given MachineConfigPool has an available OS image. Returns
// false if the annotation is missing or set to an empty string.
func (c *MachineOSConfigState) HasOSImage() bool {
	val := string(c.Config.Status.CurrentImagePullSpec)
	return val != ""
}

// Clears the image pullspec annotation.
func (c *MachineOSConfigState) ClearImagePullspec() {
	c.Config.Spec.RenderedImagePushSpec = ""
	c.Config.Status.CurrentImagePullSpec = ""
}

// Clears all build object conditions.
func (b *MachineOSBuildState) ClearAllBuildConditions() {
	b.Build.Status.Conditions = clearAllBuildConditions(b.Build.Status.Conditions)
}

// Determines if an OS image build is a success.
func (b *MachineOSBuildState) IsBuildSuccess() bool {
	return apihelpers.IsMachineOSBuildConditionTrue(b.Build.Status.Conditions, mcfgv1.MachineOSBuildSucceeded)
}

// Determines if an OS image build is pending.
func (b *MachineOSBuildState) IsBuildPending() bool {
	return apihelpers.IsMachineOSBuildConditionTrue(b.Build.Status.Conditions, mcfgv1.MachineOSBuilding)
}

// Determines if an OS image build is prepared.
func (b *MachineOSBuildState) IsBuildPrepared() bool {
	return apihelpers.IsMachineOSBuildConditionTrue(b.Build.Status.Conditions, mcfgv1.MachineOSBuildPrepared)
}

// Determines if an OS image build is in progress.
func (b *MachineOSBuildState) IsBuilding() bool {
	return apihelpers.IsMachineOSBuildConditionTrue(b.Build.Status.Conditions, mcfgv1.MachineOSBuilding)
}

// Determines if an OS image build has failed.
func (b *MachineOSBuildState) IsBuildFailure() bool {
	return apihelpers.IsMachineOSBuildConditionTrue(b.Build.Status.Conditions, mcfgv1.MachineOSBuildFailed)
}

// Determines if an OS image build has failed.
func (b *MachineOSBuildState) IsBuildInterrupted() bool {
	return apihelpers.IsMachineOSBuildConditionTrue(b.Build.Status.Conditions, mcfgv1.MachineOSBuildInterrupted)
}

// Determines if an OS image build has build conditions set on it.
func (b *MachineOSBuildState) HasBuildConditions() bool {
	return len(b.Build.Status.Conditions) != 0
}

// Determines if an OS image build is in its initial state with all conditions false.
func (b *MachineOSBuildState) IsInInitialState() bool {
	for _, status := range getMachineConfigBuildConditions() {
		if apihelpers.IsMachineOSBuildConditionTrue(b.Build.Status.Conditions, status) {
			return false
		}
	}

	return true
}

// Determines if an OS image build is in its terminal state where build success, build failure, or build interrupted condition is set.
func (b *MachineOSBuildState) IsInTerminalState() bool {
	return b.GetTerminalState() != ""
}

// Determines if an OS image build is in a transient state where it is either prepared, pending, or running.
func (b *MachineOSBuildState) IsInTransientState() bool {
	return b.GetTransientState() != ""
}

// Gets the transient state, if any is set. Otherwise, returns an empty string.
func (b *MachineOSBuildState) GetTransientState() mcfgv1.BuildProgress {
	for transientState := range MachineOSBuildTransientStates() {
		if apihelpers.IsMachineOSBuildConditionTrue(b.Build.Status.Conditions, transientState) {
			return transientState
		}
	}

	return ""
}

// Gets the current terminal state, if any is set. Otherwise, returns an empty string.
func (b *MachineOSBuildState) GetTerminalState() mcfgv1.BuildProgress {
	for terminalState := range MachineOSBuildTerminalStates() {
		if apihelpers.IsMachineOSBuildConditionTrue(b.Build.Status.Conditions, terminalState) {
			return terminalState
		}
	}

	return ""
}

func (b *MachineOSBuildState) IsAnyDegraded() bool {
	condTypes := []mcfgv1.BuildProgress{
		mcfgv1.MachineOSBuildFailed,
		mcfgv1.MachineOSBuildInterrupted,
	}

	for _, condType := range condTypes {
		if apihelpers.IsMachineOSBuildConditionTrue(b.Build.Status.Conditions, condType) {
			return true
		}
	}

	return false
}

// Idempotently sets the supplied build conditions.
func (b *MachineOSBuildState) SetBuildConditions(conditions []metav1.Condition) {
	for _, condition := range conditions {
		condition := condition
		currentCondition := apihelpers.GetMachineOSBuildCondition(b.Build.Status, mcfgv1.BuildProgress(condition.Type))
		if currentCondition != nil && isConditionEqual(*currentCondition, condition) {
			continue
		}

		mosbCondition := apihelpers.NewMachineOSBuildCondition(condition.Type, condition.Status, condition.Reason, condition.Message)
		apihelpers.SetMachineOSBuildCondition(&b.Build.Status, *mosbCondition)
	}
}

// Returns a map of the buildprogress states to their expected conditions for
// transient states. That is, the MachineOSBuild is expected to transition from
// one state to another.
func MachineOSBuildTransientStates() map[mcfgv1.BuildProgress][]metav1.Condition {
	return map[mcfgv1.BuildProgress][]metav1.Condition{
		mcfgv1.MachineOSBuilding:      apihelpers.MachineOSBuildRunningConditions(),
		mcfgv1.MachineOSBuildPrepared: apihelpers.MachineOSBuildPendingConditions(),
	}
}

// Returns a map of the buildprogress states to their expected conditions for
// terminal states; meaning that the MachineOSBuild cannot transition from one
// state to another.
func MachineOSBuildTerminalStates() map[mcfgv1.BuildProgress][]metav1.Condition {
	return map[mcfgv1.BuildProgress][]metav1.Condition{
		mcfgv1.MachineOSBuildSucceeded:   apihelpers.MachineOSBuildSucceededConditions(),
		mcfgv1.MachineOSBuildFailed:      apihelpers.MachineOSBuildFailedConditions(),
		mcfgv1.MachineOSBuildInterrupted: apihelpers.MachineOSBuildInterruptedConditions(),
	}
}

// Determines if two conditions are equal. Note: I purposely do not include the
// timestamp in the equality test, since we do not directly set it.
func isConditionEqual(cond1, cond2 metav1.Condition) bool {
	return cond1.Type == cond2.Type &&
		cond1.Status == cond2.Status &&
		cond1.Message == cond2.Message &&
		cond1.Reason == cond2.Reason
}

func clearAllBuildConditions(inConditions []metav1.Condition) []metav1.Condition {
	conditions := []metav1.Condition{}

	for _, condition := range inConditions {
		buildConditionFound := false
		for _, buildConditionType := range getMachineConfigBuildConditions() {
			if condition.Type == string(buildConditionType) {
				buildConditionFound = true
				break
			}
		}

		if !buildConditionFound {
			conditions = append(conditions, condition)
		}
	}

	return conditions
}

func getMachineConfigBuildConditions() []mcfgv1.BuildProgress {
	return []mcfgv1.BuildProgress{
		mcfgv1.MachineOSBuildFailed,
		mcfgv1.MachineOSBuildInterrupted,
		mcfgv1.MachineOSBuildPrepared,
		mcfgv1.MachineOSBuildSucceeded,
		mcfgv1.MachineOSBuilding,
	}
}

func IsPoolAnyDegraded(pool *mcfgv1.MachineConfigPool) bool {
	condTypes := []mcfgv1.MachineConfigPoolConditionType{
		mcfgv1.MachineConfigPoolDegraded,
		mcfgv1.MachineConfigPoolNodeDegraded,
		mcfgv1.MachineConfigPoolRenderDegraded,
	}

	for _, condType := range condTypes {
		if apihelpers.IsMachineConfigPoolConditionTrue(pool.Status.Conditions, condType) {
			return true
		}
	}

	return false
}

// Determine if we have a config change.
func IsPoolConfigChange(oldPool, curPool *mcfgv1.MachineConfigPool) bool {
	return oldPool.Spec.Configuration.Name != curPool.Spec.Configuration.Name
}

func HasBuildObjectForCurrentMachineConfig(pool *mcfgv1.MachineConfigPool, mosb *mcfgv1.MachineOSBuild) bool {
	return pool.Spec.Configuration.Name == mosb.Spec.MachineConfig.Name
}

// Determines if we should do a build based upon the state of our
// MachineConfigPool, the presence of a build pod, etc.
func BuildDueToPoolChange(oldPool, curPool *mcfgv1.MachineConfigPool, moscNew *mcfgv1.MachineOSConfig, mosbNew *mcfgv1.MachineOSBuild) bool {

	moscState := NewMachineOSConfigState(moscNew)
	mosbState := NewMachineOSBuildState(mosbNew)

	// If we don't have a layered pool, we should not build.
	poolStateSuggestsBuild := canPoolBuild(curPool, moscState, mosbState) &&
		// If we have a config change or we're missing an image pullspec label, we
		// should do a build.
		(IsPoolConfigChange(oldPool, curPool) || !moscState.HasOSImage())

	return poolStateSuggestsBuild

}

// Checks our pool to see if we can do a build. We base this off of a few criteria:
// 1. Is the pool opted into layering?
// 2. Do we have an object reference to an in-progress build?
// 3. Is the pool degraded?
// 4. Is our build in a specific state?
//
// Returns true if we are able to build.
func canPoolBuild(pool *mcfgv1.MachineConfigPool, moscNewState *MachineOSConfigState, mosbNewState *MachineOSBuildState) bool {
	// If we don't have a layered pool, we should not build.
	if !IsLayeredPool(moscNewState.Config, mosbNewState.Build) {
		return false
	}
	// If the pool is degraded, we should not build.
	if IsPoolAnyDegraded(pool) {
		return false
	}
	// If the new pool has an ongoing build, we should not build
	if mosbNewState.Build != nil {
		return false
	}
	return true
}

func IsLayeredPool(mosc *mcfgv1.MachineOSConfig, mosb *mcfgv1.MachineOSBuild) bool {
	return (mosc != nil || mosb != nil)
}

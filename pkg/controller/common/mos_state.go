package common

import (
	mcfgv1alpha1 "github.com/openshift/api/machineconfiguration/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/openshift/machine-config-operator/pkg/apihelpers"
)

// This is intended to provide a singular way to interrogate MachineConfigPool
// objects to determine if they're in a specific state or not. The eventual
// goal is to use this to mutate the MachineConfigPool object to provide a
// single and consistent interface for that purpose. In this current state, we
// do not perform any mutations.
type MachineOSBuildState struct {
	Build *mcfgv1alpha1.MachineOSBuild
}

type MachineOSConfigState struct {
	Config *mcfgv1alpha1.MachineOSConfig
}

func NewMachineOSConfigState(mosc *mcfgv1alpha1.MachineOSConfig) *MachineOSConfigState {
	return &MachineOSConfigState{
		Config: mosc,
	}
}

func NewMachineOSBuildState(mosb *mcfgv1alpha1.MachineOSBuild) *MachineOSBuildState {
	return &MachineOSBuildState{
		Build: mosb,
	}
}

// Returns the OS image, if one is present.
func (l *MachineOSConfigState) GetOSImage() string {
	osImage := l.Config.Status.CurrentImagePullspec
	return osImage
}

// Determines if an OS image build is a success.
func (l *MachineOSBuildState) IsBuildSuccess() bool {
	return apihelpers.IsMachineOSBuildConditionTrue(l.Build.Status.Conditions, mcfgv1alpha1.MachineOSBuildSucceeded)
}

// Determines if an OS image build is pending.
func (l *MachineOSBuildState) IsBuildPending() bool {
	return apihelpers.IsMachineOSBuildConditionTrue(l.Build.Status.Conditions, mcfgv1alpha1.MachineOSBuilding)
}

// Determines if an OS image build is in progress.
func (l *MachineOSBuildState) IsBuilding() bool {
	return apihelpers.IsMachineOSBuildConditionTrue(l.Build.Status.Conditions, mcfgv1alpha1.MachineOSBuilding)
}

// Determines if an OS image build has failed.
func (l *MachineOSBuildState) IsBuildFailure() bool {
	return apihelpers.IsMachineOSBuildConditionTrue(l.Build.Status.Conditions, mcfgv1alpha1.MachineOSBuildFailed)
}

// Determines if an OS image build has failed.
func (l *MachineOSBuildState) IsBuildInterrupted() bool {
	return apihelpers.IsMachineOSBuildConditionTrue(l.Build.Status.Conditions, mcfgv1alpha1.MachineOSBuildInterrupted)
}

func (l *MachineOSBuildState) IsAnyDegraded() bool {
	condTypes := []mcfgv1alpha1.BuildProgress{
		mcfgv1alpha1.MachineOSBuildFailed,
		mcfgv1alpha1.MachineOSBuildInterrupted,
	}

	for _, condType := range condTypes {
		if apihelpers.IsMachineOSBuildConditionTrue(l.Build.Status.Conditions, condType) {
			return true
		}
	}

	return false
}

// Idempotently sets the supplied build conditions.
func (b *MachineOSBuildState) SetBuildConditions(conditions []metav1.Condition) {
	for _, condition := range conditions {
		condition := condition
		currentCondition := apihelpers.GetMachineOSBuildCondition(b.Build.Status, mcfgv1alpha1.BuildProgress(condition.Type))
		if currentCondition != nil && isConditionEqual(*currentCondition, condition) {
			continue
		}

		mosbCondition := apihelpers.NewMachineOSBuildCondition(condition.Type, condition.Status, condition.Reason, condition.Message)
		apihelpers.SetMachineOSBuildCondition(&b.Build.Status, *mosbCondition)
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

// Determines if a given MachineConfigPool has an available OS image. Returns
// false if the annotation is missing or set to an empty string.
func (m *MachineOSConfigState) HasOSImage() bool {
	val := m.Config.Status.CurrentImagePullspec
	return val != ""
}

// Clears the image pullspec annotation.
func (m *MachineOSConfigState) ClearImagePullspec() {
	m.Config.Spec.BuildInputs.RenderedImagePushspec = ""
	m.Config.Status.CurrentImagePullspec = ""
}

// Clears all build object conditions.
func (m *MachineOSBuildState) ClearAllBuildConditions() {
	m.Build.Status.Conditions = clearAllBuildConditions(m.Build.Status.Conditions)
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

func getMachineConfigBuildConditions() []mcfgv1alpha1.BuildProgress {
	return []mcfgv1alpha1.BuildProgress{
		mcfgv1alpha1.MachineOSBuildFailed,
		mcfgv1alpha1.MachineOSBuildInterrupted,
		mcfgv1alpha1.MachineOSBuildPrepared,
		mcfgv1alpha1.MachineOSBuildSucceeded,
		mcfgv1alpha1.MachineOSBuilding,
	}
}

/*
func (l *MachineOSBuildState) IsInterrupted() bool {
	return apihelpers.IsMachineConfigPoolConditionTrue(l.pool.Status.Conditions, mcfgv1.MachineConfigPoolBuildInterrupted)
}

func (l *MachineOSBuildState) IsDegraded() bool {
	return apihelpers.IsMachineConfigPoolConditionTrue(l.pool.Status.Conditions, mcfgv1.MachineConfigPoolDegraded)
}

func (l *MachineOSBuildState) IsNodeDegraded() bool {
	return apihelpers.IsMachineConfigPoolConditionTrue(l.pool.Status.Conditions, mcfgv1.MachineConfigPoolNodeDegraded)
}

func (l *MachineOSBuildState) IsRenderDegraded() bool {
	return apihelpers.IsMachineConfigPoolConditionTrue(l.pool.Status.Conditions, mcfgv1.MachineConfigPoolRenderDegraded)
}
*/

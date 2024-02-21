package build

import (
	"fmt"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	"github.com/openshift/machine-config-operator/pkg/apihelpers"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	corev1 "k8s.io/api/core/v1"
)

type poolState struct {
	pool *mcfgv1.MachineConfigPool
}

func newPoolState(pool *mcfgv1.MachineConfigPool) *poolState {
	copied := pool.DeepCopy()
	return &poolState{
		pool: copied,
	}
}

// Returns the name of the MachineConfigPool.
func (p *poolState) Name() string {
	return p.pool.GetName()
}

// Returns the name of the current MachineConfig.
func (p *poolState) CurrentMachineConfig() string {
	return p.pool.Spec.Configuration.Name
}

// Returns a deep-copy of the MachineConfigPool with any changes made,
// propagating spec.Configurtion to status.Configuration in the process.
func (p *poolState) MachineConfigPool() *mcfgv1.MachineConfigPool {
	out := p.pool.DeepCopy()
	out.Spec.Configuration.DeepCopyInto(&out.Status.Configuration)
	return out
}

// Sets the image pullspec annotation.
func (p *poolState) SetImagePullspec(pullspec string) {
	if p.pool.Annotations == nil {
		p.pool.Annotations = map[string]string{}
	}

	p.pool.Annotations[ctrlcommon.ExperimentalNewestLayeredImageEquivalentConfigAnnotationKey] = pullspec
}

// Clears the image pullspec annotation.
func (p *poolState) ClearImagePullspec() {
	if p.pool.Annotations == nil {
		return
	}

	delete(p.pool.Annotations, ctrlcommon.ExperimentalNewestLayeredImageEquivalentConfigAnnotationKey)
}

// Deletes a given build object reference by its name.
func (p *poolState) DeleteBuildRefByName(name string) {
	p.pool.Spec.Configuration.Source = p.getFilteredObjectRefs(func(objRef corev1.ObjectReference) bool {
		return objRef.Name != name && !p.isBuildObjectRef(objRef)
	})
}

// Determines if a MachineConfigPool has a build object reference given its
// name.
func (p *poolState) HasBuildObjectRefName(name string) bool {
	for _, src := range p.pool.Spec.Configuration.Source {
		if src.Name == name && p.isBuildObjectRef(src) {
			return true
		}
	}

	return false
}

// Searches for any object reference.
func (p *poolState) HasObjectRef(objRef corev1.ObjectReference) bool {
	for _, src := range p.pool.Spec.Configuration.Source {
		if src == objRef {
			return true
		}
	}

	return false
}

// Searches only for build object references.
func (p *poolState) HasBuildObjectRef(objRef corev1.ObjectReference) bool {
	for _, src := range p.pool.Spec.Configuration.Source {
		if src == objRef && p.isBuildObjectRef(src) {
			return true
		}
	}

	return false
}

// Deletes all build object references.
func (p *poolState) DeleteAllBuildRefs() {
	p.pool.Spec.Configuration.Source = p.getFilteredObjectRefs(func(objRef corev1.ObjectReference) bool {
		return objRef.Kind == "MachineConfig"
	})
}

// Deletes a given object reference.
func (p *poolState) DeleteObjectRef(toDelete corev1.ObjectReference) {
	p.pool.Spec.Configuration.Source = p.getFilteredObjectRefs(func(objRef corev1.ObjectReference) bool {
		return objRef != toDelete
	})
}

// Gets all build object references.
func (p *poolState) GetBuildObjectRefs() []corev1.ObjectReference {
	return p.getFilteredObjectRefs(func(objRef corev1.ObjectReference) bool {
		return p.isBuildObjectRef(objRef)
	})
}

// Adds a build object reference. Returns an error if a build object reference
// already exists.
func (p *poolState) AddBuildObjectRef(objRef corev1.ObjectReference) error {
	if !p.isBuildObjectRef(objRef) {
		return fmt.Errorf("%s not a valid build object kind", objRef.Kind)
	}

	buildRefs := p.GetBuildObjectRefs()
	if len(buildRefs) != 0 {
		return fmt.Errorf("cannot add build ObjectReference, found: %v", buildRefs)
	}

	p.pool.Spec.Configuration.Source = append(p.pool.Spec.Configuration.Source, objRef)
	return nil
}

// Clears all build object conditions.
func (p *poolState) ClearAllBuildConditions() {
	p.pool.Status.Conditions = clearAllBuildConditions(p.pool.Status.Conditions)
}

// Idempotently sets the supplied build conditions.
func (p *poolState) SetBuildConditions(conditions []mcfgv1.MachineConfigPoolCondition) {
	for _, condition := range conditions {
		condition := condition
		currentCondition := apihelpers.GetMachineConfigPoolCondition(p.pool.Status, condition.Type)
		if currentCondition != nil && isConditionEqual(*currentCondition, condition) {
			continue
		}

		mcpCondition := apihelpers.NewMachineConfigPoolCondition(condition.Type, condition.Status, condition.Reason, condition.Message)
		apihelpers.SetMachineConfigPoolCondition(&p.pool.Status, *mcpCondition)
	}
}

// Gets all build conditions.
func (p *poolState) GetAllBuildConditions() []mcfgv1.MachineConfigPoolCondition {
	buildConditions := []mcfgv1.MachineConfigPoolCondition{}

	for _, condition := range p.pool.Status.Conditions {
		if p.isBuildCondition(condition) {
			buildConditions = append(buildConditions, condition)
		}
	}

	return buildConditions
}

func (p *poolState) isBuildCondition(cond mcfgv1.MachineConfigPoolCondition) bool {
	for _, condType := range getMachineConfigPoolBuildConditions() {
		if cond.Type == condType {
			return true
		}
	}

	return false
}

func (p *poolState) isBuildObjectRef(objRef corev1.ObjectReference) bool {
	if objRef.Kind == "Pod" {
		return true
	}

	if objRef.Kind == "Build" {
		return true
	}

	return false
}

func (p *poolState) getFilteredObjectRefs(filterFunc func(objRef corev1.ObjectReference) bool) []corev1.ObjectReference {
	refs := []corev1.ObjectReference{}

	for _, src := range p.pool.Spec.Configuration.Source {
		if filterFunc(src) {
			refs = append(refs, src)
		}
	}

	return refs
}

// Determines if two conditions are equal. Note: I purposely do not include the
// timestamp in the equality test, since we do not directly set it.
func isConditionEqual(cond1, cond2 mcfgv1.MachineConfigPoolCondition) bool {
	return cond1.Type == cond2.Type &&
		cond1.Status == cond2.Status &&
		cond1.Message == cond2.Message &&
		cond1.Reason == cond2.Reason
}

func clearAllBuildConditions(inConditions []mcfgv1.MachineConfigPoolCondition) []mcfgv1.MachineConfigPoolCondition {
	conditions := []mcfgv1.MachineConfigPoolCondition{}

	for _, condition := range inConditions {
		buildConditionFound := false
		for _, buildConditionType := range getMachineConfigPoolBuildConditions() {
			if condition.Type == buildConditionType {
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

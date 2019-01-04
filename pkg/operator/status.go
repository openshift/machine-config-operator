package operator

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/golang/glog"
	configv1 "github.com/openshift/api/config/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/pkg/version"
)

// syncAvailableStatus applies the new condition to the mco's ClusterOperator object.
func (optr *Operator) syncAvailableStatus() error {
	co, err := optr.fetchClusterOperator()
	if err != nil {
		return err
	}
	if co == nil {
		return nil
	}
	now := metav1.Now()
	// set available
	SetClusterOperatorStatusCondition(&co.Status.Conditions, configv1.ClusterOperatorStatusCondition{Type: configv1.OperatorAvailable, Status: configv1.ConditionTrue, LastTransitionTime: now})
	// clear progressing
	SetClusterOperatorStatusCondition(&co.Status.Conditions, configv1.ClusterOperatorStatusCondition{Type: configv1.OperatorProgressing, Status: configv1.ConditionFalse, LastTransitionTime: now})
	// clear failure
	SetClusterOperatorStatusCondition(&co.Status.Conditions, configv1.ClusterOperatorStatusCondition{Type: configv1.OperatorFailing, Status: configv1.ConditionFalse, LastTransitionTime: now})

	co.Status.Version = version.Version.String()

	optr.setMachineConfigPoolStatuses(&co.Status)
	_, err = optr.configClient.ConfigV1().ClusterOperators().UpdateStatus(co)
	return err
}

// syncProgressingStatus applies the new condition to the mco's ClusterOperator object.
func (optr *Operator) syncProgressingStatus() error {
	co, err := optr.fetchClusterOperator()
	if err != nil {
		return err
	}
	if co == nil {
		return nil
	}
	now := metav1.Now()
	// clear the available condition
	SetClusterOperatorStatusCondition(&co.Status.Conditions, configv1.ClusterOperatorStatusCondition{Type: configv1.OperatorAvailable, Status: configv1.ConditionFalse, LastTransitionTime: now})

	// preserve the most recent failing condition
	if IsClusterOperatorStatusConditionNotIn(co.Status.Conditions, configv1.OperatorFailing, configv1.ConditionTrue) {
		SetClusterOperatorStatusCondition(&co.Status.Conditions, configv1.ClusterOperatorStatusCondition{Type: configv1.OperatorFailing, Status: configv1.ConditionFalse, LastTransitionTime: now})
	}

	// set progressing
	if c := FindClusterOperatorStatusCondition(co.Status.Conditions, configv1.OperatorFailing); c != nil && c.Status == configv1.ConditionTrue {
		SetClusterOperatorStatusCondition(&co.Status.Conditions, configv1.ClusterOperatorStatusCondition{Type: configv1.OperatorProgressing, Status: configv1.ConditionTrue, Message: fmt.Sprintf("Unable to apply %s", version.Version.String()), LastTransitionTime: now})
	} else {
		SetClusterOperatorStatusCondition(&co.Status.Conditions, configv1.ClusterOperatorStatusCondition{Type: configv1.OperatorProgressing, Status: configv1.ConditionTrue, Message: fmt.Sprintf("Progressing towards %s", version.Version.String()), LastTransitionTime: now})
	}

	co.Status.Version = version.Version.String()
	optr.setMachineConfigPoolStatuses(&co.Status)
	_, err = optr.configClient.ConfigV1().ClusterOperators().UpdateStatus(co)
	return err
}

// syncFailingStatus applies the new condition to the mco's ClusterOperator object.
func (optr *Operator) syncFailingStatus(ierr error) error {
	if ierr == nil {
		return nil
	}
	co, err := optr.fetchClusterOperator()
	if err != nil {
		return err
	}
	if co == nil {
		return nil
	}
	now := metav1.Now()
	// clear the available condition
	SetClusterOperatorStatusCondition(&co.Status.Conditions, configv1.ClusterOperatorStatusCondition{Type: configv1.OperatorAvailable, Status: configv1.ConditionFalse, LastTransitionTime: now})

	// set failing condition
	SetClusterOperatorStatusCondition(&co.Status.Conditions, configv1.ClusterOperatorStatusCondition{Type: configv1.OperatorFailing, Status: configv1.ConditionTrue, Message: ierr.Error(), LastTransitionTime: now})

	// set progressing
	if IsClusterOperatorStatusConditionTrue(co.Status.Conditions, configv1.OperatorProgressing) {
		SetClusterOperatorStatusCondition(&co.Status.Conditions, configv1.ClusterOperatorStatusCondition{Type: configv1.OperatorProgressing, Status: configv1.ConditionTrue, Message: fmt.Sprintf("Unable to apply %s", version.Version.String()), LastTransitionTime: now})
	} else {
		SetClusterOperatorStatusCondition(&co.Status.Conditions, configv1.ClusterOperatorStatusCondition{Type: configv1.OperatorProgressing, Status: configv1.ConditionFalse, Message: fmt.Sprintf("Error while reconciling %s", version.Version.String()), LastTransitionTime: now})
	}

	co.Status.Version = version.Version.String()
	optr.setMachineConfigPoolStatuses(&co.Status)
	_, err = optr.configClient.ConfigV1().ClusterOperators().UpdateStatus(co)
	return err
}

func (optr *Operator) fetchClusterOperator() (*configv1.ClusterOperator, error) {
	co, err := optr.configClient.ConfigV1().ClusterOperators().Get(optr.name, metav1.GetOptions{})
	if meta.IsNoMatchError(err) {
		return nil, nil
	}
	if apierrors.IsNotFound(err) {
		return optr.initializeClusterOperator()
	}
	if err != nil {
		return nil, err
	}
	return co, nil
}

func (optr *Operator) initializeClusterOperator() (*configv1.ClusterOperator, error) {
	co, err := optr.configClient.ConfigV1().ClusterOperators().Create(&configv1.ClusterOperator{
		ObjectMeta: metav1.ObjectMeta{
			Name: optr.name,
		},
	})
	if err != nil {
		return nil, err
	}
	now := metav1.Now()
	SetClusterOperatorStatusCondition(&co.Status.Conditions, configv1.ClusterOperatorStatusCondition{Type: configv1.OperatorAvailable, Status: configv1.ConditionFalse, LastTransitionTime: now})
	SetClusterOperatorStatusCondition(&co.Status.Conditions, configv1.ClusterOperatorStatusCondition{Type: configv1.OperatorProgressing, Status: configv1.ConditionFalse, LastTransitionTime: now})
	SetClusterOperatorStatusCondition(&co.Status.Conditions, configv1.ClusterOperatorStatusCondition{Type: configv1.OperatorFailing, Status: configv1.ConditionFalse, LastTransitionTime: now})
	return optr.configClient.ConfigV1().ClusterOperators().UpdateStatus(co)
}

func (optr *Operator) setMachineConfigPoolStatuses(status *configv1.ClusterOperatorStatus) {
	statuses, err := optr.allMachineConfigPoolStatus()
	if err != nil {
		glog.Error(err)
		return
	}
	raw, err := json.Marshal(statuses)
	if err != nil {
		glog.Error(err)
		return
	}
	status.Extension.Raw = raw
}

func (optr *Operator) allMachineConfigPoolStatus() (map[string]string, error) {
	pools, err := optr.mcpLister.List(labels.Everything())
	if err != nil {
		return nil, err
	}
	ret := map[string]string{}
	for _, pool := range pools {
		p := pool.DeepCopy()
		err := isMachineConfigPoolConfigurationValid(p, version.Version.String(), optr.mcLister.Get)
		if err != nil {
			glog.V(4).Infof("Skipping status for pool %s because %v", p.GetName(), err)
			continue
		}
		ret[p.GetName()] = machineConfigPoolStatus(p)
	}
	return ret, nil
}

// isMachineConfigPoolConfigurationValid returns nil error when the configuration of a `pool` is created by the controller at version `version`.
func isMachineConfigPoolConfigurationValid(pool *mcfgv1.MachineConfigPool, version string, machineConfigGetter func(string) (*mcfgv1.MachineConfig, error)) error {
	// both .status.configuration.name and .status.configuration.source must be set.
	if len(pool.Status.Configuration.Name) == 0 {
		return fmt.Errorf("configuration for pool %s is empty", pool.GetName())
	}
	if len(pool.Status.Configuration.Source) == 0 {
		return fmt.Errorf("list of MachineConfigs that were used to generate configuration for pool %s is empty", pool.GetName())
	}

	type configValidationTask struct {
		name                 string
		versionCheckRequired bool
	}
	// we check that all the machineconfigs (generated, and those that were used to create generated) were generated by correct version of the controller.
	tasks := []configValidationTask{{
		name:                 pool.Status.Configuration.Name,
		versionCheckRequired: true,
	}}
	for _, ref := range pool.Status.Configuration.Source {
		tasks = append(tasks, configValidationTask{name: ref.Name, versionCheckRequired: false})
	}
	for _, t := range tasks {
		mc, err := machineConfigGetter(t.name)
		if err != nil {
			return err
		}

		v, ok := mc.Annotations[ctrlcommon.GeneratedByControllerVersionAnnotationKey]
		if t.versionCheckRequired && !ok {
			return fmt.Errorf("%s must be created by controller version %s", t.name, version)
		}
		if ok && v != version {
			return fmt.Errorf("controller version mismatch for %s expected %s has %s", t.name, version, v)
		}
	}
	return nil
}

func machineConfigPoolStatus(pool *mcfgv1.MachineConfigPool) string {
	switch {
	case mcfgv1.IsMachineConfigPoolConditionTrue(pool.Status.Conditions, mcfgv1.MachineConfigPoolDegraded):
		cond := mcfgv1.GetMachineConfigPoolCondition(pool.Status, mcfgv1.MachineConfigPoolDegraded)
		return fmt.Sprintf("pool is degraded because of %s", cond.Reason)
	case mcfgv1.IsMachineConfigPoolConditionTrue(pool.Status.Conditions, mcfgv1.MachineConfigPoolUpdated):
		return fmt.Sprintf("all %d nodes are at latest configuration %s", pool.Status.MachineCount, pool.Status.Configuration.Name)
	case mcfgv1.IsMachineConfigPoolConditionTrue(pool.Status.Conditions, mcfgv1.MachineConfigPoolUpdating):
		return fmt.Sprintf("%d out of %d nodes have updated to latest configuration %s", pool.Status.UpdatedMachineCount, pool.Status.MachineCount, pool.Status.Configuration.Name)
	default:
		return "<unknown>"
	}
}

// From https://github.com/openshift/library-go/pull/97

// SetClusterOperatorStatusCondition sets the corresponding condition in conditions to newCondition.
func SetClusterOperatorStatusCondition(conditions *[]configv1.ClusterOperatorStatusCondition, newCondition configv1.ClusterOperatorStatusCondition) {
	if conditions == nil {
		conditions = &[]configv1.ClusterOperatorStatusCondition{}
	}
	existingCondition := FindClusterOperatorStatusCondition(*conditions, newCondition.Type)
	if existingCondition == nil {
		newCondition.LastTransitionTime = metav1.NewTime(time.Now())
		*conditions = append(*conditions, newCondition)
		return
	}
	if existingCondition.Status != newCondition.Status {
		existingCondition.Status = newCondition.Status
		existingCondition.LastTransitionTime = newCondition.LastTransitionTime
	}
	existingCondition.Reason = newCondition.Reason
	existingCondition.Message = newCondition.Message
}

// RemoveClusterOperatorStatusCondition removes the corresponding conditionType from conditions.
func RemoveClusterOperatorStatusCondition(conditions *[]configv1.ClusterOperatorStatusCondition, conditionType configv1.ClusterStatusConditionType) {
	if conditions == nil {
		conditions = &[]configv1.ClusterOperatorStatusCondition{}
	}
	newConditions := []configv1.ClusterOperatorStatusCondition{}
	for _, condition := range *conditions {
		if condition.Type != conditionType {
			newConditions = append(newConditions, condition)
		}
	}
	*conditions = newConditions
}

// FindClusterOperatorStatusCondition finds the conditionType in conditions.
func FindClusterOperatorStatusCondition(conditions []configv1.ClusterOperatorStatusCondition, conditionType configv1.ClusterStatusConditionType) *configv1.ClusterOperatorStatusCondition {
	for i := range conditions {
		if conditions[i].Type == conditionType {
			return &conditions[i]
		}
	}
	return nil
}

// IsClusterOperatorStatusConditionTrue returns true when the conditionType is present and set to `configv1.ConditionTrue`
func IsClusterOperatorStatusConditionTrue(conditions []configv1.ClusterOperatorStatusCondition, conditionType configv1.ClusterStatusConditionType) bool {
	return IsClusterOperatorStatusConditionPresentAndEqual(conditions, conditionType, configv1.ConditionTrue)
}

// IsClusterOperatorStatusConditionFalse returns true when the conditionType is present and set to `configv1.ConditionFalse`
func IsClusterOperatorStatusConditionFalse(conditions []configv1.ClusterOperatorStatusCondition, conditionType configv1.ClusterStatusConditionType) bool {
	return IsClusterOperatorStatusConditionPresentAndEqual(conditions, conditionType, configv1.ConditionFalse)
}

// IsClusterOperatorStatusConditionPresentAndEqual returns true when conditionType is present and equal to status.
func IsClusterOperatorStatusConditionPresentAndEqual(conditions []configv1.ClusterOperatorStatusCondition, conditionType configv1.ClusterStatusConditionType, status configv1.ConditionStatus) bool {
	for _, condition := range conditions {
		if condition.Type == conditionType {
			return condition.Status == status
		}
	}
	return false
}

// IsClusterOperatorStatusConditionNotIn returns true when the conditionType does not match the status.
func IsClusterOperatorStatusConditionNotIn(conditions []configv1.ClusterOperatorStatusCondition, conditionType configv1.ClusterStatusConditionType, status ...configv1.ConditionStatus) bool {
	for _, condition := range conditions {
		if condition.Type == conditionType {
			for _, s := range status {
				if s == condition.Status {
					return false
				}
			}
			return true
		}
	}
	return true
}

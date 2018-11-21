package operator

import (
	"fmt"
	"time"

	configv1 "github.com/openshift/api/config/v1"
	"github.com/openshift/machine-config-operator/pkg/version"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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

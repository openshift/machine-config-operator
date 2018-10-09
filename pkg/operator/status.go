package operator

import (
	"fmt"

	"github.com/openshift/cluster-version-operator/lib/resourceapply"
	osv1 "github.com/openshift/cluster-version-operator/pkg/apis/operatorstatus.openshift.io/v1"
	"github.com/openshift/machine-config-operator/pkg/version"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// syncStatus applies the new condition to the mco's OperatorStatus object.
func (optr *Operator) syncStatus(cond osv1.OperatorStatusCondition) error {
	if cond.Type == osv1.OperatorStatusConditionTypeDegraded {
		return fmt.Errorf("invalid condition %s", cond.Type)
	}

	// TODO(yifan): Fill in the Extention field for the status
	// to report the status of all the managed components.
	status := &osv1.OperatorStatus{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: optr.namespace,
			Name:      optr.name,
		},
		Condition:  cond,
		Version:    version.Raw,
		LastUpdate: metav1.Now(),
	}
	_, _, err := resourceapply.ApplyOperatorStatus(optr.cvoClient.OperatorstatusV1(), status)
	return err
}

// syncDegradedStatus updates the OperatorStatus to Degraded.
// if ierr is nil, return nil
// if ierr is not nil, update OperatorStatus as Degraded and return ierr
func (optr *Operator) syncDegradedStatus(ierr error) error {
	if ierr == nil {
		return nil
	}
	cond := osv1.OperatorStatusCondition{
		Type:    osv1.OperatorStatusConditionTypeDegraded,
		Message: fmt.Sprintf("error syncing: %v", ierr),
	}

	status := &osv1.OperatorStatus{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: optr.namespace,
			Name:      optr.name,
		},
		Condition:  cond,
		Version:    version.Raw,
		LastUpdate: metav1.Now(),
		Extension:  runtime.RawExtension{},
	}
	_, _, err := resourceapply.ApplyOperatorStatus(optr.cvoClient.OperatorstatusV1(), status)
	if err != nil {
		return err
	}
	return ierr
}

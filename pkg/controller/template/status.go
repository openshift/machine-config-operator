package template

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/util/retry"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	mcfgclientv1 "github.com/openshift/client-go/machineconfiguration/clientset/versioned/typed/machineconfiguration/v1"
	"github.com/openshift/machine-config-operator/pkg/apihelpers"
	"github.com/openshift/machine-config-operator/pkg/version"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// - sets `running` condition to `true`.
// - reset the `available` condition to `false` when we are syncing to new generation.
// - does not modify `failing` condition.
func (ctrl *Controller) syncRunningStatus(ctrlconfig *mcfgv1.ControllerConfig) error {
	updateFunc := func(cfg *mcfgv1.ControllerConfig) error {
		reason := fmt.Sprintf("syncing towards (%d) generation using controller version %s", cfg.GetGeneration(), version.Raw)
		rcond := apihelpers.NewControllerConfigStatusCondition(mcfgv1.TemplateControllerRunning, corev1.ConditionTrue, "", reason)
		apihelpers.SetControllerConfigStatusCondition(&cfg.Status, *rcond)
		if cfg.GetGeneration() != cfg.Status.ObservedGeneration && apihelpers.IsControllerConfigStatusConditionPresentAndEqual(cfg.Status.Conditions, mcfgv1.TemplateControllerCompleted, corev1.ConditionTrue) {
			acond := apihelpers.NewControllerConfigStatusCondition(mcfgv1.TemplateControllerCompleted, corev1.ConditionFalse, "", fmt.Sprintf("%s due to change in Generation", reason))
			apihelpers.SetControllerConfigStatusCondition(&cfg.Status, *acond)
		}
		cfg.Status.ObservedGeneration = ctrlconfig.GetGeneration()
		return nil
	}
	return updateControllerConfigStatus(ctrlconfig.GetName(), ctrl.ccLister.Get, ctrl.client.MachineconfigurationV1().ControllerConfigs(), updateFunc)
}

// - resets `running` condition to `false`
// - resets `completed` condition to `false`
// - sets the `failing` condition to `true` using the `oerr`
func (ctrl *Controller) syncFailingStatus(ctrlconfig *mcfgv1.ControllerConfig, oerr error) error {
	if oerr == nil {
		return nil
	}
	updateFunc := func(cfg *mcfgv1.ControllerConfig) error {
		message := fmt.Sprintf("failed to syncing towards (%d) generation using controller version %s: %v", cfg.GetGeneration(), version.Raw, oerr)
		fcond := apihelpers.NewControllerConfigStatusCondition(mcfgv1.TemplateControllerFailing, corev1.ConditionTrue, "", message)
		apihelpers.SetControllerConfigStatusCondition(&cfg.Status, *fcond)
		acond := apihelpers.NewControllerConfigStatusCondition(mcfgv1.TemplateControllerCompleted, corev1.ConditionFalse, "", "")
		apihelpers.SetControllerConfigStatusCondition(&cfg.Status, *acond)
		rcond := apihelpers.NewControllerConfigStatusCondition(mcfgv1.TemplateControllerRunning, corev1.ConditionFalse, "", "")
		apihelpers.SetControllerConfigStatusCondition(&cfg.Status, *rcond)
		cfg.Status.ObservedGeneration = ctrlconfig.GetGeneration()
		return nil
	}
	if err := updateControllerConfigStatus(ctrlconfig.GetName(), ctrl.ccLister.Get, ctrl.client.MachineconfigurationV1().ControllerConfigs(), updateFunc); err != nil {
		return fmt.Errorf("failed to sync status for %v", oerr)
	}
	return oerr
}

// - resets `running` condition to `false`
// - resets `failing` condition to `false`
// - sets the `completed` condition to `true`
func (ctrl *Controller) syncCompletedStatus(ctrlconfig *mcfgv1.ControllerConfig) error {
	updateFunc := func(cfg *mcfgv1.ControllerConfig) error {
		reason := fmt.Sprintf("sync completed towards (%d) generation using controller version %s", cfg.GetGeneration(), version.Raw)
		acond := apihelpers.NewControllerConfigStatusCondition(mcfgv1.TemplateControllerCompleted, corev1.ConditionTrue, "", reason)
		apihelpers.SetControllerConfigStatusCondition(&cfg.Status, *acond)
		rcond := apihelpers.NewControllerConfigStatusCondition(mcfgv1.TemplateControllerRunning, corev1.ConditionFalse, "", "")
		apihelpers.SetControllerConfigStatusCondition(&cfg.Status, *rcond)
		fcond := apihelpers.NewControllerConfigStatusCondition(mcfgv1.TemplateControllerFailing, corev1.ConditionFalse, "", "")
		apihelpers.SetControllerConfigStatusCondition(&cfg.Status, *fcond)
		cfg.Status.ObservedGeneration = ctrlconfig.GetGeneration()
		return nil
	}
	return updateControllerConfigStatus(ctrlconfig.GetName(), ctrl.ccLister.Get, ctrl.client.MachineconfigurationV1().ControllerConfigs(), updateFunc)
}

// syncCertificateStatus places the new certitifcate data into the actual controllerConfig that is our source of truth.
func (ctrl *Controller) syncCertificateStatus(ctrlconfig *mcfgv1.ControllerConfig) error {
	updateFunc := func(cfg *mcfgv1.ControllerConfig) error {
		cfg.Status.ControllerCertificates = ctrlconfig.Status.ControllerCertificates
		return nil
	}
	return updateControllerConfigStatus(ctrlconfig.GetName(), ctrl.ccLister.Get, ctrl.client.MachineconfigurationV1().ControllerConfigs(), updateFunc)
}

type updateControllerConfigStatusFunc func(*mcfgv1.ControllerConfig) error

func updateControllerConfigStatus(name string,
	controllerConfigGetter func(name string) (*mcfgv1.ControllerConfig, error),
	client mcfgclientv1.ControllerConfigInterface,
	updateFuncs ...updateControllerConfigStatusFunc) error {
	return retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		old, err := controllerConfigGetter(name)
		if err != nil {
			return err
		}
		newCfg := old.DeepCopy()
		for _, update := range updateFuncs {
			if err := update(newCfg); err != nil {
				return err
			}
		}

		if equality.Semantic.DeepEqual(old, newCfg) {
			return nil
		}
		_, err = client.UpdateStatus(context.TODO(), newCfg, metav1.UpdateOptions{})
		return err
	})
}

package operator

import (
	"fmt"
	"time"

	"github.com/golang/glog"
	"github.com/pkg/errors"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiextv1beta1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1beta1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/wait"

	"github.com/openshift/machine-config-operator/lib/resourceapply"
	"github.com/openshift/machine-config-operator/lib/resourceread"
	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	"github.com/openshift/machine-config-operator/pkg/operator/assets"
	"github.com/openshift/machine-config-operator/pkg/version"
)

type syncFunc struct {
	name string
	fn   func(config renderConfig) error
}

func (optr *Operator) syncAll(rconfig renderConfig, syncFuncs []syncFunc) error {
	if err := optr.syncProgressingStatus(); err != nil {
		return fmt.Errorf("error syncing progressing status: %v", err)
	}

	var errs []error
	for _, sf := range syncFuncs {
		startTime := time.Now()
		errs = append(errs, sf.fn(rconfig))
		if optr.inClusterBringup {
			glog.Infof("[init mode] synced %s in %v", sf.name, time.Since(startTime))
		}
	}

	agg := utilerrors.NewAggregate(errs)
	if err := optr.syncDegradedStatus(agg); err != nil {
		return fmt.Errorf("error syncing degraded status: %v", err)
	}

	if err := optr.syncAvailableStatus(); err != nil {
		return fmt.Errorf("error syncing available status: %v", err)
	}

	if err := optr.syncVersion(); err != nil {
		return fmt.Errorf("error syncing version: %v", err)
	}

	if optr.inClusterBringup && agg == nil {
		glog.Infof("Initialization complete")
		optr.inClusterBringup = false
	}

	return agg
}

func (optr *Operator) syncCustomResourceDefinitions() error {
	crds := []string{
		"manifests/machineconfig.crd.yaml",
		"manifests/controllerconfig.crd.yaml",
		"manifests/machineconfigpool.crd.yaml",
		"manifests/kubeletconfig.crd.yaml",
		"manifests/containerruntimeconfig.crd.yaml",
	}

	for _, crd := range crds {
		crdBytes, err := assets.Asset(crd)
		if err != nil {
			return fmt.Errorf("error getting asset %s: %v", crd, err)
		}
		c := resourceread.ReadCustomResourceDefinitionV1Beta1OrDie(crdBytes)
		_, updated, err := resourceapply.ApplyCustomResourceDefinition(optr.apiExtClient.ApiextensionsV1beta1(), c)
		if err != nil {
			return err
		}
		if updated {
			if err := optr.waitForCustomResourceDefinition(c); err != nil {
				return err
			}
		}
	}

	return nil
}

func (optr *Operator) syncMachineConfigPools(config renderConfig) error {
	mcps := []string{
		"manifests/master.machineconfigpool.yaml",
		"manifests/worker.machineconfigpool.yaml",
	}

	for _, mcp := range mcps {
		mcpBytes, err := renderAsset(config, mcp)
		if err != nil {
			return err
		}
		p := resourceread.ReadMachineConfigPoolV1OrDie(mcpBytes)
		_, _, err = resourceapply.ApplyMachineConfigPool(optr.client.MachineconfigurationV1(), p)
		if err != nil {
			return err
		}
	}

	return nil
}

func (optr *Operator) syncMachineConfigController(config renderConfig) error {
	crBytes, err := renderAsset(config, "manifests/machineconfigcontroller/clusterrole.yaml")
	if err != nil {
		return err
	}
	cr := resourceread.ReadClusterRoleV1OrDie(crBytes)
	_, _, err = resourceapply.ApplyClusterRole(optr.kubeClient.RbacV1(), cr)
	if err != nil {
		return err
	}

	crbBytes, err := renderAsset(config, "manifests/machineconfigcontroller/clusterrolebinding.yaml")
	if err != nil {
		return err
	}
	crb := resourceread.ReadClusterRoleBindingV1OrDie(crbBytes)
	_, _, err = resourceapply.ApplyClusterRoleBinding(optr.kubeClient.RbacV1(), crb)
	if err != nil {
		return err
	}

	saBytes, err := renderAsset(config, "manifests/machineconfigcontroller/sa.yaml")
	if err != nil {
		return err
	}
	sa := resourceread.ReadServiceAccountV1OrDie(saBytes)
	_, _, err = resourceapply.ApplyServiceAccount(optr.kubeClient.CoreV1(), sa)
	if err != nil {
		return err
	}

	mccBytes, err := renderAsset(config, "manifests/machineconfigcontroller/deployment.yaml")
	if err != nil {
		return err
	}
	mcc := resourceread.ReadDeploymentV1OrDie(mccBytes)

	_, updated, err := resourceapply.ApplyDeployment(optr.kubeClient.AppsV1(), mcc)
	if err != nil {
		return err
	}
	if updated {
		if err := optr.waitForDeploymentRollout(mcc); err != nil {
			return err
		}
	}
	ccBytes, err := renderAsset(config, "manifests/machineconfigcontroller/controllerconfig.yaml")
	if err != nil {
		return err
	}
	cc := resourceread.ReadControllerConfigV1OrDie(ccBytes)
	_, _, err = resourceapply.ApplyControllerConfig(optr.client.MachineconfigurationV1(), cc)
	if err != nil {
		return err
	}
	return optr.waitForControllerConfigToBeCompleted(cc)
}

func (optr *Operator) syncMachineConfigDaemon(config renderConfig) error {
	for _, path := range []string{
		"manifests/machineconfigdaemon/clusterrole.yaml",
		"manifests/machineconfigdaemon/events-clusterrole.yaml",
	} {
		crBytes, err := renderAsset(config, path)
		if err != nil {
			return err
		}
		cr := resourceread.ReadClusterRoleV1OrDie(crBytes)
		_, _, err = resourceapply.ApplyClusterRole(optr.kubeClient.RbacV1(), cr)
		if err != nil {
			return err
		}
	}

	for _, path := range []string{
		"manifests/machineconfigdaemon/events-rolebinding-default.yaml",
		"manifests/machineconfigdaemon/events-rolebinding-target.yaml",
	} {
		crbBytes, err := renderAsset(config, path)
		if err != nil {
			return err
		}
		crb := resourceread.ReadRoleBindingV1OrDie(crbBytes)
		_, _, err = resourceapply.ApplyRoleBinding(optr.kubeClient.RbacV1(), crb)
		if err != nil {
			return err
		}
	}

	crbBytes, err := renderAsset(config, "manifests/machineconfigdaemon/clusterrolebinding.yaml")
	if err != nil {
		return err
	}
	crb := resourceread.ReadClusterRoleBindingV1OrDie(crbBytes)
	_, _, err = resourceapply.ApplyClusterRoleBinding(optr.kubeClient.RbacV1(), crb)
	if err != nil {
		return err
	}

	saBytes, err := renderAsset(config, "manifests/machineconfigdaemon/sa.yaml")
	if err != nil {
		return err
	}
	sa := resourceread.ReadServiceAccountV1OrDie(saBytes)
	_, _, err = resourceapply.ApplyServiceAccount(optr.kubeClient.CoreV1(), sa)
	if err != nil {
		return err
	}

	mcdBytes, err := renderAsset(config, "manifests/machineconfigdaemon/daemonset.yaml")
	if err != nil {
		return err
	}
	mcd := resourceread.ReadDaemonSetV1OrDie(mcdBytes)

	_, updated, err := resourceapply.ApplyDaemonSet(optr.kubeClient.AppsV1(), mcd)
	if err != nil {
		return err
	}
	if updated {
		return optr.waitForDaemonsetRollout(mcd)
	}
	return nil
}

func (optr *Operator) syncMachineConfigServer(config renderConfig) error {
	crBytes, err := renderAsset(config, "manifests/machineconfigserver/clusterrole.yaml")
	if err != nil {
		return err
	}
	cr := resourceread.ReadClusterRoleV1OrDie(crBytes)
	_, _, err = resourceapply.ApplyClusterRole(optr.kubeClient.RbacV1(), cr)
	if err != nil {
		return err
	}

	crbs := []string{
		"manifests/machineconfigserver/clusterrolebinding.yaml",
		"manifests/machineconfigserver/csr-bootstrap-role-binding.yaml",
		"manifests/machineconfigserver/csr-renewal-role-binding.yaml",
	}
	for _, crb := range crbs {
		b, err := renderAsset(config, crb)
		if err != nil {
			return err
		}
		obj := resourceread.ReadClusterRoleBindingV1OrDie(b)
		_, _, err = resourceapply.ApplyClusterRoleBinding(optr.kubeClient.RbacV1(), obj)
		if err != nil {
			return err
		}
	}

	sas := []string{
		"manifests/machineconfigserver/sa.yaml",
		"manifests/machineconfigserver/node-bootstrapper-sa.yaml",
	}
	for _, sa := range sas {
		b, err := renderAsset(config, sa)
		if err != nil {
			return err
		}
		obj := resourceread.ReadServiceAccountV1OrDie(b)
		_, _, err = resourceapply.ApplyServiceAccount(optr.kubeClient.CoreV1(), obj)
		if err != nil {
			return err
		}
	}

	nbtBytes, err := renderAsset(config, "manifests/machineconfigserver/node-bootstrapper-token.yaml")
	if err != nil {
		return err
	}
	nbt := resourceread.ReadSecretV1OrDie(nbtBytes)
	_, _, err = resourceapply.ApplySecret(optr.kubeClient.CoreV1(), nbt)
	if err != nil {
		return err
	}

	mcsBytes, err := renderAsset(config, "manifests/machineconfigserver/daemonset.yaml")
	if err != nil {
		return err
	}

	mcs := resourceread.ReadDaemonSetV1OrDie(mcsBytes)

	_, updated, err := resourceapply.ApplyDaemonSet(optr.kubeClient.AppsV1(), mcs)
	if err != nil {
		return err
	}
	if updated {
		return optr.waitForDaemonsetRollout(mcs)
	}
	return nil
}

// syncRequiredMachineConfigPools ensures that all the nodes in machineconfigpools labeled with requiredForUpgradeMachineConfigPoolLabelKey
// have updated to the latest configuration.
func (optr *Operator) syncRequiredMachineConfigPools(config renderConfig) error {
	sel, err := metav1.LabelSelectorAsSelector(metav1.AddLabelToSelector(&metav1.LabelSelector{}, requiredForUpgradeMachineConfigPoolLabelKey, ""))
	if err != nil {
		return err
	}
	isPoolStatusConditionTrue := func(pool *mcfgv1.MachineConfigPool, conditionType mcfgv1.MachineConfigPoolConditionType) bool {
		for _, condition := range pool.Status.Conditions {
			if condition.Type == conditionType {
				return condition.Status == corev1.ConditionTrue
			}
		}
		return false
	}

	var lastErr error
	if err := wait.Poll(time.Second, 10*time.Minute, func() (bool, error) {
		if lastErr != nil {
			co, err := optr.fetchClusterOperator()
			if err != nil {
				lastErr = errors.Wrapf(lastErr, "failed to fetch clusteroperator: %v", err)
				return false, nil
			}
			if co == nil {
				glog.Warning("no clusteroperator for machine-config")
				return false, nil
			}
			optr.setOperatorStatusExtension(&co.Status, lastErr)
			_, err = optr.configClient.ConfigV1().ClusterOperators().UpdateStatus(co)
			if err != nil {
				lastErr = errors.Wrapf(lastErr, "failed to update clusteroperator: %v", err)
				return false, nil
			}
		}
		pools, err := optr.mcpLister.List(sel)
		if err != nil {
			lastErr = err
			return false, nil
		}
		for _, pool := range pools {
			if err := isMachineConfigPoolConfigurationValid(pool, version.Hash, optr.mcLister.Get); err != nil {
				lastErr = fmt.Errorf("pool %s has not progressed to latest configuration: %v, retrying", pool.Name, err)
				return false, nil
			}
			degraded := isPoolStatusConditionTrue(pool, mcfgv1.MachineConfigPoolDegraded)
			if pool.Generation <= pool.Status.ObservedGeneration &&
				isPoolStatusConditionTrue(pool, mcfgv1.MachineConfigPoolUpdated) &&
				!degraded {
				continue
			}
			lastErr = fmt.Errorf("error pool %s is not ready, retrying. Status: (pool degraded: %v total: %d, ready %d, updated: %d, unavailable: %d)", pool.Name, degraded, pool.Status.MachineCount, pool.Status.ReadyMachineCount, pool.Status.UpdatedMachineCount, pool.Status.UnavailableMachineCount)
			return false, nil
		}
		return true, nil
	}); err != nil {
		if err == wait.ErrWaitTimeout {
			return fmt.Errorf("%v during syncRequiredMachineConfigPools: %v", err, lastErr)
		}
		return err
	}
	return nil
}

const (
	deploymentRolloutPollInterval = time.Second
	deploymentRolloutTimeout      = 10 * time.Minute

	daemonsetRolloutPollInterval = time.Second
	daemonsetRolloutTimeout      = 10 * time.Minute

	customResourceReadyInterval = time.Second
	customResourceReadyTimeout  = 10 * time.Minute

	controllerConfigCompletedInterval = time.Second
	controllerConfigCompletedTimeout  = 5 * time.Minute
)

func (optr *Operator) waitForCustomResourceDefinition(resource *apiextv1beta1.CustomResourceDefinition) error {
	var lastErr error
	if err := wait.Poll(customResourceReadyInterval, customResourceReadyTimeout, func() (bool, error) {
		crd, err := optr.crdLister.Get(resource.Name)
		if err != nil {
			lastErr = fmt.Errorf("error getting CustomResourceDefinition %s: %v", resource.Name, err)
			return false, nil
		}

		for _, condition := range crd.Status.Conditions {
			if condition.Type == apiextv1beta1.Established && condition.Status == apiextv1beta1.ConditionTrue {
				return true, nil
			}
		}
		lastErr = fmt.Errorf("CustomResourceDefinition %s is not ready. conditions: %v", crd.Name, crd.Status.Conditions)
		return false, nil
	}); err != nil {
		if err == wait.ErrWaitTimeout {
			return fmt.Errorf("%v during syncCustomResourceDefinitions: %v", err, lastErr)
		}
		return err
	}
	return nil
}

func (optr *Operator) waitForDeploymentRollout(resource *appsv1.Deployment) error {
	var lastErr error
	if err := wait.Poll(deploymentRolloutPollInterval, deploymentRolloutTimeout, func() (bool, error) {
		d, err := optr.deployLister.Deployments(resource.Namespace).Get(resource.Name)
		if apierrors.IsNotFound(err) {
			// exit early to recreate the deployment.
			return false, err
		}
		if err != nil {
			// Do not return error here, as we could be updating the API Server itself, in which case we
			// want to continue waiting.
			lastErr = fmt.Errorf("error getting Deployment %s during rollout: %v", resource.Name, err)
			return false, nil
		}

		if d.DeletionTimestamp != nil {
			return false, fmt.Errorf("Deployment %s is being deleted", resource.Name)
		}

		if d.Generation <= d.Status.ObservedGeneration && d.Status.UpdatedReplicas == d.Status.Replicas && d.Status.UnavailableReplicas == 0 {
			return true, nil
		}
		lastErr = fmt.Errorf("Deployment %s is not ready. status: (replicas: %d, updated: %d, ready: %d, unavailable: %d)", d.Name, d.Status.Replicas, d.Status.UpdatedReplicas, d.Status.ReadyReplicas, d.Status.UnavailableReplicas)
		return false, nil
	}); err != nil {
		if err == wait.ErrWaitTimeout {
			return fmt.Errorf("%v during waitForDeploymentRollout: %v", err, lastErr)
		}
		return err
	}
	return nil
}

func (optr *Operator) waitForDaemonsetRollout(resource *appsv1.DaemonSet) error {
	var lastErr error
	if err := wait.Poll(daemonsetRolloutPollInterval, daemonsetRolloutTimeout, func() (bool, error) {
		d, err := optr.daemonsetLister.DaemonSets(resource.Namespace).Get(resource.Name)
		if apierrors.IsNotFound(err) {
			// exit early to recreate the daemonset.
			return false, err
		}
		if err != nil {
			// Do not return error here, as we could be updating the API Server itself, in which case we
			// want to continue waiting.
			lastErr = fmt.Errorf("error getting Daemonset %s during rollout: %v", resource.Name, err)
			return false, nil
		}

		if d.DeletionTimestamp != nil {
			return false, fmt.Errorf("Deployment %s is being deleted", resource.Name)
		}

		if d.Generation <= d.Status.ObservedGeneration && d.Status.UpdatedNumberScheduled == d.Status.DesiredNumberScheduled && d.Status.NumberUnavailable == 0 {
			return true, nil
		}
		lastErr = fmt.Errorf("Daemonset %s is not ready. status: (desired: %d, updated: %d, ready: %d, unavailable: %d)", d.Name, d.Status.DesiredNumberScheduled, d.Status.UpdatedNumberScheduled, d.Status.NumberReady, d.Status.NumberAvailable)
		return false, nil
	}); err != nil {
		if err == wait.ErrWaitTimeout {
			return fmt.Errorf("%v during waitForDaemonsetRollout: %v", err, lastErr)
		}
		return err
	}
	return nil
}

func (optr *Operator) waitForControllerConfigToBeCompleted(resource *mcfgv1.ControllerConfig) error {
	var lastErr error
	if err := wait.Poll(controllerConfigCompletedInterval, controllerConfigCompletedTimeout, func() (bool, error) {
		if err := mcfgv1.IsControllerConfigCompleted(resource.GetName(), optr.ccLister.Get); err != nil {
			lastErr = fmt.Errorf("controllerconfig is not completed: %v", err)
			return false, nil
		}
		return true, nil
	}); err != nil {
		if err == wait.ErrWaitTimeout {
			return fmt.Errorf("%v during waitForControllerConfigToBeCompleted: %v", err, lastErr)
		}
		return err
	}
	return nil
}

const (
	requiredForUpgradeMachineConfigPoolLabelKey = "operator.machineconfiguration.openshift.io/required-for-upgrade"
)

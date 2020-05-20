package operator

import (
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io/ioutil"
	"strings"
	"time"

	"github.com/golang/glog"
	"github.com/pkg/errors"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiextv1beta1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1beta1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"

	configv1 "github.com/openshift/api/config/v1"
	"github.com/openshift/machine-config-operator/lib/resourceapply"
	"github.com/openshift/machine-config-operator/lib/resourceread"
	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	templatectrl "github.com/openshift/machine-config-operator/pkg/controller/template"
	"github.com/openshift/machine-config-operator/pkg/operator/assets"
	"github.com/openshift/machine-config-operator/pkg/version"
)

const (
	requiredForUpgradeMachineConfigPoolLabelKey = "operator.machineconfiguration.openshift.io/required-for-upgrade"
)

type syncFunc struct {
	name string
	fn   func(config *renderConfig) error
}

type syncError struct {
	task string
	err  error
}

func (optr *Operator) syncAll(syncFuncs []syncFunc) error {
	if err := optr.syncProgressingStatus(); err != nil {
		return fmt.Errorf("error syncing progressing status: %v", err)
	}

	var syncErr syncError
	for _, sf := range syncFuncs {
		startTime := time.Now()
		syncErr = syncError{
			task: sf.name,
			err:  sf.fn(optr.renderConfig),
		}
		if optr.inClusterBringup {
			glog.Infof("[init mode] synced %s in %v", sf.name, time.Since(startTime))
		}
		if syncErr.err != nil {
			break
		}
		if err := optr.clearDegradedStatus(sf.name); err != nil {
			return fmt.Errorf("error clearing degraded status: %v", err)
		}
	}

	if err := optr.syncDegradedStatus(syncErr); err != nil {
		return fmt.Errorf("error syncing degraded status: %v", err)
	}

	if err := optr.syncAvailableStatus(); err != nil {
		return fmt.Errorf("error syncing available status: %v", err)
	}

	if err := optr.syncUpgradeableStatus(); err != nil {
		return fmt.Errorf("error syncing upgradeble status: %v", err)
	}

	if err := optr.syncVersion(); err != nil {
		return fmt.Errorf("error syncing version: %v", err)
	}

	if err := optr.syncRelatedObjects(); err != nil {
		return fmt.Errorf("error syncing relatedObjects: %v", err)
	}

	if optr.inClusterBringup && syncErr.err == nil {
		glog.Infof("Initialization complete")
		optr.inClusterBringup = false
	}

	return syncErr.err
}

func (optr *Operator) syncRenderConfig(_ *renderConfig) error {
	if err := optr.syncCustomResourceDefinitions(); err != nil {
		return err
	}

	if optr.inClusterBringup {
		glog.V(4).Info("Starting inClusterBringup informers cache sync")
		// sync now our own informers after having installed the CRDs
		if !cache.WaitForCacheSync(optr.stopCh, optr.mcpListerSynced, optr.mcListerSynced, optr.ccListerSynced) {
			return errors.New("failed to sync caches for informers")
		}
		glog.V(4).Info("Finished inClusterBringup informers cache sync")
	}

	// sync up the images used by operands.
	imgsRaw, err := ioutil.ReadFile(optr.imagesFile)
	if err != nil {
		return err
	}
	imgs := Images{}
	if err := json.Unmarshal(imgsRaw, &imgs); err != nil {
		return err
	}

	optrVersion, _ := optr.vStore.Get("operator")
	if imgs.ReleaseVersion != optrVersion {
		return fmt.Errorf("refusing to read images.json version %q, operator version %q", imgs.ReleaseVersion, optrVersion)
	}

	// sync up CAs
	etcdCA, err := optr.getCAsFromConfigMap("openshift-config", "etcd-serving-ca", "ca-bundle.crt")
	if err != nil {
		return err
	}
	etcdMetricCA, err := optr.getCAsFromConfigMap("openshift-config", "etcd-metric-serving-ca", "ca-bundle.crt")
	if err != nil {
		return err
	}
	rootCA, err := optr.getCAsFromConfigMap("kube-system", "root-ca", "ca.crt")
	if err != nil {
		return err
	}
	// as described by the name this is essentially static, but it no worse than what was here before.  Since changes disrupt workloads
	// and since must perfectly match what the installer creates, this is effectively frozen in time.
	initialKubeAPIServerServingCABytes, err := optr.getCAsFromConfigMap("openshift-config", "initial-kube-apiserver-server-ca", "ca-bundle.crt")
	if err != nil {
		return err
	}

	// Fetch the following configmap and merge into the the initial CA. The CA is the same for the first year, and will rotate
	// automatically afterwards.
	kubeAPIServerServingCABytes, err := optr.getCAsFromConfigMap("openshift-kube-apiserver-operator", "kube-apiserver-to-kubelet-client-ca", "ca-bundle.crt")
	if err != nil {
		kubeAPIServerServingCABytes = initialKubeAPIServerServingCABytes
	} else {
		kubeAPIServerServingCABytes = mergeCertWithCABundle(initialKubeAPIServerServingCABytes, kubeAPIServerServingCABytes, "kube-apiserver-to-kubelet-signer")
	}

	bundle := make([]byte, 0)
	bundle = append(bundle, rootCA...)
	bundle = append(bundle, kubeAPIServerServingCABytes...)

	// sync up os image url
	// TODO: this should probably be part of the imgs
	osimageurl, err := optr.getOsImageURL(optr.namespace)
	if err != nil {
		return err
	}
	imgs.MachineOSContent = osimageurl

	// sync up the ControllerConfigSpec
	infra, network, proxy, err := optr.getGlobalConfig()
	if err != nil {
		return err
	}
	spec, err := createDiscoveredControllerConfigSpec(infra, network, proxy)
	if err != nil {
		return err
	}

	var trustBundle []byte
	certPool := x509.NewCertPool()
	// this is the generic trusted bundle for things like self-signed registries.
	additionalTrustBundle, err := optr.getCAsFromConfigMap("openshift-config", "user-ca-bundle", "ca-bundle.crt")
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	if len(additionalTrustBundle) > 0 {
		if !certPool.AppendCertsFromPEM(additionalTrustBundle) {
			return fmt.Errorf("configmap %s/%s doesn't have a valid PEM bundle", "openshift-config", "user-ca-bundle")
		}
		trustBundle = append(trustBundle, additionalTrustBundle...)
	}

	// this is the trusted bundle specific for proxy things and can differ from the generic one above.
	if proxy != nil && proxy.Spec.TrustedCA.Name != "" && proxy.Spec.TrustedCA.Name != "user-ca-bundle" {
		proxyTrustBundle, err := optr.getCAsFromConfigMap("openshift-config", proxy.Spec.TrustedCA.Name, "ca-bundle.crt")
		if err != nil {
			return err
		}
		if len(proxyTrustBundle) > 0 {
			if !certPool.AppendCertsFromPEM(proxyTrustBundle) {
				return fmt.Errorf("configmap %s/%s doesn't have a valid PEM bundle", "openshift-config", proxy.Spec.TrustedCA.Name)
			}
			trustBundle = append(trustBundle, proxyTrustBundle...)
		}
	}
	spec.AdditionalTrustBundle = trustBundle

	// if the cloudConfig is set in infra read the cloud config reference
	if infra.Spec.CloudConfig.Name != "" {
		cc, err := optr.getCloudConfigFromConfigMap("openshift-config", infra.Spec.CloudConfig.Name, infra.Spec.CloudConfig.Key)
		if err != nil {
			return err
		}
		spec.CloudProviderConfig = cc

		caCert, err := optr.getCAsFromConfigMap("openshift-config", infra.Spec.CloudConfig.Name, "ca-bundle.pem")
		if err == nil {
			spec.CloudProviderCAData = caCert
		}
	}

	if nonHAEtcd, err := optr.getEtcdNonHAOverride(); err == nil && nonHAEtcd {
		glog.V(4).Info("Non HA override in etcd found")
		spec.EtcdQuorumGuardReplicas = 1
	} else {
		spec.EtcdQuorumGuardReplicas = 3
	}

	spec.KubeAPIServerServingCAData = kubeAPIServerServingCABytes
	spec.EtcdCAData = etcdCA
	spec.EtcdMetricCAData = etcdMetricCA
	spec.RootCAData = bundle
	spec.PullSecret = &corev1.ObjectReference{Namespace: "openshift-config", Name: "pull-secret"}
	spec.OSImageURL = imgs.MachineOSContent
	spec.Images = map[string]string{
		templatectrl.EtcdImageKey:                imgs.Etcd,
		templatectrl.SetupEtcdEnvKey:             imgs.MachineConfigOperator,
		templatectrl.GCPRoutesControllerKey:      imgs.MachineConfigOperator,
		templatectrl.InfraImageKey:               imgs.InfraImage,
		templatectrl.KubeClientAgentImageKey:     imgs.KubeClientAgent,
		templatectrl.ClusterEtcdOperatorImageKey: imgs.ClusterEtcdOperator,
		templatectrl.KeepalivedKey:               imgs.Keepalived,
		templatectrl.CorednsKey:                  imgs.Coredns,
		templatectrl.MdnsPublisherKey:            imgs.MdnsPublisher,
		templatectrl.HaproxyKey:                  imgs.Haproxy,
		templatectrl.BaremetalRuntimeCfgKey:      imgs.BaremetalRuntimeCfg,
	}

	// create renderConfig
	optr.renderConfig = getRenderConfig(optr.namespace, string(kubeAPIServerServingCABytes), spec, &imgs.RenderConfigImages, infra.Status.APIServerInternalURL)
	return nil
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

func (optr *Operator) syncMachineConfigPools(config *renderConfig) error {
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

func (optr *Operator) syncMachineConfigController(config *renderConfig) error {
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

func (optr *Operator) syncMachineConfigDaemon(config *renderConfig) error {
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

	// Only generate a new proxy cookie secret if the secret does not exist or if it has been deleted.
	_, err = optr.kubeClient.CoreV1().Secrets(config.TargetNamespace).Get("cookie-secret", metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		cookieSecretBytes, err := renderAsset(config, "manifests/machineconfigdaemon/cookie-secret.yaml")
		if err != nil {
			return err
		}
		cookieSecret := resourceread.ReadSecretV1OrDie(cookieSecretBytes)
		_, _, err = resourceapply.ApplySecret(optr.kubeClient.CoreV1(), cookieSecret)
		if err != nil {
			return err
		}
	} else if err != nil {
		return err
	}

	etcdQuorumGuardBytes, err := renderAsset(config, "manifests/etcdquorumguard_deployment.yaml")
	if err != nil {
		return err
	}
	etcdQuorumGuard := resourceread.ReadDeploymentV1OrDie(etcdQuorumGuardBytes)
	_, _, err = resourceapply.ApplyDeployment(optr.kubeClient.AppsV1(), etcdQuorumGuard)
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

func (optr *Operator) syncMachineConfigServer(config *renderConfig) error {
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
func (optr *Operator) syncRequiredMachineConfigPools(_ *renderConfig) error {
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

//nolint:dupl
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

//nolint:dupl
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
		lastErr = fmt.Errorf("Daemonset %s is not ready. status: (desired: %d, updated: %d, ready: %d, unavailable: %d)", d.Name, d.Status.DesiredNumberScheduled, d.Status.UpdatedNumberScheduled, d.Status.NumberReady, d.Status.NumberUnavailable)
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

func (optr *Operator) getOsImageURL(namespace string) (string, error) {
	cm, err := optr.mcoCmLister.ConfigMaps(namespace).Get(osImageConfigMapName)
	if err != nil {
		return "", err
	}
	releaseVersion := cm.Data["releaseVersion"]
	optrVersion, _ := optr.vStore.Get("operator")
	if releaseVersion != optrVersion {
		return "", fmt.Errorf("refusing to read osImageURL version %q, operator version %q", releaseVersion, optrVersion)
	}
	return cm.Data["osImageURL"], nil
}

func (optr *Operator) getCAsFromConfigMap(namespace, name, key string) ([]byte, error) {
	cm, err := optr.clusterCmLister.ConfigMaps(namespace).Get(name)
	if err != nil {
		return nil, err
	}
	if bd, bdok := cm.BinaryData[key]; bdok {
		return bd, nil
	} else if d, dok := cm.Data[key]; dok {
		raw, err := base64.StdEncoding.DecodeString(d)
		if err != nil {
			// this is actually the result of a bad assumption.  configmap values are not encoded.
			// After the installer pull merges, this entire attempt to decode can go away.
			return []byte(d), nil
		}
		return raw, nil
	} else {
		return nil, fmt.Errorf("%s not found in %s/%s", key, namespace, name)
	}
}

func (optr *Operator) getCloudConfigFromConfigMap(namespace, name, key string) (string, error) {
	cm, err := optr.clusterCmLister.ConfigMaps(namespace).Get(name)
	if err != nil {
		return "", err
	}
	if cc, ok := cm.Data[key]; ok {
		return cc, nil
	}
	return "", fmt.Errorf("%s not found in %s/%s", key, namespace, name)
}

// getGlobalConfig gets global configuration for the cluster, namely, the Infrastructure and Network types.
// Each type of global configuration is named `cluster` for easy discovery in the cluster.
func (optr *Operator) getGlobalConfig() (*configv1.Infrastructure, *configv1.Network, *configv1.Proxy, error) {
	infra, err := optr.infraLister.Get("cluster")
	if err != nil {
		return nil, nil, nil, err
	}
	network, err := optr.networkLister.Get("cluster")
	if err != nil {
		return nil, nil, nil, err
	}
	proxy, err := optr.proxyLister.Get("cluster")
	if err != nil && !apierrors.IsNotFound(err) {
		return nil, nil, nil, err
	}
	return infra, network, proxy, nil
}

func (optr *Operator) getEtcdNonHAOverride() (bool, error) {
	if optr.etcdLister == nil {
		// if the resource is not found, i.e. it is not created by CVO
		// which means cluster-etcd-operator images is not part of CVO
		glog.V(4).Info("etcd cr not found")
		return false, nil
	}
	etcd, err := optr.etcdLister.Get("cluster")
	if err != nil {
		if apierrors.IsNotFound(err) {
			glog.V(4).Info("etcd cluster object not found")
			return false, nil
		}
		return false, fmt.Errorf("error getting etcd CR: %#v", err)
	}

	if len(etcd.Spec.UnsupportedConfigOverrides.Raw) == 0 {
		glog.V(4).Info("no unsupported overrides found")
		return false, nil
	}
	overrides := string(etcd.Spec.UnsupportedConfigOverrides.Raw)

	expected := string(runtime.RawExtension{
		Raw: []byte("{\"useUnsupportedUnsafeNonHANonProductionUnstableEtcd\":true}")}.Raw)
	if overrides == expected {
		glog.V(4).Info("non HA override found")
		return true, nil
	}
	return false, nil
}

func getRenderConfig(tnamespace, kubeAPIServerServingCA string, ccSpec *mcfgv1.ControllerConfigSpec, imgs *RenderConfigImages, apiServerURL string) *renderConfig {
	return &renderConfig{
		TargetNamespace:        tnamespace,
		Version:                version.Raw,
		ControllerConfig:       *ccSpec,
		Images:                 imgs,
		APIServerURL:           apiServerURL,
		KubeAPIServerServingCA: kubeAPIServerServingCA,
	}
}

func mergeCertWithCABundle(initialBundle, newBundle []byte, subject string) []byte {
	mergedBytes := []byte{}
	for len(initialBundle) > 0 {
		b, next := pem.Decode(initialBundle)
		if b == nil {
			break
		}
		c, err := x509.ParseCertificate(b.Bytes)
		if err != nil {
			glog.Warningf("Could not parse initial bundle certificate: %v", err)
			continue
		}
		if strings.Contains(c.Subject.String(), subject) {
			// merge and replace this cert with the new one
			mergedBytes = append(mergedBytes, newBundle...)
		} else {
			// merge the original cert
			mergedBytes = append(mergedBytes, pem.EncodeToMemory(b)...)
		}
		initialBundle = next
	}
	return mergedBytes
}

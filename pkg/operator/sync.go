package operator

import (
	"context"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io/ioutil"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/golang/glog"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	kubeErrs "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"

	configv1 "github.com/openshift/api/config/v1"
	"github.com/openshift/library-go/pkg/operator/resource/resourceapply"
	"github.com/openshift/library-go/pkg/operator/resource/resourceread"
	mcoResourceApply "github.com/openshift/machine-config-operator/lib/resourceapply"
	mcoResourceRead "github.com/openshift/machine-config-operator/lib/resourceread"
	"github.com/openshift/machine-config-operator/manifests"
	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	templatectrl "github.com/openshift/machine-config-operator/pkg/controller/template"
	daemonconsts "github.com/openshift/machine-config-operator/pkg/daemon/constants"
	"github.com/openshift/machine-config-operator/pkg/server"
	"github.com/openshift/machine-config-operator/pkg/version"
)

const (
	requiredForUpgradeMachineConfigPoolLabelKey = "operator.machineconfiguration.openshift.io/required-for-upgrade"
)

var (
	platformsRequiringCloudConf = sets.NewString(
		string(configv1.AzurePlatformType),
		string(configv1.GCPPlatformType),
		string(configv1.OpenStackPlatformType),
		string(configv1.VSpherePlatformType),
	)
)

type manifestPaths struct {
	clusterRoles        []string
	roleBindings        []string
	clusterRoleBindings []string
	serviceAccounts     []string
	secrets             []string
	daemonset           string
}

const (
	// Machine Config Controller manifest paths
	mccClusterRoleManifestPath              = "manifests/machineconfigcontroller/clusterrole.yaml"
	mccEventsClusterRoleManifestPath        = "manifests/machineconfigcontroller/events-clusterrole.yaml"
	mccEventsRoleBindingDefaultManifestPath = "manifests/machineconfigcontroller/events-rolebinding-default.yaml"
	mccEventsRoleBindingTargetManifestPath  = "manifests/machineconfigcontroller/events-rolebinding-target.yaml"
	mccClusterRoleBindingManifestPath       = "manifests/machineconfigcontroller/clusterrolebinding.yaml"
	mccServiceAccountManifestPath           = "manifests/machineconfigcontroller/sa.yaml"

	// Machine Config Daemon manifest paths
	mcdClusterRoleManifestPath              = "manifests/machineconfigdaemon/clusterrole.yaml"
	mcdEventsClusterRoleManifestPath        = "manifests/machineconfigdaemon/events-clusterrole.yaml"
	mcdEventsRoleBindingDefaultManifestPath = "manifests/machineconfigdaemon/events-rolebinding-default.yaml"
	mcdEventsRoleBindingTargetManifestPath  = "manifests/machineconfigdaemon/events-rolebinding-target.yaml"
	mcdClusterRoleBindingManifestPath       = "manifests/machineconfigdaemon/clusterrolebinding.yaml"
	mcdServiceAccountManifestPath           = "manifests/machineconfigdaemon/sa.yaml"
	mcdDaemonsetManifestPath                = "manifests/machineconfigdaemon/daemonset.yaml"

	// Machine Config Server manifest paths
	mcsClusterRoleManifestPath                    = "manifests/machineconfigserver/clusterrole.yaml"
	mcsClusterRoleBindingManifestPath             = "manifests/machineconfigserver/clusterrolebinding.yaml"
	mcsCSRBootstrapRoleBindingManifestPath        = "manifests/machineconfigserver/csr-bootstrap-role-binding.yaml"
	mcsCSRRenewalRoleBindingManifestPath          = "manifests/machineconfigserver/csr-renewal-role-binding.yaml"
	mcsServiceAccountManifestPath                 = "manifests/machineconfigserver/sa.yaml"
	mcsNodeBootstrapperServiceAccountManifestPath = "manifests/machineconfigserver/node-bootstrapper-sa.yaml"
	mcsNodeBootstrapperTokenManifestPath          = "manifests/machineconfigserver/node-bootstrapper-token.yaml"
	mcsDaemonsetManifestPath                      = "manifests/machineconfigserver/daemonset.yaml"
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
		return fmt.Errorf("error syncing progressing status: %w", err)
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
			return fmt.Errorf("error clearing degraded status: %w", err)
		}
	}

	if err := optr.syncDegradedStatus(syncErr); err != nil {
		return fmt.Errorf("error syncing degraded status: %w", err)
	}

	if err := optr.syncAvailableStatus(syncErr); err != nil {
		return fmt.Errorf("error syncing available status: %w", err)
	}

	if err := optr.syncUpgradeableStatus(); err != nil {
		return fmt.Errorf("error syncing upgradeble status: %w", err)
	}

	if err := optr.syncVersion(); err != nil {
		return fmt.Errorf("error syncing version: %w", err)
	}

	if err := optr.syncRelatedObjects(); err != nil {
		return fmt.Errorf("error syncing relatedObjects: %w", err)
	}

	if optr.inClusterBringup && syncErr.err == nil {
		glog.Infof("Initialization complete")
		optr.inClusterBringup = false
	}

	return syncErr.err
}

// Return true if kube-cloud-config ConfigMap is required.
func isKubeCloudConfigCMRequired(infra *configv1.Infrastructure) bool {
	return infra.Spec.CloudConfig.Name != "" || isCloudConfRequired(infra)
}

// Return true if the platform requires a cloud.conf.
func isCloudConfRequired(infra *configv1.Infrastructure) bool {
	if infra.Status.PlatformStatus == nil {
		return false
	}
	return platformsRequiringCloudConf.Has(string(infra.Status.PlatformStatus.Type))
}

// Sync cloud config on supported platform from cloud.conf available in openshift-config-managed/kube-cloud-config ConfigMap.
func (optr *Operator) syncCloudConfig(spec *mcfgv1.ControllerConfigSpec, infra *configv1.Infrastructure) error {
	cm, err := optr.clusterCmLister.ConfigMaps("openshift-config-managed").Get("kube-cloud-config")
	if err != nil {
		if apierrors.IsNotFound(err) {
			if isKubeCloudConfigCMRequired(infra) {
				// Return error only if the kube-cloud-config ConfigMap is required, otherwise proceeds further.
				return fmt.Errorf("%s/%s configmap is required on platform %s but not found: %w",
					"openshift-config-managed", "kube-cloud-config", infra.Status.PlatformStatus.Type, err)
			}
			return nil
		}
		return err
	}
	// Read cloud.conf from openshift-config-managed/kube-cloud-config ConfigMap.
	cc, err := getCloudConfigFromConfigMap(cm, "cloud.conf")
	if err != nil {
		if isCloudConfRequired(infra) {
			// Return error only if cloud.conf is required, otherwise proceeds further.
			return fmt.Errorf("%s/%s configmap must have the %s key on platform %s but not found",
				"openshift-config-managed", "kube-cloud-config", "cloud.conf", infra.Status.PlatformStatus.Type)
		}
	} else {
		spec.CloudProviderConfig = cc
	}

	caCert, err := getCAsFromConfigMap(cm, "ca-bundle.pem")
	if err == nil {
		spec.CloudProviderCAData = caCert
	}
	return nil
}

//nolint:gocyclo
func (optr *Operator) syncRenderConfig(_ *renderConfig) error {
	if err := optr.syncCustomResourceDefinitions(); err != nil {
		return err
	}

	if optr.inClusterBringup {
		glog.V(4).Info("Starting inClusterBringup informers cache sync")
		// sync now our own informers after having installed the CRDs
		if !cache.WaitForCacheSync(optr.stopCh, optr.ccListerSynced) {
			return fmt.Errorf("failed to sync caches for informers")
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

	// sync up os image url
	// TODO: this should probably be part of the imgs
	oscontainer, osextensionscontainer, osimageurl, err := optr.getOsImageURLs(optr.namespace)
	if err != nil {
		return err
	}
	imgs.MachineOSContent = osimageurl
	imgs.BaseOperatingSystemContainer = oscontainer
	imgs.BaseOperatingSystemExtensionsContainer = osextensionscontainer

	// sync up the ControllerConfigSpec
	infra, network, proxy, dns, err := optr.getGlobalConfig()
	if err != nil {
		return err
	}
	spec, err := createDiscoveredControllerConfigSpec(infra, network, proxy, dns)
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

	if err := optr.syncCloudConfig(spec, infra); err != nil {
		return err
	}

	spec.KubeAPIServerServingCAData = kubeAPIServerServingCABytes
	spec.RootCAData = bundle
	spec.PullSecret = &corev1.ObjectReference{Namespace: "openshift-config", Name: "pull-secret"}
	spec.OSImageURL = imgs.MachineOSContent
	spec.BaseOperatingSystemContainer = imgs.BaseOperatingSystemContainer
	spec.BaseOperatingSystemExtensionsContainer = imgs.BaseOperatingSystemExtensionsContainer
	spec.Images = map[string]string{
		templatectrl.MachineConfigOperatorKey: imgs.MachineConfigOperator,

		templatectrl.APIServerWatcherKey:    imgs.MachineConfigOperator,
		templatectrl.InfraImageKey:          imgs.InfraImage,
		templatectrl.KeepalivedKey:          imgs.Keepalived,
		templatectrl.CorednsKey:             imgs.Coredns,
		templatectrl.HaproxyKey:             imgs.Haproxy,
		templatectrl.BaremetalRuntimeCfgKey: imgs.BaremetalRuntimeCfg,
	}

	ignitionHost, err := getIgnitionHost(&infra.Status)
	if err != nil {
		return err
	}

	pointerConfig, err := ctrlcommon.PointerConfig(ignitionHost, rootCA)
	if err != nil {
		return err
	}
	pointerConfigData, err := json.Marshal(pointerConfig)
	if err != nil {
		return err
	}

	// create renderConfig
	optr.renderConfig = getRenderConfig(optr.namespace, string(kubeAPIServerServingCABytes), spec, &imgs.RenderConfigImages, infra.Status.APIServerInternalURL, pointerConfigData)
	return nil
}

func getIgnitionHost(infraStatus *configv1.InfrastructureStatus) (string, error) {
	internalURL := infraStatus.APIServerInternalURL
	internalURLParsed, err := url.Parse(internalURL)
	if err != nil {
		return "", err
	}
	securePortStr := strconv.Itoa(server.SecurePort)
	ignitionHost := fmt.Sprintf("%s:%s", internalURLParsed.Hostname(), securePortStr)
	if infraStatus.PlatformStatus != nil {
		switch infraStatus.PlatformStatus.Type {
		case configv1.BareMetalPlatformType:
			ignitionHost = net.JoinHostPort(infraStatus.PlatformStatus.BareMetal.APIServerInternalIP, securePortStr)
		case configv1.OpenStackPlatformType:
			ignitionHost = net.JoinHostPort(infraStatus.PlatformStatus.OpenStack.APIServerInternalIP, securePortStr)
		case configv1.OvirtPlatformType:
			ignitionHost = net.JoinHostPort(infraStatus.PlatformStatus.Ovirt.APIServerInternalIP, securePortStr)
		case configv1.VSpherePlatformType:
			if infraStatus.PlatformStatus.VSphere != nil {
				if infraStatus.PlatformStatus.VSphere.APIServerInternalIP != "" {
					ignitionHost = net.JoinHostPort(infraStatus.PlatformStatus.VSphere.APIServerInternalIP, securePortStr)
				}
			}
		}
	}

	return ignitionHost, nil
}

func (optr *Operator) syncCustomResourceDefinitions() error {
	crds := []string{
		"manifests/controllerconfig.crd.yaml",
	}

	for _, crd := range crds {
		crdBytes, err := manifests.ReadFile(crd)
		if err != nil {
			return fmt.Errorf("error getting asset %s: %w", crd, err)
		}
		c := resourceread.ReadCustomResourceDefinitionV1OrDie(crdBytes)
		_, updated, err := resourceapply.ApplyCustomResourceDefinitionV1(context.TODO(), optr.apiExtClient.ApiextensionsV1(), optr.libgoRecorder, c)
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
		p := mcoResourceRead.ReadMachineConfigPoolV1OrDie(mcpBytes)
		_, _, err = mcoResourceApply.ApplyMachineConfigPool(optr.client.MachineconfigurationV1(), p)
		if err != nil {
			return err
		}
	}

	userDataTemplatePath := "manifests/userdata_secret.yaml"
	pools, err := optr.mcpLister.List(labels.Everything())
	if err != nil {
		return err
	}
	// base64.StdEncoding.EncodeToString
	for _, pool := range pools {
		pointerConfigAsset := newAssetRenderer("pointer-config")
		pointerConfigAsset.templateData = config.PointerConfig
		pointerConfigData, err := pointerConfigAsset.render(struct{ Role string }{pool.Name})
		if err != nil {
			return err
		}

		userDataAsset := newAssetRenderer(userDataTemplatePath)
		if err := userDataAsset.read(); err != nil {
			return err
		}
		userDataAsset.addTemplateFuncs()
		userdataBytes, err := userDataAsset.render(struct{ Role, PointerConfig string }{
			pool.Name,
			base64.StdEncoding.EncodeToString(pointerConfigData),
		})
		if err != nil {
			return err
		}
		p := resourceread.ReadSecretV1OrDie(userdataBytes)
		_, _, err = resourceapply.ApplySecret(context.TODO(), optr.kubeClient.CoreV1(), optr.libgoRecorder, p)
		if err != nil {
			return err
		}
	}

	return nil
}

func (optr *Operator) applyManifests(config *renderConfig, paths manifestPaths) error {
	for _, path := range paths.clusterRoles {
		crBytes, err := renderAsset(config, path)
		if err != nil {
			return err
		}
		cr := resourceread.ReadClusterRoleV1OrDie(crBytes)
		_, _, err = resourceapply.ApplyClusterRole(context.TODO(), optr.kubeClient.RbacV1(), optr.libgoRecorder, cr)
		if err != nil {
			return err
		}
	}

	for _, path := range paths.roleBindings {
		rbBytes, err := renderAsset(config, path)
		if err != nil {
			return err
		}
		rb := resourceread.ReadRoleBindingV1OrDie(rbBytes)
		_, _, err = resourceapply.ApplyRoleBinding(context.TODO(), optr.kubeClient.RbacV1(), optr.libgoRecorder, rb)
		if err != nil {
			return err
		}
	}

	for _, path := range paths.clusterRoleBindings {
		crbBytes, err := renderAsset(config, path)
		if err != nil {
			return err
		}
		crb := resourceread.ReadClusterRoleBindingV1OrDie(crbBytes)
		_, _, err = resourceapply.ApplyClusterRoleBinding(context.TODO(), optr.kubeClient.RbacV1(), optr.libgoRecorder, crb)
		if err != nil {
			return err
		}
	}

	for _, path := range paths.serviceAccounts {
		saBytes, err := renderAsset(config, path)
		if err != nil {
			return err
		}
		sa := resourceread.ReadServiceAccountV1OrDie(saBytes)
		_, _, err = resourceapply.ApplyServiceAccount(context.TODO(), optr.kubeClient.CoreV1(), optr.libgoRecorder, sa)
		if err != nil {
			return err
		}
	}

	for _, path := range paths.secrets {
		sBytes, err := renderAsset(config, path)
		if err != nil {
			return err
		}
		s := resourceread.ReadSecretV1OrDie(sBytes)
		_, _, err = resourceapply.ApplySecret(context.TODO(), optr.kubeClient.CoreV1(), optr.libgoRecorder, s)
		if err != nil {
			return err
		}
	}

	if paths.daemonset != "" {
		dBytes, err := renderAsset(config, paths.daemonset)
		if err != nil {
			return err
		}
		d := resourceread.ReadDaemonSetV1OrDie(dBytes)
		_, updated, err := mcoResourceApply.ApplyDaemonSet(optr.kubeClient.AppsV1(), d)
		if err != nil {
			return err
		}
		if updated {
			return optr.waitForDaemonsetRollout(d)
		}
	}

	return nil
}

func (optr *Operator) syncMachineConfigController(config *renderConfig) error {
	paths := manifestPaths{
		clusterRoles: []string{
			mccClusterRoleManifestPath,
			mccEventsClusterRoleManifestPath,
		},
		roleBindings: []string{
			mccEventsRoleBindingDefaultManifestPath,
			mccEventsRoleBindingTargetManifestPath,
		},
		clusterRoleBindings: []string{
			mccClusterRoleBindingManifestPath,
		},
		serviceAccounts: []string{
			mccServiceAccountManifestPath,
		},
	}
	if err := optr.applyManifests(config, paths); err != nil {
		return fmt.Errorf("failed to apply machine config controller manifests: %w", err)
	}

	mccBytes, err := renderAsset(config, "manifests/machineconfigcontroller/deployment.yaml")
	if err != nil {
		return err
	}
	mcc := resourceread.ReadDeploymentV1OrDie(mccBytes)

	_, updated, err := mcoResourceApply.ApplyDeployment(optr.kubeClient.AppsV1(), mcc)
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
	cc := mcoResourceRead.ReadControllerConfigV1OrDie(ccBytes)
	// Propagate our binary version into the controller config to help
	// suppress rendered config generation until a corresponding
	// new controller can roll out too.
	// https://bugzilla.redhat.com/show_bug.cgi?id=1879099
	cc.Annotations[daemonconsts.GeneratedByVersionAnnotationKey] = version.Raw

	// add ocp release version as annotation to controller config, for use when
	// annotating rendered configs with same.
	optrVersion, _ := optr.vStore.Get("operator")
	cc.Annotations[ctrlcommon.ReleaseImageVersionAnnotationKey] = optrVersion

	_, _, err = mcoResourceApply.ApplyControllerConfig(optr.client.MachineconfigurationV1(), cc)
	if err != nil {
		return err
	}
	return optr.waitForControllerConfigToBeCompleted(cc)
}

func (optr *Operator) syncMachineConfigDaemon(config *renderConfig) error {
	paths := manifestPaths{
		clusterRoles: []string{
			mcdClusterRoleManifestPath,
			mcdEventsClusterRoleManifestPath,
		},
		roleBindings: []string{
			mcdEventsRoleBindingDefaultManifestPath,
			mcdEventsRoleBindingTargetManifestPath,
		},
		clusterRoleBindings: []string{
			mcdClusterRoleBindingManifestPath,
		},
		serviceAccounts: []string{
			mcdServiceAccountManifestPath,
		},
		daemonset: mcdDaemonsetManifestPath,
	}

	// Only generate a new proxy cookie secret if the secret does not exist or if it has been deleted.
	_, err := optr.kubeClient.CoreV1().Secrets(config.TargetNamespace).Get(context.TODO(), "cookie-secret", metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		paths.secrets = []string{"manifests/machineconfigdaemon/cookie-secret.yaml"}
	} else if err != nil {
		return err
	}

	if err := optr.applyManifests(config, paths); err != nil {
		return fmt.Errorf("failed to apply machine config daemon manifests: %w", err)
	}

	return nil
}

func (optr *Operator) syncMachineConfigServer(config *renderConfig) error {
	paths := manifestPaths{
		clusterRoles: []string{
			mcsClusterRoleManifestPath,
		},
		clusterRoleBindings: []string{
			mcsClusterRoleBindingManifestPath,
			mcsCSRBootstrapRoleBindingManifestPath,
			mcsCSRRenewalRoleBindingManifestPath,
		},
		serviceAccounts: []string{
			mcsServiceAccountManifestPath,
			mcsNodeBootstrapperServiceAccountManifestPath,
		},
		secrets: []string{
			mcsNodeBootstrapperTokenManifestPath,
		},
		daemonset: mcsDaemonsetManifestPath,
	}
	if err := optr.applyManifests(config, paths); err != nil {
		return fmt.Errorf("failed to apply machine config server manifests: %w", err)
	}

	return nil
}

// syncRequiredMachineConfigPools ensures that all the nodes in machineconfigpools labeled with requiredForUpgradeMachineConfigPoolLabelKey
// have updated to the latest configuration.
func (optr *Operator) syncRequiredMachineConfigPools(_ *renderConfig) error {
	var lastErr error
	if err := wait.Poll(time.Second, 10*time.Minute, func() (bool, error) {
		if lastErr != nil {
			co, err := optr.fetchClusterOperator()
			if err != nil {
				errs := kubeErrs.NewAggregate([]error{err, lastErr})
				lastErr = fmt.Errorf("failed to fetch clusteroperator: %w", errs)
				return false, nil
			}
			if co == nil {
				glog.Warning("no clusteroperator for machine-config")
				return false, nil
			}
			optr.setOperatorStatusExtension(&co.Status, lastErr)
			_, err = optr.configClient.ConfigV1().ClusterOperators().UpdateStatus(context.TODO(), co, metav1.UpdateOptions{})
			if err != nil {
				errs := kubeErrs.NewAggregate([]error{err, lastErr})
				lastErr = fmt.Errorf("failed to update clusteroperator: %w", errs)
				return false, nil
			}
		}

		pools, err := optr.mcpLister.List(labels.Everything())
		if err != nil {
			lastErr = err
			return false, nil
		}
		for _, pool := range pools {
			degraded := isPoolStatusConditionTrue(pool, mcfgv1.MachineConfigPoolDegraded)
			if degraded {
				lastErr = fmt.Errorf("error pool %s is not ready, retrying. Status: (pool degraded: %v total: %d, ready %d, updated: %d, unavailable: %d)", pool.Name, degraded, pool.Status.MachineCount, pool.Status.ReadyMachineCount, pool.Status.UpdatedMachineCount, pool.Status.UnavailableMachineCount)
				glog.Errorf("Error syncing Required MachineConfigPools: %q", lastErr)
				syncerr := optr.syncUpgradeableStatus()
				if syncerr != nil {
					glog.Errorf("Error syncingUpgradeableStatus: %q", syncerr)
				}
				return false, nil
			}

			_, hasRequiredPoolLabel := pool.Labels[requiredForUpgradeMachineConfigPoolLabelKey]

			if hasRequiredPoolLabel {
				_, _, opURL, err := optr.getOsImageURLs(optr.namespace)
				if err != nil {
					glog.Errorf("Error getting configmap osImageURL: %q", err)
					return false, nil
				}
				releaseVersion, _ := optr.vStore.Get("operator")
				if err := isMachineConfigPoolConfigurationValid(pool, version.Hash, releaseVersion, opURL, optr.mcLister.Get); err != nil {
					lastErr = fmt.Errorf("pool %s has not progressed to latest configuration: %w, retrying", pool.Name, err)
					syncerr := optr.syncUpgradeableStatus()
					if syncerr != nil {
						glog.Errorf("Error syncingUpgradeableStatus: %q", syncerr)
					}
					return false, nil
				}

				if pool.Generation <= pool.Status.ObservedGeneration &&
					isPoolStatusConditionTrue(pool, mcfgv1.MachineConfigPoolUpdated) {
					continue
				}
				lastErr = fmt.Errorf("error required pool %s is not ready, retrying. Status: (total: %d, ready %d, updated: %d, unavailable: %d, degraded: %d)", pool.Name, pool.Status.MachineCount, pool.Status.ReadyMachineCount, pool.Status.UpdatedMachineCount, pool.Status.UnavailableMachineCount, pool.Status.DegradedMachineCount)
				syncerr := optr.syncUpgradeableStatus()
				if syncerr != nil {
					glog.Errorf("Error syncingUpgradeableStatus: %q", syncerr)
				}
				// If we don't account for pause here, we will spin in this loop until we hit the 10 minute timeout because paused pools can't sync.
				if pool.Spec.Paused {
					return false, fmt.Errorf("Required MachineConfigPool '%s' is paused and can not sync until it is unpaused", pool.Name)
				}
				return false, nil
			}
		}
		return true, nil
	}); err != nil {
		if err == wait.ErrWaitTimeout {
			glog.Errorf("Error syncing Required MachineConfigPools: %q", lastErr)
			errs := kubeErrs.NewAggregate([]error{err, lastErr})
			return fmt.Errorf("error during syncRequiredMachineConfigPools: %w", errs)
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

func (optr *Operator) waitForCustomResourceDefinition(resource *apiextv1.CustomResourceDefinition) error {
	var lastErr error
	if err := wait.Poll(customResourceReadyInterval, customResourceReadyTimeout, func() (bool, error) {
		crd, err := optr.crdLister.Get(resource.Name)
		if err != nil {
			lastErr = fmt.Errorf("error getting CustomResourceDefinition %s: %w", resource.Name, err)
			return false, nil
		}

		for _, condition := range crd.Status.Conditions {
			if condition.Type == apiextv1.Established && condition.Status == apiextv1.ConditionTrue {
				return true, nil
			}
		}
		lastErr = fmt.Errorf("CustomResourceDefinition %s is not ready. conditions: %v", crd.Name, crd.Status.Conditions)
		return false, nil
	}); err != nil {
		if err == wait.ErrWaitTimeout {
			errs := kubeErrs.NewAggregate([]error{err, lastErr})
			return fmt.Errorf("error during syncCustomResourceDefinitions: %w", errs)
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
			lastErr = fmt.Errorf("error getting Deployment %s during rollout: %w", resource.Name, err)
			return false, nil
		}

		if d.DeletionTimestamp != nil {
			return false, fmt.Errorf("deployment %s is being deleted", resource.Name)
		}

		if d.Generation <= d.Status.ObservedGeneration && d.Status.UpdatedReplicas == d.Status.Replicas && d.Status.UnavailableReplicas == 0 {
			return true, nil
		}
		lastErr = fmt.Errorf("deployment %s is not ready. status: (replicas: %d, updated: %d, ready: %d, unavailable: %d)", d.Name, d.Status.Replicas, d.Status.UpdatedReplicas, d.Status.ReadyReplicas, d.Status.UnavailableReplicas)
		return false, nil
	}); err != nil {
		if err == wait.ErrWaitTimeout {
			errs := kubeErrs.NewAggregate([]error{err, lastErr})
			return fmt.Errorf("error during waitForDeploymentRollout: %w", errs)
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
			lastErr = fmt.Errorf("error getting Daemonset %s during rollout: %w", resource.Name, err)
			return false, nil
		}

		if d.DeletionTimestamp != nil {
			return false, fmt.Errorf("deployment %s is being deleted", resource.Name)
		}

		if d.Generation <= d.Status.ObservedGeneration && d.Status.UpdatedNumberScheduled == d.Status.DesiredNumberScheduled && d.Status.NumberUnavailable == 0 {
			return true, nil
		}
		lastErr = fmt.Errorf("daemonset %s is not ready. status: (desired: %d, updated: %d, ready: %d, unavailable: %d)", d.Name, d.Status.DesiredNumberScheduled, d.Status.UpdatedNumberScheduled, d.Status.NumberReady, d.Status.NumberUnavailable)
		return false, nil
	}); err != nil {
		if err == wait.ErrWaitTimeout {
			errs := kubeErrs.NewAggregate([]error{err, lastErr})
			return fmt.Errorf("error during waitForDaemonsetRollout: %w", errs)
		}
		return err
	}
	return nil
}

func (optr *Operator) waitForControllerConfigToBeCompleted(resource *mcfgv1.ControllerConfig) error {
	var lastErr error
	if err := wait.Poll(controllerConfigCompletedInterval, controllerConfigCompletedTimeout, func() (bool, error) {
		if err := mcfgv1.IsControllerConfigCompleted(resource.GetName(), optr.ccLister.Get); err != nil {
			lastErr = fmt.Errorf("controllerconfig is not completed: %w", err)
			return false, nil
		}
		return true, nil
	}); err != nil {
		if err == wait.ErrWaitTimeout {
			errs := kubeErrs.NewAggregate([]error{err, lastErr})
			return fmt.Errorf("error during waitForControllerConfigToBeCompleted: %w", errs)
		}
		return err
	}
	return nil
}

// getOsImageURLs returns (new type, new extensions, old type) for operating system update images.
func (optr *Operator) getOsImageURLs(namespace string) (string, string, string, error) {
	cm, err := optr.mcoCmLister.ConfigMaps(namespace).Get(osImageConfigMapName)
	if err != nil {
		return "", "", "", err
	}
	releaseVersion := cm.Data["releaseVersion"]
	optrVersion, _ := optr.vStore.Get("operator")
	if releaseVersion != optrVersion {
		return "", "", "", fmt.Errorf("refusing to read osImageURL version %q, operator version %q", releaseVersion, optrVersion)
	}

	newextensions, hasNewExtensions := cm.Data["baseOperatingSystemExtensionsContainer"]

	newformat, hasNewFormat := cm.Data["baseOperatingSystemContainer"]

	oldformat, hasOldFormat := cm.Data["osImageURL"]

	if hasNewFormat && !hasNewExtensions {
		// TODO(jkyros): This is okay for now because it's not required, but someday this will be bad
	}

	// If we don't have a new format image, and we can't fall back to the old one
	if !hasOldFormat && !hasNewFormat {
		return "", "", "", fmt.Errorf("Missing baseOperatingSystemContainer and osImageURL from configmap")
	}

	return newformat, newextensions, oldformat, nil
}

func (optr *Operator) getCAsFromConfigMap(namespace, name, key string) ([]byte, error) {
	cm, err := optr.clusterCmLister.ConfigMaps(namespace).Get(name)
	if err != nil {
		return nil, err
	}
	return getCAsFromConfigMap(cm, key)
}

func getCAsFromConfigMap(cm *corev1.ConfigMap, key string) ([]byte, error) {
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
		return nil, fmt.Errorf("%s not found in %s/%s", key, cm.Namespace, cm.Name)
	}
}

func (optr *Operator) getCloudConfigFromConfigMap(namespace, name, key string) (string, error) {
	cm, err := optr.clusterCmLister.ConfigMaps(namespace).Get(name)
	if err != nil {
		return "", err
	}
	return getCloudConfigFromConfigMap(cm, key)
}

func getCloudConfigFromConfigMap(cm *corev1.ConfigMap, key string) (string, error) {
	if cc, ok := cm.Data[key]; ok {
		return cc, nil
	}
	return "", fmt.Errorf("%s not found in %s/%s", key, cm.Namespace, cm.Name)
}

// getGlobalConfig gets global configuration for the cluster, namely, the Infrastructure and Network types.
// Each type of global configuration is named `cluster` for easy discovery in the cluster.
func (optr *Operator) getGlobalConfig() (*configv1.Infrastructure, *configv1.Network, *configv1.Proxy, *configv1.DNS, error) {
	infra, err := optr.infraLister.Get("cluster")
	if err != nil {
		return nil, nil, nil, nil, err
	}
	network, err := optr.networkLister.Get("cluster")
	if err != nil {
		return nil, nil, nil, nil, err
	}
	proxy, err := optr.proxyLister.Get("cluster")
	if err != nil && !apierrors.IsNotFound(err) {
		return nil, nil, nil, nil, err
	}
	dns, err := optr.dnsLister.Get("cluster")
	if err != nil && !apierrors.IsNotFound(err) {
		return nil, nil, nil, nil, err
	}
	return infra, network, proxy, dns, nil
}

func getRenderConfig(tnamespace, kubeAPIServerServingCA string, ccSpec *mcfgv1.ControllerConfigSpec, imgs *RenderConfigImages, apiServerURL string, pointerConfigData []byte) *renderConfig {
	return &renderConfig{
		TargetNamespace:        tnamespace,
		Version:                version.Raw,
		ControllerConfig:       *ccSpec,
		Images:                 imgs,
		APIServerURL:           apiServerURL,
		KubeAPIServerServingCA: kubeAPIServerServingCA,
		PointerConfig:          string(pointerConfigData),
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

func isPoolStatusConditionTrue(pool *mcfgv1.MachineConfigPool, conditionType mcfgv1.MachineConfigPoolConditionType) bool {
	for _, condition := range pool.Status.Conditions {
		if condition.Type == conditionType {
			return condition.Status == corev1.ConditionTrue
		}
	}
	return false
}

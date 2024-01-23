package operator

import (
	"context"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	configclientscheme "github.com/openshift/client-go/config/clientset/versioned/scheme"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/apimachinery/pkg/types"
	kubeErrs "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/jsonmergepatch"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	configv1 "github.com/openshift/api/config/v1"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	v1alpha1 "github.com/openshift/api/machineconfiguration/v1alpha1"

	"github.com/openshift/library-go/pkg/operator/resource/resourceapply"
	"github.com/openshift/library-go/pkg/operator/resource/resourceread"
	mcoResourceApply "github.com/openshift/machine-config-operator/lib/resourceapply"
	mcoResourceRead "github.com/openshift/machine-config-operator/lib/resourceread"
	"github.com/openshift/machine-config-operator/manifests"
	"github.com/openshift/machine-config-operator/pkg/apihelpers"
	"github.com/openshift/machine-config-operator/pkg/controller/build"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	templatectrl "github.com/openshift/machine-config-operator/pkg/controller/template"
	daemonconsts "github.com/openshift/machine-config-operator/pkg/daemon/constants"
	"github.com/openshift/machine-config-operator/pkg/server"
	"github.com/openshift/machine-config-operator/pkg/upgrademonitor"
	"github.com/openshift/machine-config-operator/pkg/version"
	autoscalingv1 "k8s.io/api/autoscaling/v1"
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
	configMaps          []string
	roles               []string
}

const (
	// Machine Config Controller manifest paths
	mccClusterRoleManifestPath                = "manifests/machineconfigcontroller/clusterrole.yaml"
	mccEventsClusterRoleManifestPath          = "manifests/machineconfigcontroller/events-clusterrole.yaml"
	mccEventsRoleBindingDefaultManifestPath   = "manifests/machineconfigcontroller/events-rolebinding-default.yaml"
	mccEventsRoleBindingTargetManifestPath    = "manifests/machineconfigcontroller/events-rolebinding-target.yaml"
	mccClusterRoleBindingManifestPath         = "manifests/machineconfigcontroller/clusterrolebinding.yaml"
	mccServiceAccountManifestPath             = "manifests/machineconfigcontroller/sa.yaml"
	mccKubeRbacProxyConfigMapPath             = "manifests/machineconfigcontroller/kube-rbac-proxy-config.yaml"
	mccKubeRbacProxyPrometheusRolePath        = "manifests/machineconfigcontroller/prometheus-rbac.yaml"
	mccKubeRbacProxyPrometheusRoleBindingPath = "manifests/machineconfigcontroller/prometheus-rolebinding-target.yaml"

	// Machine OS Builder manifest paths
	mobClusterRoleManifestPath                      = "manifests/machineosbuilder/clusterrole.yaml"
	mobEventsClusterRoleManifestPath                = "manifests/machineosbuilder/events-clusterrole.yaml"
	mobEventsRoleBindingDefaultManifestPath         = "manifests/machineosbuilder/events-rolebinding-default.yaml"
	mobEventsRoleBindingTargetManifestPath          = "manifests/machineosbuilder/events-rolebinding-target.yaml"
	mobClusterRoleBindingServiceAccountManifestPath = "manifests/machineosbuilder/clusterrolebinding-service-account.yaml"
	mobClusterRolebindingAnyUIDManifestPath         = "manifests/machineosbuilder/clusterrolebinding-anyuid.yaml"
	mobServiceAccountManifestPath                   = "manifests/machineosbuilder/sa.yaml"

	// Machine Config Daemon manifest paths
	mcdClusterRoleManifestPath                = "manifests/machineconfigdaemon/clusterrole.yaml"
	mcdEventsClusterRoleManifestPath          = "manifests/machineconfigdaemon/events-clusterrole.yaml"
	mcdEventsRoleBindingDefaultManifestPath   = "manifests/machineconfigdaemon/events-rolebinding-default.yaml"
	mcdEventsRoleBindingTargetManifestPath    = "manifests/machineconfigdaemon/events-rolebinding-target.yaml"
	mcdClusterRoleBindingManifestPath         = "manifests/machineconfigdaemon/clusterrolebinding.yaml"
	mcdServiceAccountManifestPath             = "manifests/machineconfigdaemon/sa.yaml"
	mcdDaemonsetManifestPath                  = "manifests/machineconfigdaemon/daemonset.yaml"
	mcdKubeRbacProxyConfigMapPath             = "manifests/machineconfigdaemon/kube-rbac-proxy-config.yaml"
	mcdKubeRbacProxyPrometheusRolePath        = "manifests/machineconfigdaemon/prometheus-rbac.yaml"
	mcdKubeRbacProxyPrometheusRoleBindingPath = "manifests/machineconfigdaemon/prometheus-rolebinding-target.yaml"
	mcdRolePath                               = "manifests/machineconfigdaemon/role.yaml"
	mcdRoleBindingPath                        = "manifests/machineconfigdaemon/rolebinding.yaml"

	// Machine Config Server manifest paths
	mcsClusterRoleManifestPath                    = "manifests/machineconfigserver/clusterrole.yaml"
	mcsClusterRoleBindingManifestPath             = "manifests/machineconfigserver/clusterrolebinding.yaml"
	mcsCSRBootstrapRoleBindingManifestPath        = "manifests/machineconfigserver/csr-bootstrap-role-binding.yaml"
	mcsCSRRenewalRoleBindingManifestPath          = "manifests/machineconfigserver/csr-renewal-role-binding.yaml"
	mcsServiceAccountManifestPath                 = "manifests/machineconfigserver/sa.yaml"
	mcsNodeBootstrapperServiceAccountManifestPath = "manifests/machineconfigserver/node-bootstrapper-sa.yaml"
	mcsNodeBootstrapperTokenManifestPath          = "manifests/machineconfigserver/node-bootstrapper-token.yaml"
	mcsDaemonsetManifestPath                      = "manifests/machineconfigserver/daemonset.yaml"

	// Machine OS puller manifest paths
	mopRoleBindingManifestPath    = "manifests/machine-os-puller/rolebinding.yaml"
	mopServiceAccountManifestPath = "manifests/machine-os-puller/sa.yaml"
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
			klog.Infof("[init mode] synced %s in %v", sf.name, time.Since(startTime))
		}
		if syncErr.err != nil {
			// Keep rendering controllerconfig if the daemon sync fails so (among other things)
			// our certificates don't expire.
			// https://bugzilla.redhat.com/show_bug.cgi?id=2034883
			if sf.name == "MachineConfigDaemon" {
				if err := optr.safetySyncControllerConfig(optr.renderConfig); err != nil {
					// we don't want this error to supersede the actual error
					// it's just "oh also, we tried to save you, but that didn't work either"
					klog.Errorf("Error performing safety controllerconfig sync: %v", err)
				}
			}
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

	if err := optr.syncMetrics(); err != nil {
		return fmt.Errorf("error syncing metrics: %w", err)
	}

	if optr.inClusterBringup && syncErr.err == nil {
		klog.Infof("Initialization complete")
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
		klog.V(4).Info("Starting inClusterBringup informers cache sync")
		// sync now our own informers after having installed the CRDs
		if !cache.WaitForCacheSync(optr.stopCh, optr.ccListerSynced) {
			return fmt.Errorf("failed to sync caches for informers")
		}
		klog.V(4).Info("Finished inClusterBringup informers cache sync")
	}

	// sync up the images used by operands.
	imgsRaw, err := os.ReadFile(optr.imagesFile)
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

	// handle image registry certificates.
	// parse these, add them to ctrlcfgspec and then handle these in the daemon write to disk function
	cfg, err := optr.imgLister.Get("cluster")
	if err != nil {
		return err
	}
	imgRegistryUsrData := []mcfgv1.ImageRegistryBundle{}
	if cfg.Spec.AdditionalTrustedCA.Name != "" {
		cm, err := optr.clusterCmLister.ConfigMaps("openshift-config").Get(cfg.Spec.AdditionalTrustedCA.Name)
		if err != nil {
			klog.Warningf("could not find configmap specified in image.config.openshift.io/cluster with the name %s", cfg.Spec.AdditionalTrustedCA.Name)
		} else {
			newKeys := sets.StringKeySet(cm.Data).List()
			newBinaryKeys := sets.StringKeySet(cm.BinaryData).List()
			for _, key := range newKeys {
				raw, err := base64.StdEncoding.DecodeString(cm.Data[key])
				if err != nil {
					imgRegistryUsrData = append(imgRegistryUsrData, mcfgv1.ImageRegistryBundle{
						File: key,
						Data: []byte(cm.Data[key]),
					})
				} else {
					imgRegistryUsrData = append(imgRegistryUsrData, mcfgv1.ImageRegistryBundle{
						File: key,
						Data: raw,
					})
				}
			}
			for _, key := range newBinaryKeys {
				imgRegistryUsrData = append(imgRegistryUsrData, mcfgv1.ImageRegistryBundle{
					File: key,
					Data: cm.BinaryData[key],
				})
			}
		}
	}

	imgRegistryData := []mcfgv1.ImageRegistryBundle{}
	cm, err := optr.clusterCmLister.ConfigMaps("openshift-config-managed").Get("image-registry-ca")
	if err == nil {
		newKeys := sets.StringKeySet(cm.Data).List()
		newBinaryKeys := sets.StringKeySet(cm.BinaryData).List()
		for _, key := range newKeys {
			raw, err := base64.StdEncoding.DecodeString(cm.Data[key])
			if err != nil {
				imgRegistryData = append(imgRegistryData, mcfgv1.ImageRegistryBundle{
					File: key,
					Data: []byte(cm.Data[key]),
				})
			} else {
				imgRegistryData = append(imgRegistryData, mcfgv1.ImageRegistryBundle{
					File: key,
					Data: raw,
				})
			}
		}
		for _, key := range newBinaryKeys {
			imgRegistryData = append(imgRegistryData, mcfgv1.ImageRegistryBundle{
				File: key,
				Data: cm.BinaryData[key],
			})
		}
	}

	mergedData := append(imgRegistryData, imgRegistryUsrData...)
	caData := make(map[string]string, len(mergedData))
	for _, CA := range mergedData {
		caData[CA.File] = string(CA.Data)
	}

	cm, err = optr.clusterCmLister.ConfigMaps("openshift-config-managed").Get("merged-trusted-image-registry-ca")
	if err != nil && !errors.IsNotFound(err) {
		return err
	}
	cmAnnotations := make(map[string]string)
	cmAnnotations["openshift.io/description"] = "Created and managed by the machine-config-operator"
	if err != nil && errors.IsNotFound(err) {
		klog.Infof("creating merged-trusted-image-registry-ca")
		_, err = optr.kubeClient.CoreV1().ConfigMaps("openshift-config-managed").Create(
			context.TODO(),
			&corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "merged-trusted-image-registry-ca",
					Annotations: cmAnnotations,
				},
				Data: caData,
			},
			metav1.CreateOptions{},
		)
		if err != nil {
			return err
		}
	} else {
		cmMarshal, err := json.Marshal(cm)
		if err != nil {
			return err
		}
		newCM := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:        "merged-trusted-image-registry-ca",
				Annotations: cmAnnotations,
			},
			Data: caData,
		}
		newCMMarshal, err := json.Marshal(newCM)
		if err != nil {
			return err
		}

		// check for deletedData
		stillExists := false
		for file, data := range cm.Data {
			stillExists = false
			// for each file in the configmap
			// does it match ANY of the new files? if not, it is deleted.
			for newfile, newdata := range caData {
				if newfile == file && newdata == data {
					stillExists = true
				}
			}
			if !stillExists {
				break
			}
		}
		// check for newData
		for file, data := range caData {
			equal := false
			for oldfile, olddata := range cm.Data {
				if file == oldfile && data == olddata {
					equal = true
				}
			}
			if !equal || (!stillExists && len(cm.Data) > 0) {
				klog.Info("Detecting changes in merged-trusted-image-registry-ca, creating patch")
				if !equal {
					klog.Infof("Diff is between file %s and its data %s which are new", file, data)
				}
				patchBytes, err := jsonmergepatch.CreateThreeWayJSONMergePatch(cmMarshal, newCMMarshal, cmMarshal)
				klog.Infof("JSONPATCH: \n  %s", string(patchBytes))
				if err != nil {
					return err
				}
				_, err = optr.kubeClient.CoreV1().ConfigMaps("openshift-config-managed").Patch(context.TODO(), "merged-trusted-image-registry-ca", types.MergePatchType, patchBytes, metav1.PatchOptions{})
				if err != nil {
					return fmt.Errorf("Could not patch merged-trusted-image-registry-ca with data %s: %w", string(patchBytes), err)
				}
				break
			}
		}
	}

	// sync up CAs
	rootCA, err := optr.getCAsFromConfigMap("kube-system", "root-ca", "ca.crt")
	if err != nil {
		return err
	}

	bootstrapComplete := false
	_, err = optr.clusterCmLister.ConfigMaps("kube-system").Get("bootstrap")
	switch {
	case err == nil:
		bootstrapComplete = true
	case apierrors.IsNotFound(err):
		bootstrapComplete = false
	case err != nil:
		return err
	default:
		panic("math broke")
	}

	var kubeAPIServerServingCABytes []byte
	var internalRegistryPullSecret []byte
	// We only want to switch to the proper certificate bundle *after* we've generated our first controllerconfig.
	// we need to make sure we generate a rendered-config that matches the "day 1" config that our nodes bootstrapped with and
	// if bootstrapComplete happens while inClusterBringup == true, that matching MachineConfig will never get rendered and
	// our pools will degrade (because the machine-config-daemon checks for it).
	// See: https://issues.redhat.com/browse/OCPBUGS-5888
	if bootstrapComplete && !optr.inClusterBringup {
		// This is the ca-bundle published by the kube-apiserver that is used terminate client-certificates in the kube cluster.
		// If the kubelet has a flag to check the in-cluster published ca bundle, that would be ideal.
		kubeAPIServerServingCABytes, err = optr.getCAsFromConfigMap("openshift-config-managed", "kube-apiserver-client-ca", "ca-bundle.crt")
		if err != nil {
			return err
		}
		// Fetch the merged image registry pull secret file. The hope is that, by doing this after cluster bringup, an additional
		// roll of machineconfig can be avoided. This can be moved to the controller once certs are removed from machine configs
		// https://github.com/openshift/machine-config-operator/pull/3787

		internalRegistryPullSecret, err = optr.getImageRegistryPullSecrets()
		if err != nil {
			klog.Errorf("Merging registry secrets failed with: %s", err)
			return err
		}

	} else {
		// this else block exists to find a way to match the rendering hash of the machineconfig that the installer creates.
		// it seems that if the MCO cannot find an existing machineconfig, the operator and machines perma-wedge and it cannot
		// install a new machineconfig.  This is an unfortunate behavior that should be fixed eventually, but we need the kubelet
		// to be able to trust the monitoring stack and this, combined with the no-reboot behavior, is enough to get us unstuck.

		// as described by the name this is essentially static, but it no worse than what was here before.  Since changes disrupt workloads
		// and since must perfectly match what the installer creates, this is effectively frozen in time.
		initialKubeAPIServerServingCABytes, err := optr.getCAsFromConfigMap("openshift-config", "initial-kube-apiserver-server-ca", "ca-bundle.crt")
		if err != nil {
			return err
		}

		// Fetch the following configmap and merge into the the initial CA. The CA is the same for the first year, and will rotate
		// automatically afterwards.
		kubeAPIServerServingCABytes, err = optr.getCAsFromConfigMap("openshift-kube-apiserver-operator", "kube-apiserver-to-kubelet-client-ca", "ca-bundle.crt")
		if err != nil {
			kubeAPIServerServingCABytes = initialKubeAPIServerServingCABytes
		} else {
			kubeAPIServerServingCABytes = mergeCertWithCABundle(initialKubeAPIServerServingCABytes, kubeAPIServerServingCABytes, "kube-apiserver-to-kubelet-signer")
		}

		// Blank out image registry pull secret if cluster is still in bringup
		internalRegistryPullSecret = nil
	}

	bundle := make([]byte, 0)
	bundle = append(bundle, rootCA...)

	// sync up os image url
	// TODO: this should probably be part of the imgs
	oscontainer, osextensionscontainer, err := optr.getOsImageURLs(optr.namespace)
	if err != nil {
		return err
	}
	imgs.BaseOSContainerImage = oscontainer
	imgs.BaseOSExtensionsContainerImage = osextensionscontainer

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
	spec.ImageRegistryBundleData = imgRegistryData
	spec.ImageRegistryBundleUserData = imgRegistryUsrData
	spec.PullSecret = &corev1.ObjectReference{Namespace: "openshift-config", Name: "pull-secret"}
	spec.InternalRegistryPullSecret = internalRegistryPullSecret
	spec.BaseOSContainerImage = imgs.BaseOSContainerImage
	spec.BaseOSExtensionsContainerImage = imgs.BaseOSExtensionsContainerImage
	spec.Images = map[string]string{
		templatectrl.MachineConfigOperatorKey: imgs.MachineConfigOperator,
		templatectrl.APIServerWatcherKey:      imgs.MachineConfigOperator,
		templatectrl.InfraImageKey:            imgs.InfraImage,
		templatectrl.KeepalivedKey:            imgs.Keepalived,
		templatectrl.CorednsKey:               imgs.Coredns,
		templatectrl.HaproxyKey:               imgs.Haproxy,
		templatectrl.BaremetalRuntimeCfgKey:   imgs.BaremetalRuntimeCfg,
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
			ignitionHost = net.JoinHostPort(infraStatus.PlatformStatus.BareMetal.APIServerInternalIPs[0], securePortStr)
		case configv1.OpenStackPlatformType:
			ignitionHost = net.JoinHostPort(infraStatus.PlatformStatus.OpenStack.APIServerInternalIPs[0], securePortStr)
		case configv1.OvirtPlatformType:
			ignitionHost = net.JoinHostPort(infraStatus.PlatformStatus.Ovirt.APIServerInternalIPs[0], securePortStr)
		case configv1.VSpherePlatformType:
			if infraStatus.PlatformStatus.VSphere != nil {
				if len(infraStatus.PlatformStatus.VSphere.APIServerInternalIPs) > 0 {
					ignitionHost = net.JoinHostPort(infraStatus.PlatformStatus.VSphere.APIServerInternalIPs[0], securePortStr)
				}
			}
		}
	}

	return ignitionHost, nil
}

func (optr *Operator) syncCustomResourceDefinitions() error {
	crds := []string{
		"manifests/controllerconfig.crd.yaml",
		"manifests/0000_80_machine-config-operator_01_machineconfignode-TechPreviewNoUpgrade.crd.yaml",
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

		// Work around https://github.com/kubernetes/kubernetes/issues/3030 and https://github.com/kubernetes/kubernetes/issues/80609
		pool.APIVersion = mcfgv1.GroupVersion.String()
		pool.Kind = "MachineConfigPool"

		p.ObjectMeta.OwnerReferences = []metav1.OwnerReference{
			{
				APIVersion: pool.APIVersion,
				Kind:       pool.Kind,
				Name:       pool.ObjectMeta.Name,
				UID:        pool.ObjectMeta.UID,
			},
		}
		_, _, err = resourceapply.ApplySecret(context.TODO(), optr.kubeClient.CoreV1(), optr.libgoRecorder, p)
		if err != nil {
			return err
		}
	}

	return nil
}

// we need to mimic this
func (optr *Operator) syncMachineConfigNodes(_ *renderConfig) error {
	fg, err := optr.fgAccessor.CurrentFeatureGates()
	if err != nil {
		klog.Errorf("Could not get fg: %w", err)
		return err
	}
	if !fg.Enabled(configv1.FeatureGateMachineConfigNodes) {
		return nil
	}
	nodes, err := optr.nodeLister.List(labels.Everything())
	if err != nil {
		return err
	}

	nodeMap := make(map[string]*corev1.Node, len(nodes))
	for _, node := range nodes {
		nodeMap[node.Name] = node
	}

	mcns, err := optr.client.MachineconfigurationV1alpha1().MachineConfigNodes().List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return err
	}
	for _, node := range nodes {
		if node.Status.Phase == corev1.NodePending || node.Status.Phase == corev1.NodePhase("Provisioning") {
			continue
		}
		var pool string
		var ok bool
		if _, ok = node.Labels["node-role.kubernetes.io/worker"]; ok {
			pool = "worker"
		} else if _, ok = node.Labels["node-role.kubernetes.io/master"]; ok {
			pool = "master"
		}
		newMCS := &v1alpha1.MachineConfigNode{
			Spec: v1alpha1.MachineConfigNodeSpec{
				Node: v1alpha1.MCOObjectReference{
					Name: node.Name,
				},
				Pool: v1alpha1.MCOObjectReference{
					Name: pool,
				},
				ConfigVersion: v1alpha1.MachineConfigNodeSpecMachineConfigVersion{
					Desired: upgrademonitor.NotYetSet,
				},
			},
			TypeMeta: metav1.TypeMeta{
				Kind:       "MachineConfigNode",
				APIVersion: "machineconfiguration.openshift.io/v1alpha1",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name: node.Name,
			},
		}
		mcsBytes, err := json.Marshal(newMCS)
		if err != nil {
			klog.Errorf("error rendering asset for MachineConfigNode %w", err)
			return err
		}
		p := mcoResourceRead.ReadMachineConfigNodeV1OrDie(mcsBytes)
		mcn, _, err := mcoResourceApply.ApplyMachineConfigNode(optr.client.MachineconfigurationV1alpha1(), p)
		if err != nil {
			return err
		}
		// if this is the first time we are applying the MCN and the node is ready, set the config version probably
		if mcn.Spec.ConfigVersion.Desired == upgrademonitor.NotYetSet {
			err = upgrademonitor.GenerateAndApplyMachineConfigNodeSpec(optr.fgAccessor, pool, node, optr.client)
			if err != nil {
				klog.Errorf("Error making MCN spec for Update Compatible: %w", err)
			}
		}

	}
	if mcns != nil {
		for _, mcn := range mcns.Items {
			if _, ok := nodeMap[mcn.Name]; !ok {
				klog.Infof("Node %s has been removed, deleting associated MCN", mcn.Name)
				optr.client.MachineconfigurationV1alpha1().MachineConfigNodes().Delete(context.TODO(), mcn.Name, metav1.DeleteOptions{})
			}
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

	for _, path := range paths.roles {
		rBytes, err := renderAsset(config, path)
		if err != nil {
			return err
		}
		r := resourceread.ReadRoleV1OrDie(rBytes)
		_, _, err = resourceapply.ApplyRole(context.TODO(), optr.kubeClient.RbacV1(), optr.libgoRecorder, r)
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

	for _, path := range paths.configMaps {
		cmBytes, err := renderAsset(config, path)
		if err != nil {
			return err
		}
		cm := resourceread.ReadConfigMapV1OrDie(cmBytes)
		_, _, err = resourceapply.ApplyConfigMap(context.TODO(), optr.kubeClient.CoreV1(), optr.libgoRecorder, cm)
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

// safetySyncControllerConfig is a special case render of the controllerconfig that we run when
// we need to keep syncing controllerconfig, but something (like daemon sync failing) is preventing
// us from getting to the actual controller sync
func (optr *Operator) safetySyncControllerConfig(config *renderConfig) error {
	klog.Infof("Performing safety controllerconfig sync")

	// If we have an existing controllerconfig, we might be able to keep rendering
	existingCc, err := optr.ccLister.Get(ctrlcommon.ControllerConfigName)
	if err != nil {
		return err
	}

	// If there is an existing config, but we didn't render it (e.g. we are in the middle of an upgrade)
	// we can't render a new one here, it won't succeed because the existing controller won't touch it
	// and we'll time out waiting.
	if existingCc.Annotations[daemonconsts.GeneratedByVersionAnnotationKey] != version.Raw {
		return fmt.Errorf("Our version (%s) differs from controllerconfig (%s), can't do 'safety' controllerconfig sync until controller is updated", version.Raw, existingCc.Annotations[daemonconsts.GeneratedByVersionAnnotationKey])
	}

	// If we made it here, we should be able to sync controllerconfig, and the existing controller should handle it
	return optr.syncControllerConfig(config)
}

// syncControllerConfig is NOT meant to be called as a separate 'top level" sync in syncAll,
// it is meant to be called as part of syncMachineConfigController because it will only succeed if
// the operator version and controller version match. We can call it from safetySyncControllerConfig
// because safetySyncControllerConfig ensures that the operator and controller versions match before it syncs.
func (optr *Operator) syncControllerConfig(config *renderConfig) error {
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

func (optr *Operator) syncMachineConfigController(config *renderConfig) error {
	paths := manifestPaths{
		clusterRoles: []string{
			mccClusterRoleManifestPath,
			mccEventsClusterRoleManifestPath,
		},
		roles: []string{
			mccKubeRbacProxyPrometheusRolePath,
		},
		roleBindings: []string{
			mccEventsRoleBindingDefaultManifestPath,
			mccEventsRoleBindingTargetManifestPath,
			mccKubeRbacProxyPrometheusRoleBindingPath,
			mopRoleBindingManifestPath,
		},
		clusterRoleBindings: []string{
			mccClusterRoleBindingManifestPath,
		},
		configMaps: []string{
			mccKubeRbacProxyConfigMapPath,
		},
		serviceAccounts: []string{
			mccServiceAccountManifestPath,
			mopServiceAccountManifestPath,
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
	return optr.syncControllerConfig(config)
}

// syncs machine os builder
func (optr *Operator) syncMachineOSBuilder(config *renderConfig) error {
	klog.V(4).Info("Machine OS Builder sync started")
	defer func() {
		klog.V(4).Info("Machine OS Builder sync complete")
	}()

	paths := manifestPaths{
		clusterRoles: []string{
			mobClusterRoleManifestPath,
			mobEventsClusterRoleManifestPath,
		},
		roleBindings: []string{
			mobEventsRoleBindingDefaultManifestPath,
			mobEventsRoleBindingTargetManifestPath,
		},
		clusterRoleBindings: []string{
			mobClusterRoleBindingServiceAccountManifestPath,
			mobClusterRolebindingAnyUIDManifestPath,
		},
		serviceAccounts: []string{
			mobServiceAccountManifestPath,
		},
	}

	// It's probably fine to leave these around if we don't have an opted-in
	// pool, since they don't consume any resources.
	if err := optr.applyManifests(config, paths); err != nil {
		return fmt.Errorf("failed to apply machine os builder manifests: %w", err)
	}

	mobBytes, err := renderAsset(config, "manifests/machineosbuilder/deployment.yaml")
	if err != nil {
		return fmt.Errorf("could not render Machine OS Builder deployment asset: %w", err)
	}

	mob := resourceread.ReadDeploymentV1OrDie(mobBytes)

	return optr.reconcileMachineOSBuilder(mob)
}

// Determines if the Machine OS Builder deployment is in the correct state
// based upon whether we have opted-in pools or not.
func (optr *Operator) reconcileMachineOSBuilder(mob *appsv1.Deployment) error {
	// First, check if we have any MachineConfigPools opted in.
	layeredMCPs, err := optr.getLayeredMachineConfigPools()
	if err != nil {
		return fmt.Errorf("could not get layered MachineConfigPools: %w", err)
	}

	isRunning, err := optr.isMachineOSBuilderRunning(mob)
	// An unknown error occurred. Bail out here.
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("could not determine if Machine OS Builder is running: %w", err)
	}

	// If we have opted-in pools and the Machine OS Builder deployment is either
	// not running or doesn't have the correct replica count, scale it up.
	correctReplicaCount := optr.hasCorrectReplicaCount(mob)
	if len(layeredMCPs) != 0 && (!isRunning || !correctReplicaCount) {
		if !correctReplicaCount {
			klog.Infof("Adjusting Machine OS Builder pod replica count because MachineConfigPool(s) opted into layering")
			return optr.updateMachineOSBuilderDeployment(mob, 1)
		}
		klog.Infof("Starting Machine OS Builder pod because MachineConfigPool(s) opted into layering")
		return optr.startMachineOSBuilderDeployment(mob)
	}

	// If we do not have opted-in pools and the Machine OS Builder deployment is
	// running, scale it down.
	if len(layeredMCPs) == 0 && isRunning {
		klog.Infof("Shutting down Machine OS Builder pod because no MachineConfigPool(s) opted into layering")
		return optr.stopMachineOSBuilderDeployment(mob.Name)
	}

	// No-op if everything is in the desired state.
	return nil
}

// Delete the Machine OS Builder Deployment
func (optr *Operator) stopMachineOSBuilderDeployment(name string) error {
	return optr.kubeClient.AppsV1().Deployments(ctrlcommon.MCONamespace).Delete(context.TODO(), name, metav1.DeleteOptions{})
}

// Determines if the Machine OS Builder has the correct replica count.
func (optr *Operator) hasCorrectReplicaCount(mob *appsv1.Deployment) bool {
	apiMob, err := optr.deployLister.Deployments(ctrlcommon.MCONamespace).Get(mob.Name)
	if err == nil && *apiMob.Spec.Replicas == 1 {
		return true
	}
	return false
}

func (optr *Operator) updateMachineOSBuilderDeployment(mob *appsv1.Deployment, replicas int32) error {
	if err := build.ValidateOnClusterBuildConfig(optr.kubeClient); err != nil {
		return fmt.Errorf("could not update Machine OS Builder deployment: %w", err)
	}

	_, updated, err := mcoResourceApply.ApplyDeployment(optr.kubeClient.AppsV1(), mob)
	if err != nil {
		return fmt.Errorf("could not apply Machine OS Builder deployment: %w", err)
	}

	scale := &autoscalingv1.Scale{
		ObjectMeta: mob.ObjectMeta,
		Spec: autoscalingv1.ScaleSpec{
			Replicas: replicas,
		},
	}

	_, err = optr.kubeClient.AppsV1().Deployments(ctrlcommon.MCONamespace).UpdateScale(context.TODO(), mob.Name, scale, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("could not scale Machine OS Builder: %w", err)
	}

	if updated {
		if err := optr.waitForDeploymentRollout(mob); err != nil {
			return fmt.Errorf("could not wait for Machine OS Builder deployment rollout: %w", err)
		}
	}

	return nil
}

// Determines if the Machine OS Builder is running based upon how many replicas
// we have. If an error is encountered, it is assumed that no Deployments are
// running.
func (optr *Operator) isMachineOSBuilderRunning(mob *appsv1.Deployment) (bool, error) {
	apiMob, err := optr.deployLister.Deployments(ctrlcommon.MCONamespace).Get(mob.Name)

	if err == nil && *apiMob.Spec.Replicas != 0 {
		return true, nil
	}

	return false, err
}

// Updates the Machine OS Builder Deployment, creating it if it does not exist.
func (optr *Operator) startMachineOSBuilderDeployment(mob *appsv1.Deployment) error {
	if err := build.ValidateOnClusterBuildConfig(optr.kubeClient); err != nil {
		return fmt.Errorf("could not start Machine OS Builder: %w", err)
	}

	// start machine os builder deployment
	_, updated, err := mcoResourceApply.ApplyDeployment(optr.kubeClient.AppsV1(), mob)
	if err != nil {
		return fmt.Errorf("could not apply Machine OS Builder deployment: %w", err)
	}

	if updated {
		if err := optr.waitForDeploymentRollout(mob); err != nil {
			return fmt.Errorf("could not wait for Machine OS Builder deployment rollout: %w", err)
		}
	}

	return nil
}

// Returns a list of MachineConfigPools which have opted in to layering.
// Returns an empty list if none have opted in.
func (optr *Operator) getLayeredMachineConfigPools() ([]*mcfgv1.MachineConfigPool, error) {
	// TODO: Once https://github.com/openshift/machine-config-operator/pull/3731
	// lands, change this to consume ctrlcommon.LayeringEnabledPoolLabel instead
	// of having this hard-coded here.
	requirement, err := labels.NewRequirement(ctrlcommon.LayeringEnabledPoolLabel, selection.Exists, []string{})
	if err != nil {
		return []*mcfgv1.MachineConfigPool{}, err
	}

	selector := labels.NewSelector().Add(*requirement)
	pools, err := optr.mcpLister.List(selector)
	if err != nil {
		return []*mcfgv1.MachineConfigPool{}, err
	}

	return pools, nil
}

func (optr *Operator) syncMachineConfigDaemon(config *renderConfig) error {
	paths := manifestPaths{
		clusterRoles: []string{
			mcdClusterRoleManifestPath,
			mcdEventsClusterRoleManifestPath,
		},
		roles: []string{
			mcdKubeRbacProxyPrometheusRolePath,
			mcdRolePath,
		},
		roleBindings: []string{
			mcdEventsRoleBindingDefaultManifestPath,
			mcdEventsRoleBindingTargetManifestPath,
			mcdKubeRbacProxyPrometheusRoleBindingPath,
			mcdRoleBindingPath,
		},
		clusterRoleBindings: []string{
			mcdClusterRoleBindingManifestPath,
		},
		serviceAccounts: []string{
			mcdServiceAccountManifestPath,
		},
		daemonset: mcdDaemonsetManifestPath,
		configMaps: []string{
			mcdKubeRbacProxyConfigMapPath,
		},
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

	ctx := context.TODO()

	// Calculate total timeout for "required"(aka master) nodes in the pool.
	pools, err := optr.mcpLister.List(labels.Everything())
	if err != nil {
		return fmt.Errorf("error during syncRequiredMachineConfigPools: %w", err)
	}

	requiredMachineCount := 0
	for _, pool := range pools {
		_, hasRequiredPoolLabel := pool.Labels[requiredForUpgradeMachineConfigPoolLabelKey]
		if hasRequiredPoolLabel {
			requiredMachineCount += int(pool.Status.MachineCount)
		}
	}

	// Let's start with a 10 minute timeout per "required" node.
	if err := wait.PollUntilContextTimeout(ctx, time.Second, time.Duration(requiredMachineCount*10)*time.Minute, false, func(ctx context.Context) (bool, error) {
		if err := optr.syncMetrics(); err != nil {
			return false, err
		}
		if lastErr != nil {
			co, err := optr.fetchClusterOperator()
			if err != nil {
				errs := kubeErrs.NewAggregate([]error{err, lastErr})
				lastErr = fmt.Errorf("failed to fetch clusteroperator: %w", errs)
				return false, nil
			}
			if co == nil {
				klog.Warning("no clusteroperator for machine-config")
				return false, nil
			}
			optr.setOperatorStatusExtension(&co.Status, lastErr)
			_, err = optr.configClient.ConfigV1().ClusterOperators().UpdateStatus(ctx, co, metav1.UpdateOptions{})
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
				lastErr = fmt.Errorf("error MachineConfigPool %s is not ready, retrying. Status: (pool degraded: %v total: %d, ready %d, updated: %d, unavailable: %d)", pool.Name, degraded, pool.Status.MachineCount, pool.Status.ReadyMachineCount, pool.Status.UpdatedMachineCount, pool.Status.UnavailableMachineCount)
				klog.Errorf("Error syncing Required MachineConfigPools: %q", lastErr)
				syncerr := optr.syncUpgradeableStatus()
				if syncerr != nil {
					klog.Errorf("Error syncingUpgradeableStatus: %q", syncerr)
				}
				return false, nil
			}

			_, hasRequiredPoolLabel := pool.Labels[requiredForUpgradeMachineConfigPoolLabelKey]

			if hasRequiredPoolLabel {
				opURL, _, err := optr.getOsImageURLs(optr.namespace)
				if err != nil {
					klog.Errorf("Error getting configmap osImageURL: %q", err)
					return false, nil
				}
				releaseVersion, _ := optr.vStore.Get("operator")

				// Calling this on a "required" pool for now
				if err := optr.stampBootImagesCM(pool); err != nil {
					klog.Errorf("Failed to stamp bootimages configmap: %w", err)
				}

				if err := isMachineConfigPoolConfigurationValid(pool, version.Hash, releaseVersion, opURL, optr.mcLister.Get); err != nil {
					lastErr = fmt.Errorf("MachineConfigPool %s has not progressed to latest configuration: %w, retrying", pool.Name, err)
					syncerr := optr.syncUpgradeableStatus()
					if syncerr != nil {
						klog.Errorf("Error syncingUpgradeableStatus: %q", syncerr)
					}
					return false, nil
				}

				if pool.Generation <= pool.Status.ObservedGeneration &&
					isPoolStatusConditionTrue(pool, mcfgv1.MachineConfigPoolUpdated) {
					continue
				}
				lastErr = fmt.Errorf("error required MachineConfigPool %s is not ready, retrying. Status: (total: %d, ready %d, updated: %d, unavailable: %d, degraded: %d)", pool.Name, pool.Status.MachineCount, pool.Status.ReadyMachineCount, pool.Status.UpdatedMachineCount, pool.Status.UnavailableMachineCount, pool.Status.DegradedMachineCount)
				syncerr := optr.syncUpgradeableStatus()
				if syncerr != nil {
					klog.Errorf("Error syncingUpgradeableStatus: %q", syncerr)
				}
				// If we don't account for pause here, we will spin in this loop until we hit the 10 minute timeout because paused pools can't sync.
				if pool.Spec.Paused {
					return false, fmt.Errorf("error required MachineConfigPool %s is paused and cannot sync until it is unpaused", pool.Name)
				}
				return false, nil
			}
		}
		return true, nil
	}); err != nil {
		if wait.Interrupted(err) {
			klog.Errorf("Error syncing Required MachineConfigPools: %q", lastErr)
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

	ctx := context.TODO()

	if err := wait.PollUntilContextTimeout(ctx, customResourceReadyInterval, customResourceReadyTimeout, false, func(_ context.Context) (bool, error) {
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
		if wait.Interrupted(err) {
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
	ctx := context.TODO()

	if err := wait.PollUntilContextTimeout(ctx, deploymentRolloutPollInterval, deploymentRolloutTimeout, false, func(_ context.Context) (bool, error) {
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
		if wait.Interrupted(err) {
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
	ctx := context.TODO()

	if err := wait.PollUntilContextTimeout(ctx, daemonsetRolloutPollInterval, daemonsetRolloutTimeout, false, func(_ context.Context) (bool, error) {
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
		if wait.Interrupted(err) {
			errs := kubeErrs.NewAggregate([]error{err, lastErr})
			return fmt.Errorf("error during waitForDaemonsetRollout: %w", errs)
		}
		return err
	}
	return nil
}

func (optr *Operator) waitForControllerConfigToBeCompleted(resource *mcfgv1.ControllerConfig) error {
	var lastErr error
	ctx := context.TODO()

	if err := wait.PollUntilContextTimeout(ctx, controllerConfigCompletedInterval, controllerConfigCompletedTimeout, false, func(_ context.Context) (bool, error) {
		if err := apihelpers.IsControllerConfigCompleted(resource.GetName(), optr.ccLister.Get); err != nil {
			lastErr = fmt.Errorf("controllerconfig is not completed: %w", err)
			return false, nil
		}
		return true, nil
	}); err != nil {
		if wait.Interrupted(err) {
			errs := kubeErrs.NewAggregate([]error{err, lastErr})
			return fmt.Errorf("error during waitForControllerConfigToBeCompleted: %w", errs)
		}
		return err
	}
	return nil
}

// getOsImageURLs returns (new type, new extensions, old type) for operating system update images.
func (optr *Operator) getOsImageURLs(namespace string) (string, string, error) {
	cm, err := optr.mcoCmLister.ConfigMaps(namespace).Get(osImageConfigMapName)
	if err != nil {
		return "", "", err
	}
	releaseVersion := cm.Data["releaseVersion"]
	optrVersion, _ := optr.vStore.Get("operator")
	if releaseVersion != optrVersion {
		return "", "", fmt.Errorf("refusing to read osImageURL version %q, operator version %q", releaseVersion, optrVersion)
	}

	baseOSImage := cm.Data["baseOSContainerImage"]
	extensionsImage := cm.Data["baseOSExtensionsContainerImage"]

	return baseOSImage, extensionsImage, nil
}

func (optr *Operator) getCAsFromConfigMap(namespace, name, key string) ([]byte, error) {
	cm, err := optr.clusterCmLister.ConfigMaps(namespace).Get(name)
	if err != nil {
		return nil, err
	}
	return getCAsFromConfigMap(cm, key)
}

// This function stamps the current operator version and commit hash in the boot images configmap
// that lives in the MCO namespace. Before doing so, it ensures that the pool is targeting an MC
// that is generated by the current version of the MCO and that the pool atleast has 1 upgraded node(or
// has completed an upgrade). This "stamp" is used by the machine set controller as a safety before
// it updates boot images.

func (optr *Operator) stampBootImagesCM(pool *mcfgv1.MachineConfigPool) error {

	// Ensure the targeted MC for this pool was generated by the current MCO
	renderedMC, err := optr.mcLister.Get(pool.Spec.Configuration.Name)
	if err != nil {
		return fmt.Errorf("failed to grab rendered MC %s, error: %w", pool.Spec.Configuration.Name, err)
	}
	if renderedMC.Annotations[ctrlcommon.ReleaseImageVersionAnnotationKey] != version.ReleaseVersion {
		klog.V(4).Infof("rendered MC release version %s mismatch with operator release version %s", renderedMC.Annotations[ctrlcommon.ReleaseImageVersionAnnotationKey], version.ReleaseVersion)
		return nil
	}
	if renderedMC.Annotations[ctrlcommon.GeneratedByControllerVersionAnnotationKey] != version.Hash {
		klog.V(4).Infof("rendered MC commit hash %s mismatch with operator release commit hash %s", renderedMC.Annotations[ctrlcommon.GeneratedByControllerVersionAnnotationKey], version.Hash)
		return nil
	}

	// Check if the pool has atleast one updated node(mid-upgrade), or if the pool has completed the upgrade to the new config(the additional check for spec==status here is
	// to ensure we are not checking an older "Updated" condition and the MCP fields haven't caught up yet
	if (apihelpers.IsMachineConfigPoolConditionTrue(pool.Status.Conditions, mcfgv1.MachineConfigPoolUpdating) && pool.Status.UpdatedMachineCount > 0) ||
		(apihelpers.IsMachineConfigPoolConditionTrue(pool.Status.Conditions, mcfgv1.MachineConfigPoolUpdated) && (pool.Spec.Configuration.Name == pool.Status.Configuration.Name)) {
		cm, err := optr.clusterCmLister.ConfigMaps(ctrlcommon.MCONamespace).Get(ctrlcommon.BootImagesConfigMapName)
		if err != nil {
			return fmt.Errorf("failed to grab boot images configmap: %w", err)
		}
		storedVersionHashFromCM, storedVersionHashFound := cm.Data[ctrlcommon.MCOVersionHashKey]
		releaseVersionFromCM, releaseVersionFound := cm.Data[ctrlcommon.MCOReleaseImageVersionKey]

		if storedVersionHashFound && releaseVersionFound {
			// No need to update if the existing versions are a match, exit
			if storedVersionHashFromCM == version.Hash && releaseVersionFromCM == version.ReleaseVersion {
				return nil
			}
		}

		// Stamp the configmap with newest commit hash and OCP release version
		cm.Data[ctrlcommon.MCOVersionHashKey] = version.Hash
		cm.Data[ctrlcommon.MCOReleaseImageVersionKey] = version.ReleaseVersion

		// Update the ConfigMap
		_, err = optr.kubeClient.CoreV1().ConfigMaps(ctrlcommon.MCONamespace).Update(context.TODO(), cm, metav1.UpdateOptions{})
		if err != nil {
			return fmt.Errorf("failed to update bootimages configmap %w", err)
		}
		klog.Infof("Stamped boot images configmap with %s and %s, machine pool updated count: %d", version.Hash, version.ReleaseVersion, pool.Status.UpdatedMachineCount)

	}
	return nil
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

	// the client removes apiversion/kind (gvk) from all objects during decoding and re-adds it when they are reencoded for
	// transmission, which is normally fine, but the re-add does not recurse into embedded objects, so we have to explicitly
	// re-inject apiversion/kind here so our embedded objects will still validate when embedded in ControllerConfig
	// See: https://issues.redhat.com/browse/OCPBUGS-13860
	infra = infra.DeepCopy()
	err = setGVK(infra, configclientscheme.Scheme)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("Failed setting gvk for infra object: %w", err)
	}
	dns = dns.DeepCopy()
	err = setGVK(dns, configclientscheme.Scheme)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("Failed setting gvk for dns object: %w", err)
	}

	return infra, network, proxy, dns, nil
}

// setGVK sets the group/version/kind of an object based on the supplied client schema. This
// is used by the MCO to re-populate the apiVersion and Kind fields in our embedded objects since
// they get stripped out when the client decodes the object (this stripping behavior is an upstream
// kube decision and not a decision by the MCO)
func setGVK(obj runtime.Object, scheme *runtime.Scheme) error {
	gvks, _, err := scheme.ObjectKinds(obj)
	if err != nil {
		return err
	}
	// For our usage we will only probably ever get back one gvk, but just in case
	for _, gvk := range gvks {
		if gvk.Kind == "" || gvk.Version == "" {
			continue
		}
		obj.GetObjectKind().SetGroupVersionKind(gvk)
		break
	}
	return nil
}

func getRenderConfig(tnamespace, kubeAPIServerServingCA string, ccSpec *mcfgv1.ControllerConfigSpec, imgs *RenderConfigImages, apiServerURL string, pointerConfigData []byte) *renderConfig {
	return &renderConfig{
		TargetNamespace:        tnamespace,
		Version:                version.Raw,
		ReleaseVersion:         version.ReleaseVersion,
		ControllerConfig:       *ccSpec,
		Images:                 imgs,
		KubeAPIServerServingCA: kubeAPIServerServingCA,
		APIServerURL:           apiServerURL,
		PointerConfig:          string(pointerConfigData),
	}
}

func mergeCertWithCABundle(initialBundle, newBundle []byte, subject string) []byte {
	mergedBytes := []byte{}
	for len(initialBundle) > 0 {
		b, next := pem.Decode(initialBundle)
		if b == nil {
			klog.Infof("Unable to decode cert %s into a pem block. Cert is either empty or invalid.", string(initialBundle))
			break
		}
		c, err := x509.ParseCertificate(b.Bytes)
		if err != nil {
			klog.Warningf("Could not parse initial bundle certificate: %v", err)
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

// getImageRegistryPullSecrets fetches the image registry's pull secrets and merges them with the
// global pull secret. It also adds a default route to the registry for the firstboot scenario.

func (optr *Operator) getImageRegistryPullSecrets() ([]byte, error) {

	// Check if image registry exists, if it doesn't we no-op
	co, err := optr.mcoCOLister.Get("image-registry")

	// returning no error in certain cases because image registry may become optional in the future
	// More info at: https://issues.redhat.com/browse/IR-351
	if co == nil || apierrors.IsNotFound(err) {
		klog.Infof("exiting image registry secrets fetch - image registry operator does not exist.")
		return nil, nil
	}
	// for any other error, report it back
	if err != nil {
		return nil, err
	}
	// Image registry exists, so continue to grab the secrets

	// This is for wiring up the default registry route later
	dns, err := optr.dnsLister.Get("cluster")
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve dns object")
	}

	// Create the JSON map
	dockerConfigJSON := ctrlcommon.DockerConfigJSON{
		Auths: map[string]ctrlcommon.DockerConfigEntry{},
	}

	// Get the list of image pull secrets from the designated service account
	imageRegistrySA, err := optr.mcoSALister.ServiceAccounts(optr.namespace).Get("machine-os-puller")
	if err != nil {
		// returning no error here because during an upgrade; the first sync won't have the manifests
		klog.Errorf("exiting image registry secrets fetch - machine-os-puller service account does not exist yet.")
		return nil, nil
	}

	// Step through each image pull secret
	for _, imagePullSecret := range imageRegistrySA.ImagePullSecrets {
		secret, err := optr.mcoSecretLister.Secrets(optr.namespace).Get(imagePullSecret.Name)
		if err != nil {
			return nil, fmt.Errorf("failed to retrieve image pull secret %s: %w", imagePullSecret.Name, err)
		}
		// merge the secret into the JSON map
		ctrlcommon.MergeDockerConfigstoJSONMap(secret.Data[corev1.DockerConfigKey], dockerConfigJSON.Auths)
	}

	// Fetch the cluster pull secret
	clusterPullSecret, err := optr.ocSecretLister.Secrets("openshift-config").Get("pull-secret")
	if err != nil {
		return nil, fmt.Errorf("error fetching cluster pull secret: %w", err)
	}
	if clusterPullSecret.Type != corev1.SecretTypeDockerConfigJson {
		return nil, fmt.Errorf("expected secret type %s found %s", corev1.SecretTypeDockerConfigJson, clusterPullSecret.Type)
	}

	clusterPullSecretRaw := clusterPullSecret.Data[corev1.DockerConfigJsonKey]

	// Add in the cluster pull secret to the JSON map, but convert it to kubernetes.io/dockercfg first
	// as the global pull secret is of type kubernetes.io/dockerconfigjson
	clusterPullSecretRawOld, err := ctrlcommon.ConvertSecretTodockercfg(clusterPullSecretRaw)
	if err != nil {
		return nil, fmt.Errorf("failed to convert global pull secret to old format: %w", err)
	}

	err = ctrlcommon.MergeDockerConfigstoJSONMap(clusterPullSecretRawOld, dockerConfigJSON.Auths)
	if err != nil {
		return nil, fmt.Errorf("failed to merge global pull secret:  %w", err)
	}

	// Add in a default image registry route for first boot cases; this route won't be provided during a pull
	// in live operation. Clone the auth details for the "live" image registry route into this field.
	if entry, ok := dockerConfigJSON.Auths["image-registry.openshift-image-registry.svc:5000"]; ok {
		assembledDefaultRoute := "default-route-openshift-image-registry.apps." + dns.Spec.BaseDomain
		dockerConfigJSON.Auths[assembledDefaultRoute] = entry
	}

	// If "auths" is empty, don't roll config
	if len(dockerConfigJSON.Auths) > 0 {

		mergedPullSecrets, err := json.Marshal(dockerConfigJSON)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal the merged pull secrets: %w", err)
		}

		return mergedPullSecrets, nil
	}

	return nil, nil
}

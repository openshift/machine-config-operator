package operator

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	configclientscheme "github.com/openshift/client-go/config/clientset/versioned/scheme"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
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
	"k8s.io/client-go/util/retry"
	"k8s.io/klog/v2"

	configv1 "github.com/openshift/api/config/v1"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	mcfgv1alpha1 "github.com/openshift/api/machineconfiguration/v1alpha1"
	v1alpha1 "github.com/openshift/api/machineconfiguration/v1alpha1"
	opv1 "github.com/openshift/api/operator/v1"

	features "github.com/openshift/api/features"
	mcoac "github.com/openshift/client-go/operator/applyconfigurations/operator/v1"
	"github.com/openshift/library-go/pkg/operator/resource/resourceapply"
	"github.com/openshift/library-go/pkg/operator/resource/resourceread"
	mcoResourceApply "github.com/openshift/machine-config-operator/lib/resourceapply"
	mcoResourceRead "github.com/openshift/machine-config-operator/lib/resourceread"
	"github.com/openshift/machine-config-operator/pkg/apihelpers"
	"github.com/openshift/machine-config-operator/pkg/controller/build"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	templatectrl "github.com/openshift/machine-config-operator/pkg/controller/template"
	daemonconsts "github.com/openshift/machine-config-operator/pkg/daemon/constants"
	"github.com/openshift/machine-config-operator/pkg/helpers"
	"github.com/openshift/machine-config-operator/pkg/server"
	"github.com/openshift/machine-config-operator/pkg/upgrademonitor"
	"github.com/openshift/machine-config-operator/pkg/version"
	autoscalingv1 "k8s.io/api/autoscaling/v1"
)

const (
	requiredForUpgradeMachineConfigPoolLabelKey = "operator.machineconfiguration.openshift.io/required-for-upgrade"
)

var platformsRequiringCloudConf = sets.NewString(
	string(configv1.AzurePlatformType),
	string(configv1.GCPPlatformType),
	string(configv1.OpenStackPlatformType),
	string(configv1.VSpherePlatformType),
)

type manifestPaths struct {
	clusterRoles                      []string
	roleBindings                      []string
	clusterRoleBindings               []string
	serviceAccounts                   []string
	secrets                           []string
	configMaps                        []string
	roles                             []string
	validatingAdmissionPolicies       []string
	validatingAdmissionPolicyBindings []string
}

const (
	// Machine Config Controller manifest paths
	mccClusterRoleManifestPath                                        = "manifests/machineconfigcontroller/clusterrole.yaml"
	mccEventsClusterRoleManifestPath                                  = "manifests/machineconfigcontroller/events-clusterrole.yaml"
	mccEventsRoleBindingDefaultManifestPath                           = "manifests/machineconfigcontroller/events-rolebinding-default.yaml"
	mccEventsRoleBindingTargetManifestPath                            = "manifests/machineconfigcontroller/events-rolebinding-target.yaml"
	mccClusterRoleBindingManifestPath                                 = "manifests/machineconfigcontroller/clusterrolebinding.yaml"
	mccServiceAccountManifestPath                                     = "manifests/machineconfigcontroller/sa.yaml"
	mccKubeRbacProxyConfigMapPath                                     = "manifests/machineconfigcontroller/kube-rbac-proxy-config.yaml"
	mccKubeRbacProxyPrometheusRolePath                                = "manifests/machineconfigcontroller/prometheus-rbac.yaml"
	mccKubeRbacProxyPrometheusRoleBindingPath                         = "manifests/machineconfigcontroller/prometheus-rolebinding-target.yaml"
	mccMachineConfigurationGuardsValidatingAdmissionPolicyPath        = "manifests/machineconfigcontroller/machineconfiguration-guards-validatingadmissionpolicy.yaml"
	mccMachineConfigurationGuardsValidatingAdmissionPolicyBindingPath = "manifests/machineconfigcontroller/machineconfiguration-guards-validatingadmissionpolicybinding.yaml"
	mccMachineConfigPoolSelectorValidatingAdmissionPolicyPath         = "manifests/machineconfigcontroller/custom-machine-config-pool-selector-validatingadmissionpolicy.yaml"
	mccMachineConfigPoolSelectorValidatingAdmissionPolicyBindingPath  = "manifests/machineconfigcontroller/custom-machine-config-pool-selector-validatingadmissionpolicybinding.yaml"
	mccUpdateBootImagesValidatingAdmissionPolicyPath                  = "manifests/machineconfigcontroller/update-bootimages-validatingadmissionpolicy.yaml"
	mccUpdateBootImagesValidatingAdmissionPolicyBindingPath           = "manifests/machineconfigcontroller/update-bootimages-validatingadmissionpolicybinding.yaml"

	// Machine OS Builder manifest paths
	mobClusterRoleManifestPath                      = "manifests/machineosbuilder/clusterrole.yaml"
	mobEventsClusterRoleManifestPath                = "manifests/machineosbuilder/events-clusterrole.yaml"
	mobEventsRoleBindingDefaultManifestPath         = "manifests/machineosbuilder/events-rolebinding-default.yaml"
	mobEventsRoleBindingTargetManifestPath          = "manifests/machineosbuilder/events-rolebinding-target.yaml"
	mobClusterRoleBindingServiceAccountManifestPath = "manifests/machineosbuilder/clusterrolebinding-service-account.yaml"
	mobClusterRolebindingAnyUIDManifestPath         = "manifests/machineosbuilder/clusterrolebinding-anyuid.yaml"
	mobClusterRolebindingPipelinesSCCManifestPath   = "manifests/machineosbuilder/clusterrolebinding-pipelines-scc.yaml"
	mobServiceAccountManifestPath                   = "manifests/machineosbuilder/sa.yaml"

	// Machine Config Daemon manifest paths
	mcdClusterRoleManifestPath                      = "manifests/machineconfigdaemon/clusterrole.yaml"
	mcdEventsClusterRoleManifestPath                = "manifests/machineconfigdaemon/events-clusterrole.yaml"
	mcdEventsRoleBindingDefaultManifestPath         = "manifests/machineconfigdaemon/events-rolebinding-default.yaml"
	mcdEventsRoleBindingTargetManifestPath          = "manifests/machineconfigdaemon/events-rolebinding-target.yaml"
	mcdClusterRoleBindingManifestPath               = "manifests/machineconfigdaemon/clusterrolebinding.yaml"
	mcdServiceAccountManifestPath                   = "manifests/machineconfigdaemon/sa.yaml"
	mcdDaemonsetManifestPath                        = "manifests/machineconfigdaemon/daemonset.yaml"
	mcdKubeRbacProxyConfigMapPath                   = "manifests/machineconfigdaemon/kube-rbac-proxy-config.yaml"
	mcdKubeRbacProxyPrometheusRolePath              = "manifests/machineconfigdaemon/prometheus-rbac.yaml"
	mcdKubeRbacProxyPrometheusRoleBindingPath       = "manifests/machineconfigdaemon/prometheus-rolebinding-target.yaml"
	mcdRolePath                                     = "manifests/machineconfigdaemon/role.yaml"
	mcdRoleBindingPath                              = "manifests/machineconfigdaemon/rolebinding.yaml"
	mcdMCNGuardValidatingAdmissionPolicyPath        = "manifests/machineconfigdaemon/mcn-guards-validatingadmissionpolicy.yaml"
	mcdMCNGuardValidatingAdmissionPolicyBindingPath = "manifests/machineconfigdaemon/mcn-guards-validatingadmissionpolicybinding.yaml"

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
	fn   func(config *renderConfig, co *configv1.ClusterOperator) error
}

type syncError struct {
	task string
	err  error
}

func (optr *Operator) syncAll(syncFuncs []syncFunc) error {

	co, err := optr.fetchClusterOperator()
	if err != nil {
		return err
	}

	// Deepcopy the cluster operator object. Pass it through each of the sync functions below. Some of the sync functions
	// will modify the object status as needed. This will be then used to perform a "batch" update to the object at the end of this
	// function. This helps minimize API calls.
	//
	// Note: syncRequiredMachineConfigPools(one of the syncFuncs) will update the object prior to the batch update
	// For syncRequiredMachineConfigPools, this is because the operator may be rolling out a new
	// MachineConfig and may get "stuck" in the polling block within that function until all required pools are done updating.
	// By updating within syncRequiredMachineConfigPools, the operator is able to provide faster updates during the progression
	// of the pool update.

	updatedCO := co.DeepCopy()

	optr.syncProgressingStatus(updatedCO)

	var syncErr syncError

	for _, sf := range syncFuncs {
		startTime := time.Now()
		syncErr = syncError{
			task: sf.name,
			err:  sf.fn(optr.renderConfig, updatedCO),
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
	}

	optr.syncDegradedStatus(updatedCO, syncErr)

	optr.syncAvailableStatus(updatedCO)

	optr.syncVersion(updatedCO)

	optr.syncRelatedObjects(updatedCO)

	syncUpgradeableStatusErr := optr.syncUpgradeableStatus(updatedCO)

	syncClusterFleetEvaluationErr := optr.syncClusterFleetEvaluation(updatedCO)

	// Batch update the Cluster Operator Status. This update will cause a resource conflict if
	// syncRequiredMachineConfigPools has done an update prior to this. In such a case,
	// updateClusterOperatorStatus will refetch the Cluster Operator object before updating the new status.
	if _, err := optr.updateClusterOperatorStatus(co, &updatedCO.Status, nil); err != nil {
		return fmt.Errorf("error updating cluster operator status: %w", err)
	}

	// Handle these errors after as CO status updates should have priority over this
	if syncUpgradeableStatusErr != nil {
		return fmt.Errorf("error syncingUpgradeableStatus: %w", syncUpgradeableStatusErr)

	}
	if syncClusterFleetEvaluationErr != nil {
		return fmt.Errorf("error updating cluster operator status: %w", syncClusterFleetEvaluationErr)
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
func (optr *Operator) syncRenderConfig(_ *renderConfig, _ *configv1.ClusterOperator) error {
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

	imgs, err := ctrlcommon.ParseImagesFromBytes(imgsRaw)
	if err != nil {
		return err
	}

	// Do a short retry loop here as there can be a rare race between the machine-config-operator-images configmap and
	// the operator deployment when they are updated. An immediate degrade would be undesirable in such cases.
	if err := wait.PollUntilContextTimeout(context.TODO(), 1*time.Second, 3*time.Second, true, func(_ context.Context) (bool, error) {
		optrVersion, exists := optr.vStore.Get("operator")
		if !exists {
			return false, fmt.Errorf("operator version not found")
		}
		if imgs.ReleaseVersion != optrVersion {
			return false, fmt.Errorf("refusing to read images.json version %q, operator version %q", imgs.ReleaseVersion, optrVersion)
		}
		return true, nil
	}); err != nil {
		return fmt.Errorf("timed out: %v", err)
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

	mergedData := append([]mcfgv1.ImageRegistryBundle{}, append(imgRegistryData, imgRegistryUsrData...)...)
	caData := make(map[string]string, len(mergedData))
	for _, CA := range mergedData {
		caData[CA.File] = string(CA.Data)
	}

	cm, err = optr.clusterCmLister.ConfigMaps("openshift-config-managed").Get("merged-trusted-image-registry-ca")
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	cmAnnotations := make(map[string]string)
	cmAnnotations["openshift.io/description"] = "Created and managed by the machine-config-operator"
	if err != nil && apierrors.IsNotFound(err) {
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
	infra, network, proxy, dns, apiServer, err := optr.getGlobalConfig()
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
		templatectrl.KubeRbacProxyKey:         imgs.KubeRbacProxy,
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

	isOnClusterBuildEnabled, err := optr.isOnClusterBuildFeatureGateEnabled()
	if err != nil {
		return err
	}

	if isOnClusterBuildEnabled {
		moscs, err := optr.getAndValidateMachineOSConfigs()
		if err != nil {
			return err
		}

		// create renderConfig
		optr.renderConfig = getRenderConfig(optr.namespace, string(kubeAPIServerServingCABytes), spec, &imgs.RenderConfigImages, infra.Status.APIServerInternalURL, pointerConfigData, moscs, apiServer)
	} else {
		optr.renderConfig = getRenderConfig(optr.namespace, string(kubeAPIServerServingCABytes), spec, &imgs.RenderConfigImages, infra.Status.APIServerInternalURL, pointerConfigData, nil, apiServer)
	}

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

func (optr *Operator) syncMachineConfigPools(config *renderConfig, _ *configv1.ClusterOperator) error {
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
func (optr *Operator) syncMachineConfigNodes(_ *renderConfig, _ *configv1.ClusterOperator) error {
	fg, err := optr.fgAccessor.CurrentFeatureGates()
	if err != nil {
		klog.Errorf("Could not get fg: %v", err)
		return err
	}
	if !fg.Enabled(features.FeatureGateMachineConfigNodes) {
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
				OwnerReferences: []metav1.OwnerReference{
					{
						APIVersion: "v1",
						Name:       node.ObjectMeta.Name,
						Kind:       "Node",
						UID:        node.ObjectMeta.UID,
					},
				},
			},
		}
		mcsBytes, err := json.Marshal(newMCS)
		if err != nil {
			klog.Errorf("error rendering asset for MachineConfigNode %v", err)
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
				klog.Errorf("Error making MCN spec for Update Compatible: %v", err)
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

//nolint:gocyclo
func (optr *Operator) applyManifests(config *renderConfig, paths manifestPaths) error {
	// Retry in case this is a short lived, transient issue.
	// This does an exponential retry, up to 5 times before it eventually times out and causing a degrade.
	return retry.OnError(retry.DefaultRetry, mcoResourceApply.IsApplyErrorRetriable, func() error {
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
		fg, err := optr.fgAccessor.CurrentFeatureGates()
		if err != nil {
			return fmt.Errorf("could not get feature gates: %w", err)
		}

		if fg == nil {
			return fmt.Errorf("received nil feature gates")
		}

		// These new apply functions have a resource cache in case there are duplicate CRs
		noCache := resourceapply.NewResourceCache()
		for _, path := range paths.validatingAdmissionPolicies {
			vapBytes, err := renderAsset(config, path)
			if err != nil {
				return err
			}
			vap := resourceread.ReadValidatingAdmissionPolicyV1OrDie(vapBytes)
			_, _, err = resourceapply.ApplyValidatingAdmissionPolicyV1(context.TODO(), optr.kubeClient.AdmissionregistrationV1(), optr.libgoRecorder, vap, noCache)
			if err != nil {
				return err
			}
		}

		for _, path := range paths.validatingAdmissionPolicyBindings {
			vapbBytes, err := renderAsset(config, path)
			if err != nil {
				return err
			}
			vapb := resourceread.ReadValidatingAdmissionPolicyBindingV1OrDie(vapbBytes)
			_, _, err = resourceapply.ApplyValidatingAdmissionPolicyBindingV1(context.TODO(), optr.kubeClient.AdmissionregistrationV1(), optr.libgoRecorder, vapb, noCache)
			if err != nil {
				return err
			}
		}
		return nil
	})
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
//
//nolint:gocritic
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

	editCCAnno := false
	kubeConfigData, err := optr.clusterCmLister.ConfigMaps("openshift-config-managed").Get("kube-apiserver-server-ca")
	if err != nil {
		klog.Errorf("Could not get in-cluster server-ca data %v", err)
	} else {
		data, err := cmToData(kubeConfigData, "ca-bundle.crt")
		if err != nil {
			klog.Errorf("kube-apiserver-server-ca ConfigMap not populated yet. %v", err)
		} else if data != nil {
			cmNew := corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name: "kubeconfig-data",
				},
			}
			mcoCM, getErr := optr.kubeClient.CoreV1().ConfigMaps(ctrlcommon.MCONamespace).Get(context.TODO(), "kubeconfig-data", metav1.GetOptions{})
			if getErr != nil {
				klog.Errorf("Issue getting the kubeconfig-data configmap: %v", getErr)
				if !apierrors.IsNotFound(getErr) && !apierrors.IsTimeout(getErr) && !apierrors.IsServerTimeout(getErr) {
					return getErr
				}
			}
			dataOld, err := cmToData(mcoCM, "ca-bundle.crt")
			if err != nil && getErr == nil {
				klog.Errorf("Could not get ca-bundle.crt from kubeconfig-data cm %v", err)
			} else if !bytes.Equal(data, dataOld) && getErr == nil {
				// -1 == mcoCM < kubeCM
				// 1 == mcoCM > kubeCM
				klog.Infof("the MCO configmap for the server-ca and the openshift-config-managed one do not match. Diff: (0 means equal -1 means data has been removed 1 means data has been added) %d.", bytes.Compare(dataOld, data))
				// old mco CM data
				newData := true
				for newData {
					klog.Infof("Polling for new data in kube-apiserver-server-ca")
					if err := wait.PollUntilContextTimeout(context.TODO(), 15*time.Second, 1*time.Minute, false, func(_ context.Context) (bool, error) {
						newData = false
						// pull off of API not a lister
						kubeConfigData, err := optr.kubeClient.CoreV1().ConfigMaps("openshift-config-managed").Get(context.TODO(), "kube-apiserver-server-ca", metav1.GetOptions{})
						if err != nil {
							klog.Errorf("Could not get in-cluster server-ca data %v", err)
						} else {
							newBytes, err := cmToData(kubeConfigData, "ca-bundle.crt")
							if err != nil {
								return false, err
							}
							if !bytes.Equal(newBytes, data) {
								klog.Infof("Found new data while polling. Waiting an extra minute to see if there are any more changes.")
								newData = true
								data = newBytes
								return newData, nil
							}
						}
						return false, nil
					}); err != nil {
						if !wait.Interrupted(err) {
							klog.Errorf("Error waiting for kube-apiserver-server-ca to settle: %v", err)
						}
					}
				}
				binData := make(map[string][]byte)
				binData["ca-bundle.crt"] = data
				cmNew.BinaryData = binData
				cmMarshal, err := json.Marshal(mcoCM)
				if err != nil {
					return fmt.Errorf("could not marshal old configmap data. Err: %v ", err)
				}
				// new mco CM Data
				newCMMarshal, err := json.Marshal(cmNew)
				if err != nil {
					return fmt.Errorf("could not marshal new configmap data. Data: %s Err: %v ", string(data), err)
				}
				// patch mco CM
				patchBytes, err := jsonmergepatch.CreateThreeWayJSONMergePatch(cmMarshal, newCMMarshal, cmMarshal)
				if err != nil {
					return fmt.Errorf("Could not create a three way json merge patch: %w", err)
				}
				_, err = optr.kubeClient.CoreV1().ConfigMaps(ctrlcommon.MCONamespace).Patch(context.TODO(), "kubeconfig-data", types.MergePatchType, patchBytes, metav1.PatchOptions{})
				if err != nil {
					return fmt.Errorf("Could not patch kubeconfig-data with data %s: %w", string(patchBytes), err)
				}
				editCCAnno = true
			} else if getErr != nil {
				binData := make(map[string][]byte)
				binData["ca-bundle.crt"] = data
				cmNew.BinaryData = binData
				_, err := optr.kubeClient.CoreV1().ConfigMaps(ctrlcommon.MCONamespace).Create(context.TODO(), &cmNew, metav1.CreateOptions{})
				if err != nil {
					return fmt.Errorf("Could not make kubeconfig-data CM, %v", err)
				}
				editCCAnno = true
			}

		}
	}
	cc.Annotations[daemonconsts.GeneratedByVersionAnnotationKey] = version.Raw
	if editCCAnno {
		cc.Annotations[ctrlcommon.ServiceCARotateAnnotation] = ctrlcommon.ServiceCARotateTrue
	} else {
		cc.Annotations[ctrlcommon.ServiceCARotateAnnotation] = ctrlcommon.ServiceCARotateFalse
	}
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

func (optr *Operator) syncMachineConfigController(config *renderConfig, _ *configv1.ClusterOperator) error {
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
		validatingAdmissionPolicies: []string{
			mccMachineConfigurationGuardsValidatingAdmissionPolicyPath,
			mccUpdateBootImagesValidatingAdmissionPolicyPath,
			mccMachineConfigPoolSelectorValidatingAdmissionPolicyPath,
		},
		validatingAdmissionPolicyBindings: []string{
			mccMachineConfigurationGuardsValidatingAdmissionPolicyBindingPath,
			mccUpdateBootImagesValidatingAdmissionPolicyBindingPath,
			mccMachineConfigPoolSelectorValidatingAdmissionPolicyBindingPath,
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

	var updated bool
	if retryErr := retry.OnError(retry.DefaultRetry, mcoResourceApply.IsApplyErrorRetriable, func() error {
		_, updated, err = mcoResourceApply.ApplyDeployment(optr.kubeClient.AppsV1(), mcc)
		return err
	}); retryErr != nil {
		return retryErr
	}
	if updated {
		if err := optr.waitForDeploymentRollout(mcc); err != nil {
			return err
		}
	}
	return optr.syncControllerConfig(config)
}

// syncs machine os builder
func (optr *Operator) syncMachineOSBuilder(config *renderConfig, _ *configv1.ClusterOperator) error {
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
			mobClusterRolebindingPipelinesSCCManifestPath,
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
	// Access current feature gates
	fg, err := optr.fgAccessor.CurrentFeatureGates()
	if err != nil {
		return fmt.Errorf("could not get feature gates: %w", err)
	}

	if fg == nil {
		return fmt.Errorf("received nil feature gates")
	}

	// Check if OnClusterBuild feature gate is enabled
	if !fg.Enabled(features.FeatureGateOnClusterBuild) {
		return nil
	}

	// First, check if we have any MachineConfigPools opted in.
	layeredMCPs, err := optr.getLayeredMachineConfigPools()
	if err != nil {
		return fmt.Errorf("could not get layered MachineConfigPools: %w", err)
	}

	// If layeredMCP is found, verify that the node is coreos based as non coreos OS images
	// cannot handle layered builds
	if len(layeredMCPs) > 0 {
		if err := optr.validateLayeredPoolNodes(layeredMCPs); err != nil {
			return err
		}
	}

	isRunning, err := optr.isMachineOSBuilderRunning(mob)
	// An unknown error occurred. Bail out here.
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("could not determine if Machine OS Builder is running: %w", err)
	}

	// Reconcile etc-pki-entitlement secrets in the MCO namespace, if required.
	if err := optr.reconcileSimpleContentAccessSecrets(layeredMCPs); err != nil {
		return fmt.Errorf("could not reconcile etc-pki-entitlement secrets: %w", err)
	}

	// If we have opted-in pools and the Machine OS Builder deployment is either
	// not running or doesn't have the correct replica count, scale it up.
	correctReplicaCount := optr.hasCorrectReplicaCount(mob)
	if len(layeredMCPs) != 0 && (!isRunning || !correctReplicaCount) {
		if !correctReplicaCount {
			klog.Infof("Adjusting Machine OS Builder pod replica count because MachineConfigPool(s) opted into layering")
			return optr.updateMachineOSBuilderDeployment(mob, 1, layeredMCPs)
		}
		klog.Infof("Starting Machine OS Builder pod because MachineConfigPool(s) opted into layering")
		return optr.startMachineOSBuilderDeployment(mob, layeredMCPs)
	}

	// If we do not have opted-in pools and the Machine OS Builder deployment is
	// running, scale it down.
	if len(layeredMCPs) == 0 && isRunning {
		klog.Infof("Shutting down Machine OS Builder pod because no MachineConfigPool(s) opted into layering")
		return optr.stopMachineOSBuilderDeployment(mob.Name)
	}

	// if we are in ocb, but for some reason we dont need to do an update to the deployment, we still need to validate config
	if len(layeredMCPs) != 0 {
		return build.ValidateOnClusterBuildConfig(optr.kubeClient, optr.client, layeredMCPs)
	}
	return nil
}

// Validate that the nodes part of layered pools are coreos based
func (optr *Operator) validateLayeredPoolNodes(layeredMCPs []*mcfgv1.MachineConfigPool) error {
	nodes, err := optr.GetAllManagedNodes(layeredMCPs)
	if err != nil {
		return fmt.Errorf("could not get nodes in layered MachineConfigPools: %w", err)
	}
	// Error out if any of the nodes in the pool where OCL is enabled is a non-coreos node
	errNodes := []error{}
	for _, node := range nodes {
		if !helpers.IsCoreOSNode(node) {
			poolNames, err := helpers.GetPoolNamesForNode(optr.mcpLister, node)
			if err != nil {
				return err
			}
			err = fmt.Errorf("non-CoreOS OS %q detected on node %q in MachineConfigPool(s) %q", node.Status.NodeInfo.OSImage, node.Name, poolNames)
			errNodes = append(errNodes, err)
		}
	}

	if len(errNodes) > 0 {
		return fmt.Errorf("on-cluster layering is only supported on CoreOS-based nodes: %w", kubeErrs.NewAggregate(errNodes))
	}
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

func (optr *Operator) updateMachineOSBuilderDeployment(mob *appsv1.Deployment, replicas int32, layeredMCPs []*mcfgv1.MachineConfigPool) error {
	if err := build.ValidateOnClusterBuildConfig(optr.kubeClient, optr.client, layeredMCPs); err != nil {
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

func (optr *Operator) reconcileSimpleContentAccessSecrets(layeredMCPs []*mcfgv1.MachineConfigPool) error {

	// Create set of layered and non layeredPools
	layeredPoolSet := sets.Set[string]{}
	for _, pool := range layeredMCPs {
		layeredPoolSet.Insert(pool.Name)
	}

	allMCP, err := optr.mcpLister.List(labels.Everything())
	if err != nil {
		return err
	}

	allPoolSet := sets.Set[string]{}
	for _, pool := range allMCP {
		allPoolSet.Insert(pool.Name)
	}

	nonLayeredPoolSet := allPoolSet.Difference(layeredPoolSet)

	// For all non-layered pools, if the secret exists, delete it.
	for _, poolName := range nonLayeredPoolSet.UnsortedList() {
		secretName := ctrlcommon.SimpleContentAccessSecretName + "-" + poolName
		_, err := optr.mcoSecretLister.Secrets(ctrlcommon.MCONamespace).Get(secretName)
		if apierrors.IsNotFound(err) {
			continue
		} else if err != nil {
			return err
		}
		if deleteErr := optr.kubeClient.CoreV1().Secrets(ctrlcommon.MCONamespace).Delete(context.TODO(), secretName, metav1.DeleteOptions{}); deleteErr != nil {
			return deleteErr
		}
		klog.Infof("deleting %s", secretName)
	}

	// Now check if the Simple Content Access Secret "etc-pki-entitlement" secret exists
	simpleContentAccessSecret, err := optr.ocManagedSecretLister.Secrets(ctrlcommon.OpenshiftConfigManagedNamespace).Get(ctrlcommon.SimpleContentAccessSecretName)
	if apierrors.IsNotFound(err) {
		// exit early, this cluster does not have RHEL entitlements
		klog.V(4).Infof("Exiting early due to lack of RHEL entitlements")
		return nil
	} else if err != nil {
		return err
	}

	// For all layered pools, create/update the secret
	for _, poolName := range layeredPoolSet.UnsortedList() {
		secretName := ctrlcommon.SimpleContentAccessSecretName + "-" + poolName

		// Create a clone of simpleContentAccessSecret, modify it to be in the MCO namespace with the new name
		clonedSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      secretName,
				Namespace: ctrlcommon.MCONamespace,
			},
			Data: simpleContentAccessSecret.Data,
			Type: corev1.SecretTypeOpaque,
		}

		currentSecret, err := optr.mcoSecretLister.Secrets(ctrlcommon.MCONamespace).Get(secretName)
		switch {
		case apierrors.IsNotFound(err):
			// The secret doesn't exist, so create one for this pool
			if _, createErr := optr.kubeClient.CoreV1().Secrets(ctrlcommon.MCONamespace).Create(context.TODO(), clonedSecret, metav1.CreateOptions{}); createErr != nil {
				return createErr
			}
			klog.Infof("creating %s", secretName)
		case err != nil:
			return err
		case !reflect.DeepEqual(currentSecret.Data, clonedSecret.Data):
			// An update to the secret is required
			if _, updateErr := optr.kubeClient.CoreV1().Secrets(ctrlcommon.MCONamespace).Update(context.TODO(), clonedSecret, metav1.UpdateOptions{}); updateErr != nil {
				return updateErr
			}
			klog.Infof("updating %s", secretName)
		}
	}

	return nil
}

// Updates the Machine OS Builder Deployment, creating it if it does not exist.
func (optr *Operator) startMachineOSBuilderDeployment(mob *appsv1.Deployment, layeredMCPs []*mcfgv1.MachineConfigPool) error {
	if err := build.ValidateOnClusterBuildConfig(optr.kubeClient, optr.client, layeredMCPs); err != nil {
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

	if len(pools) == 0 {
		moscPools := []*mcfgv1.MachineConfigPool{}
		machineosconfigs, err := optr.client.MachineconfigurationV1alpha1().MachineOSConfigs().List(context.TODO(), metav1.ListOptions{})
		if err != nil {
			return []*mcfgv1.MachineConfigPool{}, err
		}
		for _, mosc := range machineosconfigs.Items {
			mcp, err := optr.mcpLister.Get(mosc.Spec.MachineConfigPool.Name)
			if err != nil {
				return []*mcfgv1.MachineConfigPool{}, err
			}
			moscPools = append(moscPools, mcp)
		}
		return moscPools, nil
	}

	return pools, nil
}

func (optr *Operator) syncMachineConfigDaemon(config *renderConfig, _ *configv1.ClusterOperator) error {
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
		configMaps: []string{
			mcdKubeRbacProxyConfigMapPath,
		},
		validatingAdmissionPolicies: []string{
			mcdMCNGuardValidatingAdmissionPolicyPath,
		},
		validatingAdmissionPolicyBindings: []string{
			mcdMCNGuardValidatingAdmissionPolicyBindingPath,
		},
	}

	if err := optr.applyManifests(config, paths); err != nil {
		return fmt.Errorf("failed to apply machine config daemon manifests: %w", err)
	}

	dBytes, err := renderAsset(config, mcdDaemonsetManifestPath)
	if err != nil {
		return err
	}
	d := resourceread.ReadDaemonSetV1OrDie(dBytes)

	var updated bool
	if retryErr := retry.OnError(retry.DefaultRetry, mcoResourceApply.IsApplyErrorRetriable, func() error {
		_, updated, err = mcoResourceApply.ApplyDaemonSet(optr.kubeClient.AppsV1(), d)
		return err
	}); retryErr != nil {
		return retryErr
	}
	if updated {
		return optr.waitForDaemonsetRollout(d)
	}

	return nil
}

func (optr *Operator) syncMachineConfigServer(config *renderConfig, _ *configv1.ClusterOperator) error {
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
	}

	if err := optr.applyManifests(config, paths); err != nil {
		return fmt.Errorf("failed to apply machine config server manifests: %w", err)
	}

	dBytes, err := renderAsset(config, mcsDaemonsetManifestPath)
	if err != nil {
		return err
	}
	d := resourceread.ReadDaemonSetV1OrDie(dBytes)

	var updated bool
	if retryErr := retry.OnError(retry.DefaultRetry, mcoResourceApply.IsApplyErrorRetriable, func() error {
		_, updated, err = mcoResourceApply.ApplyDaemonSet(optr.kubeClient.AppsV1(), d)
		return err
	}); retryErr != nil {
		return retryErr
	}
	if updated {
		return optr.waitForDaemonsetRollout(d)
	}

	return nil
}

// syncRequiredMachineConfigPools ensures that all the nodes in machineconfigpools labeled with requiredForUpgradeMachineConfigPoolLabelKey
// have updated to the latest configuration.
func (optr *Operator) syncRequiredMachineConfigPools(config *renderConfig, co *configv1.ClusterOperator) error {
	var lastErr error

	fg, err := optr.fgAccessor.CurrentFeatureGates()
	if err != nil {
		return fmt.Errorf("could not get feature gates: %w", err)
	}

	if fg == nil {
		return fmt.Errorf("received nil feature gates")
	}

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
	if err := wait.PollUntilContextTimeout(ctx, time.Second, time.Duration(requiredMachineCount*10)*time.Minute, false, func(_ context.Context) (bool, error) {
		if err := optr.syncMetrics(); err != nil {
			return false, err
		}

		if lastErr != nil {
			// In this case, only the status extension field is updated.
			newCOStatus := co.Status.DeepCopy()
			co, err = optr.updateClusterOperatorStatus(co, newCOStatus, lastErr)
			if err != nil {
				errs := kubeErrs.NewAggregate([]error{err, lastErr})
				klog.Errorf("Error updating cluster operator status: %q", err)
				lastErr = fmt.Errorf("failed to update clusteroperator: %w", errs)
				return false, nil
			}
		}

		// This was needed in-case the cluster is doing a master pool update when a new MachineConfiguration was applied.
		// This prevents the need to wait for all the master nodes (as the syncAll function of the operator will be
		// "stuck" here in such a case) to update before the MachineConfiguration status is updated.
		if syncErr := optr.syncMachineConfiguration(config, co); syncErr != nil {
			// Update the degrade condition if there was an error reported by syncMachineConfiguration
			newCO := co.DeepCopy()
			optr.syncDegradedStatus(newCO, syncError{task: "MachineConfiguration", err: syncErr})
			co, syncErr = optr.updateClusterOperatorStatus(co, &newCO.Status, lastErr)
			if syncErr != nil {
				klog.Errorf("Error updating cluster operator status: %q", syncErr)
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
				newCO := co.DeepCopy()
				syncerr := optr.syncUpgradeableStatus(newCO)
				if syncerr != nil {
					klog.Errorf("Error syncingUpgradeableStatus: %q", syncerr)
				}
				co, syncerr = optr.updateClusterOperatorStatus(co, &newCO.Status, lastErr)
				if syncerr != nil {
					klog.Errorf("Error updating cluster operator status: %q", syncerr)
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
					klog.Errorf("Failed to stamp bootimages configmap: %v", err)
				}

				if err := isMachineConfigPoolConfigurationValid(fg, pool, version.Hash, releaseVersion, opURL, optr.mcLister.Get); err != nil {
					lastErr = fmt.Errorf("MachineConfigPool %s has not progressed to latest configuration: %w, retrying", pool.Name, err)
					newCO := co.DeepCopy()
					syncerr := optr.syncUpgradeableStatus(newCO)
					if syncerr != nil {
						klog.Errorf("Error syncingUpgradeableStatus: %q", syncerr)
					}
					co, syncerr = optr.updateClusterOperatorStatus(co, &newCO.Status, lastErr)
					if syncerr != nil {
						klog.Errorf("Error updating cluster operator status: %q", syncerr)
					}
					return false, nil
				}

				if pool.Generation <= pool.Status.ObservedGeneration &&
					isPoolStatusConditionTrue(pool, mcfgv1.MachineConfigPoolUpdated) {
					continue
				}
				lastErr = fmt.Errorf("error required MachineConfigPool %s is not ready, retrying. Status: (total: %d, ready %d, updated: %d, unavailable: %d, degraded: %d)", pool.Name, pool.Status.MachineCount, pool.Status.ReadyMachineCount, pool.Status.UpdatedMachineCount, pool.Status.UnavailableMachineCount, pool.Status.DegradedMachineCount)
				newCO := co.DeepCopy()
				syncerr := optr.syncUpgradeableStatus(newCO)
				if syncerr != nil {
					klog.Errorf("Error syncingUpgradeableStatus: %q", syncerr)
				}
				co, syncerr = optr.updateClusterOperatorStatus(co, &newCO.Status, lastErr)
				if syncerr != nil {
					klog.Errorf("Error updating cluster operator status: %q", syncerr)
				}
				// If we don't account for pause here, we will spin in this loop until we hit the 10 minute timeout because paused pools can't sync.
				if pool.Spec.Paused {
					if isPoolStatusConditionTrue(pool, mcfgv1.MachineConfigPoolUpdated) {
						return false, fmt.Errorf("the required MachineConfigPool %s was paused with no pending updates; no further syncing will occur until it is unpaused", pool.Name)
					}
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
	cm, err := optr.mcoCmLister.ConfigMaps(namespace).Get(ctrlcommon.MachineConfigOSImageURLConfigMapName)
	if err != nil {
		return "", "", err
	}

	cfg, err := ctrlcommon.ParseOSImageURLConfigMap(cm)
	if err != nil {
		return "", "", err
	}

	optrVersion, _ := optr.vStore.Get("operator")
	if cfg.ReleaseVersion != optrVersion {
		return "", "", fmt.Errorf("refusing to read osImageURL version %q, operator version %q", cfg.ReleaseVersion, optrVersion)
	}

	return cfg.BaseOSContainerImage, cfg.BaseOSExtensionsContainerImage, nil
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
	}
	if d, dok := cm.Data[key]; dok {
		raw, err := base64.StdEncoding.DecodeString(d)
		if err != nil {
			return []byte(d), nil
		}
		return raw, nil
	}
	return nil, fmt.Errorf("%s not found in %s/%s", key, cm.Namespace, cm.Name)
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

// getGlobalConfig gets global configuration for the cluster, namely, the Infrastructure, Network and the APIServer types.
// Each type of global configuration is named `cluster` for easy discovery in the cluster.
func (optr *Operator) getGlobalConfig() (*configv1.Infrastructure, *configv1.Network, *configv1.Proxy, *configv1.DNS, *configv1.APIServer, error) {
	infra, err := optr.infraLister.Get("cluster")
	if err != nil {
		return nil, nil, nil, nil, nil, err
	}
	network, err := optr.networkLister.Get("cluster")
	if err != nil {
		return nil, nil, nil, nil, nil, err
	}
	proxy, err := optr.proxyLister.Get("cluster")
	if err != nil && !apierrors.IsNotFound(err) {
		return nil, nil, nil, nil, nil, err
	}
	dns, err := optr.dnsLister.Get("cluster")
	if err != nil && !apierrors.IsNotFound(err) {
		return nil, nil, nil, nil, nil, err
	}
	apiServer, err := optr.apiserverLister.Get(ctrlcommon.APIServerInstanceName)
	if err != nil && !apierrors.IsNotFound(err) {
		return nil, nil, nil, nil, nil, err
	}

	// the client removes apiversion/kind (gvk) from all objects during decoding and re-adds it when they are reencoded for
	// transmission, which is normally fine, but the re-add does not recurse into embedded objects, so we have to explicitly
	// re-inject apiversion/kind here so our embedded objects will still validate when embedded in ControllerConfig
	// See: https://issues.redhat.com/browse/OCPBUGS-13860
	infra = infra.DeepCopy()
	err = setGVK(infra, configclientscheme.Scheme)
	if err != nil {
		return nil, nil, nil, nil, nil, fmt.Errorf("Failed setting gvk for infra object: %w", err)
	}
	dns = dns.DeepCopy()
	err = setGVK(dns, configclientscheme.Scheme)
	if err != nil {
		return nil, nil, nil, nil, nil, fmt.Errorf("Failed setting gvk for dns object: %w", err)
	}

	return infra, network, proxy, dns, apiServer, nil
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

func getRenderConfig(tnamespace, kubeAPIServerServingCA string, ccSpec *mcfgv1.ControllerConfigSpec, imgs *ctrlcommon.RenderConfigImages, apiServerURL string, pointerConfigData []byte, moscs []*mcfgv1alpha1.MachineOSConfig, apiServer *configv1.APIServer) *renderConfig {
	tlsMinVersion, tlsCipherSuites := ctrlcommon.GetSecurityProfileCiphersFromAPIServer(apiServer)
	return &renderConfig{
		TargetNamespace:        tnamespace,
		Version:                version.Raw,
		ReleaseVersion:         version.ReleaseVersion,
		ControllerConfig:       *ccSpec,
		Images:                 imgs,
		KubeAPIServerServingCA: kubeAPIServerServingCA,
		APIServerURL:           apiServerURL,
		PointerConfig:          string(pointerConfigData),
		MachineOSConfigs:       moscs,
		TLSMinVersion:          tlsMinVersion,
		TLSCipherSuites:        tlsCipherSuites,
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
	co, err := optr.clusterOperatorLister.Get("image-registry")

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
		if err := ctrlcommon.MergeDockerConfigstoJSONMap(secret.Data[corev1.DockerConfigKey], dockerConfigJSON.Auths); err != nil {
			return nil, fmt.Errorf("could not merge auths from secret %s: %w", imagePullSecret.Name, err)
		}
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

//nolint:unparam
func cmToData(cm *corev1.ConfigMap, key string) ([]byte, error) {
	if bd, bdok := cm.BinaryData["ca-bundle.crt"]; bdok {
		return bd, nil
	}
	if d, dok := cm.Data["ca-bundle.crt"]; dok {
		raw, err := base64.StdEncoding.DecodeString(d)
		if err != nil {
			return []byte(d), nil
		}
		return raw, nil
	}
	return nil, fmt.Errorf("%s not found in %s/%s", key, cm.Namespace, cm.Name)
}

// Validates configuration provided in the MachineConfiguration object's spec for each feature
// and updates the status of the object as necessary
func (optr *Operator) syncMachineConfiguration(_ *renderConfig, _ *configv1.ClusterOperator) error {

	// Grab the cluster CR
	mcop, err := optr.mcopLister.Get(ctrlcommon.MCOOperatorKnobsObjectName)
	if err != nil {
		// Create one if it doesn't exist
		if apierrors.IsNotFound(err) {
			klog.Info("MachineConfiguration object doesn't exist; a new one will be created")
			// Using server-side apply here as the NodeDisruption API has a rule technicality which prevents apply using a template manifest like the MCO typically does
			// [spec.nodeDisruptionPolicy.sshkey.actions: Required value, <nil>: Invalid value: "null"]
			p := mcoac.MachineConfiguration(ctrlcommon.MCOOperatorKnobsObjectName).WithSpec(mcoac.MachineConfigurationSpec().WithManagementState("Managed"))
			_, err := optr.mcopClient.OperatorV1().MachineConfigurations().Apply(context.TODO(), p, metav1.ApplyOptions{FieldManager: "machine-config-operator"})
			if err != nil {
				klog.Infof("applying mco object failed: %s", err)
				return err
			}
			// This causes a re-sync and allows the cache for the lister to refresh.
			return nil
		}
		return fmt.Errorf("grabbing MachineConfiguration/%s CR failed: %v", ctrlcommon.MCOOperatorKnobsObjectName, err)
	}

	fg, err := optr.fgAccessor.CurrentFeatureGates()
	if err != nil {
		klog.Errorf("Could not get fg: %v", err)
		return err
	}

	// If FeatureGateNodeDisruptionPolicy feature gate is not enabled, no updates will need to be done for the MachineConfiguration object.
	if fg.Enabled(features.FeatureGateNodeDisruptionPolicy) {
		// Merges the cluster's default node disruption policies with the user defined policies, if any.
		newMachineConfigurationStatus := mcop.Status.DeepCopy()
		newMachineConfigurationStatus.NodeDisruptionPolicyStatus = opv1.NodeDisruptionPolicyStatus{
			ClusterPolicies: apihelpers.MergeClusterPolicies(mcop.Spec.NodeDisruptionPolicy),
		}
		newMachineConfigurationStatus.ObservedGeneration = mcop.GetGeneration()

		// Check if any changes are required in the Status before making the API call.
		if !reflect.DeepEqual(mcop.Status, *newMachineConfigurationStatus) {
			klog.Infof("Updating MachineConfiguration status")
			mcop.Status = *newMachineConfigurationStatus
			mcop, err = optr.mcopClient.OperatorV1().MachineConfigurations().UpdateStatus(context.TODO(), mcop, metav1.UpdateOptions{})
			if err != nil {
				klog.Errorf("MachineConfiguration status apply failed: %v", err)
				return nil
			}
		}
	}

	// If FeatureGateManagedBootImages is enabled, check for opv1.MachineConfigurationBootImageUpdateDegraded condition being true
	// and degrade operator if necessary
	if fg.Enabled(features.FeatureGateManagedBootImages) {
		for _, condition := range mcop.Status.Conditions {
			if condition.Type == opv1.MachineConfigurationBootImageUpdateDegraded && condition.Status == metav1.ConditionTrue {
				return errors.New("bootimage update failed: " + condition.Message)
			}
		}
	}

	return nil
}

// Gets MachineOSConfigs from the lister, assuming that the OnClusterBuild
// FeatureGate is enabled. Will return nil if the FeatureGate is not enabled.
func (optr *Operator) getMachineOSConfigs() ([]*mcfgv1alpha1.MachineOSConfig, error) {
	isOnClusterBuildEnabled, err := optr.isOnClusterBuildFeatureGateEnabled()
	if err != nil {
		return nil, err
	}

	if !isOnClusterBuildEnabled {
		return nil, nil
	}

	moscs, err := optr.moscLister.List(labels.Everything())
	if err != nil {
		return nil, err
	}

	sort.Slice(moscs, func(i, j int) bool { return moscs[i].Name < moscs[j].Name })
	return moscs, nil
}

// Fetches and validates the MachineOSConfigs. For now, validation consists of
// ensuring that the secrets the MachineOSConfig was configured with exist.
func (optr *Operator) getAndValidateMachineOSConfigs() ([]*mcfgv1alpha1.MachineOSConfig, error) {
	moscs, err := optr.getMachineOSConfigs()

	if err != nil {
		return nil, err
	}

	if moscs == nil {
		return nil, nil
	}

	for _, mosc := range moscs {
		if err := build.ValidateMachineOSConfigFromListers(optr.mcpLister, optr.mcoSecretLister, mosc); err != nil {
			return nil, fmt.Errorf("invalid MachineOSConfig %s: %w", mosc.Name, err)
		}
	}

	return moscs, nil
}

// Determines if the OnclusterBuild FeatureGate is enabled. Returns any errors encountered.
func (optr *Operator) isOnClusterBuildFeatureGateEnabled() (bool, error) {
	fg, err := optr.fgAccessor.CurrentFeatureGates()
	if err != nil {
		klog.Errorf("Could not get fg: %v", err)
		return false, err
	}

	return fg.Enabled(features.FeatureGateOnClusterBuild), nil
}

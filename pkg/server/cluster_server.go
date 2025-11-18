package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"time"

	ign3types "github.com/coreos/ignition/v2/config/v3_5/types"
	yaml "github.com/ghodss/yaml"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	mcfginformers "github.com/openshift/client-go/machineconfiguration/informers/externalversions"

	routeclientset "github.com/openshift/client-go/route/clientset/versioned"
	"github.com/openshift/machine-config-operator/internal/clients"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/informers"
	clientset "k8s.io/client-go/kubernetes"
	corelisterv1 "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	clientcmdv1 "k8s.io/client-go/tools/clientcmd/api/v1"
	"k8s.io/klog/v2"

	v1 "github.com/openshift/client-go/machineconfiguration/listers/machineconfiguration/v1"
)

const (
	//nolint:gosec
	bootstrapTokenDir   = "/etc/mcs/bootstrap-token"
	caBundleFilePath    = "/etc/kubernetes/kubelet-ca.crt"
	cloudProviderCAPath = "/etc/kubernetes/static-pod-resources/configmaps/cloud-config/ca-bundle.pem"
	additionalCAPath    = "/etc/pki/ca-trust/source/anchors/openshift-config-user-ca-bundle.crt"
)

// ensure clusterServer implements the
// Server interface.
var _ = Server(&clusterServer{})

type clusterServer struct {
	machineConfigPoolLister v1.MachineConfigPoolLister
	machineConfigLister     v1.MachineConfigLister
	controllerConfigLister  v1.ControllerConfigLister
	configMapLister         corelisterv1.ConfigMapLister
	machineOSConfigLister   v1.MachineOSConfigLister
	machineOSBuildLister    v1.MachineOSBuildLister

	kubeclient  clientset.Interface
	routeclient routeclientset.Interface

	kubeconfigFunc kubeconfigFunc
	apiserverURL   string
}

const minResyncPeriod = 20 * time.Minute

func resyncPeriod() func() time.Duration {
	return func() time.Duration {
		// Disable gosec here to avoid throwing
		// G404: Use of weak random number generator (math/rand instead of crypto/rand)
		// #nosec
		factor := rand.Float64() + 1
		return time.Duration(float64(minResyncPeriod.Nanoseconds()) * factor)
	}
}

// NewClusterServer is used to initialize the machine config
// server that will be used to fetch the requested MachineConfigPool
// objects from within the cluster.
// It accepts a kubeConfig, which is not required when it's
// run from within a cluster(useful in testing).
// It accepts the apiserverURL which is the location of the KubeAPIServer.
func NewClusterServer(kubeConfig, apiserverURL string) (Server, error) {
	clientsBuilder, err := clients.NewBuilder(kubeConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create Kubernetes rest client: %w", err)
	}

	machineConfigClient := clientsBuilder.MachineConfigClientOrDie("machine-config-shared-informer")
	kubeClient := clientsBuilder.KubeClientOrDie("kube-client-shared-informer")
	routeClient := clientsBuilder.RouteClientOrDie("route-client")
	sharedInformerFactory := mcfginformers.NewSharedInformerFactory(machineConfigClient, resyncPeriod()())
	kubeNamespacedSharedInformer := informers.NewFilteredSharedInformerFactory(kubeClient, resyncPeriod()(), "openshift-machine-config-operator", nil)

	mcpInformer, mcInformer, ccInformer, cmInformer, moscInformer, mosbInformer :=
		sharedInformerFactory.Machineconfiguration().V1().MachineConfigPools(),
		sharedInformerFactory.Machineconfiguration().V1().MachineConfigs(),
		sharedInformerFactory.Machineconfiguration().V1().ControllerConfigs(),
		kubeNamespacedSharedInformer.Core().V1().ConfigMaps(),
		sharedInformerFactory.Machineconfiguration().V1().MachineOSConfigs(),
		sharedInformerFactory.Machineconfiguration().V1().MachineOSBuilds()
	mcpLister, mcLister, ccLister, cmLister, moscLister, mosbLister := mcpInformer.Lister(), mcInformer.Lister(), ccInformer.Lister(), cmInformer.Lister(), moscInformer.Lister(), mosbInformer.Lister()
	mcpListerHasSynced, mcListerHasSynced, ccListerHasSynced, cmListerHasSynced, moscListerHasSynced, mosbListerHasSynced :=
		mcpInformer.Informer().HasSynced,
		mcInformer.Informer().HasSynced,
		ccInformer.Informer().HasSynced,
		cmInformer.Informer().HasSynced,
		moscInformer.Informer().HasSynced,
		mosbInformer.Informer().HasSynced

	var informerStopCh chan struct{}
	go sharedInformerFactory.Start(informerStopCh)
	go kubeNamespacedSharedInformer.Start(informerStopCh)

	if !cache.WaitForCacheSync(informerStopCh, mcpListerHasSynced, mcListerHasSynced, ccListerHasSynced, cmListerHasSynced, moscListerHasSynced, mosbListerHasSynced) {
		return nil, errors.New("failed to wait for cache sync")
	}

	return &clusterServer{
		machineConfigPoolLister: mcpLister,
		machineConfigLister:     mcLister,
		controllerConfigLister:  ccLister,
		configMapLister:         cmLister,
		machineOSConfigLister:   moscLister,
		machineOSBuildLister:    mosbLister,
		kubeclient:              kubeClient,
		routeclient:             routeClient,
		kubeconfigFunc:          func() ([]byte, []byte, error) { return kubeconfigFromSecret(bootstrapTokenDir, apiserverURL, nil) },
		apiserverURL:            apiserverURL,
	}, nil
}

// GetConfig fetches the machine config(type - Ignition) from the cluster,
// based on the pool request.
func (cs *clusterServer) GetConfig(cr poolRequest) (*runtime.RawExtension, error) {
	mp, err := cs.machineConfigPoolLister.Get(cr.machineConfigPool)
	if err != nil {
		return nil, fmt.Errorf("could not fetch pool. err: %w", err)
	}

	// For new nodes, we roll out the latest if at least one node has successfully updated.
	// This avoids deadlocks in situations where the old configuration broke somehow
	// (e.g. pull secret expired)
	// and also avoids provisioning a new node, only to update it not long thereafter.
	var currConf string
	if mp.Status.UpdatedMachineCount > 0 {
		currConf = mp.Spec.Configuration.Name
	} else {
		currConf = mp.Status.Configuration.Name
	}

	mc, err := cs.machineConfigLister.Get(currConf)
	if err != nil {
		return nil, fmt.Errorf("could not fetch config %s, err: %w", currConf, err)
	}
	ignConf, err := ctrlcommon.ParseAndConvertConfig(mc.Spec.Config.Raw)
	if err != nil {
		return nil, fmt.Errorf("parsing Ignition config failed with error: %w", err)
	}

	// Update the kubelet cert bundle to the latest in the controllerconfig, in case the pool was paused
	// This also means that the /etc/mcs-machine-config-content.json written to disk will be a lie
	// TODO(jerzhang): improve this process once we have a proper cert management model
	cc, err := cs.controllerConfigLister.Get(ctrlcommon.ControllerConfigName)
	if err != nil {
		return nil, fmt.Errorf("could not get controllerconfig: %w", err)
	}

	// we cannot mock this in a test env
	if cs.configMapLister != nil {
		cm, err := cs.configMapLister.ConfigMaps(ctrlcommon.MCONamespace).Get("kubeconfig-data")
		if err != nil {
			klog.Errorf("Could not get kubeconfig data: %v", err)
		} else {
			if caBundle, ok := cm.BinaryData["ca-bundle.crt"]; ok {
				cs.kubeconfigFunc = func() ([]byte, []byte, error) {
					return kubeconfigFromSecret(bootstrapTokenDir, cs.apiserverURL, caBundle)
				}
			}
		}
	}

	// strip the kargs out if we're going back to a version that doesn't support it
	if err := MigrateKernelArgsIfNecessary(&ignConf, mc, cr.version); err != nil {
		return nil, fmt.Errorf("failed to migrate kernel args %w", err)
	}

	addDataAndMaybeAppendToIgnition(caBundleFilePath, cc.Spec.KubeAPIServerServingCAData, &ignConf)
	addDataAndMaybeAppendToIgnition(cloudProviderCAPath, cc.Spec.CloudProviderCAData, &ignConf)

	desiredImage := cs.resolveDesiredImageForPool(mp)

	appenders := newAppendersBuilder(cr.version, cs.kubeconfigFunc, []string{}, "").
		WithNodeAnnotations(currConf, desiredImage).
		WithCustomAppender(appendDesiredOSImage(desiredImage)).
		build()

	for _, a := range appenders {
		if err := a(&ignConf, mc); err != nil {
			return nil, err
		}
	}

	rawConf, err := json.Marshal(ignConf)
	if err != nil {
		return nil, err
	}
	return &runtime.RawExtension{Raw: rawConf}, nil
}

// kubeconfigFromSecret creates a kubeconfig with the certificate
// and token files in secretDir. If caData is provided, it will instead
// use that to populate the kubeconfig
func kubeconfigFromSecret(secretDir, apiserverURL string, caData []byte) ([]byte, []byte, error) {
	caFile := filepath.Join(secretDir, corev1.ServiceAccountRootCAKey)
	tokenFile := filepath.Join(secretDir, corev1.ServiceAccountTokenKey)
	if caData == nil {
		var err error
		caData, err = os.ReadFile(caFile)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to read %s: %w", caFile, err)
		}
	}
	token, err := os.ReadFile(tokenFile)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read %s: %w", tokenFile, err)
	}

	kubeconfig := clientcmdv1.Config{
		Clusters: []clientcmdv1.NamedCluster{{
			Name: "local",
			Cluster: clientcmdv1.Cluster{
				Server:                   apiserverURL,
				CertificateAuthorityData: caData,
			}},
		},
		AuthInfos: []clientcmdv1.NamedAuthInfo{{
			Name: "kubelet",
			AuthInfo: clientcmdv1.AuthInfo{
				Token: string(token),
			},
		}},
		Contexts: []clientcmdv1.NamedContext{{
			Name: "kubelet",
			Context: clientcmdv1.Context{
				Cluster:  "local",
				AuthInfo: "kubelet",
			},
		}},
		CurrentContext: "kubelet",
	}
	kcData, err := yaml.Marshal(kubeconfig)
	if err != nil {
		return nil, nil, err
	}
	return kcData, caData, nil
}

// Finds the MOSC that targets this MCO and verfies that the MOSC has an image in status
// locates the matching MOSB for the MCP's current or next rendered MC and confirms build succeeded
// and returns the image pullspec when its ready
func (cs *clusterServer) resolveDesiredImageForPool(pool *mcfgv1.MachineConfigPool) string {
	// If listers are not initialized (e.g., in tests or clusters without layering), return empty
	if cs.machineOSConfigLister == nil || cs.machineOSBuildLister == nil {
		return ""
	}

	// Find MachineOSConfig for this pool
	moscList, err := cs.machineOSConfigLister.List(labels.Everything())
	if err != nil {
		// MOSC resources not available
		klog.Infof("Could not list MachineOSConfigs for pool %s: %v", pool.Name, err)
		return ""
	}

	// TODO(dkhater): Simplify to directly get MOSC using pool name with admin_ack enforcing MOSC name == pool name.
	// Versions 4.18.21-4.18.23 lack OCPBUGS-60904 validation (MOSCs could have non-matching names).
	// Can use admin_ack (e.g., at 4.23) to block upgrades until MOSCs are corrected, then replace List+filter with: mosc, err := cs.machineOSConfigLister.Get(pool.Name)
	// See: https://issues.redhat.com/browse/OCPBUGS-60904 for validation bug.
	// See: https://issues.redhat.com/browse/MCO-1935 for cleanup story.

	var mosc *mcfgv1.MachineOSConfig
	for _, config := range moscList {
		if config.Spec.MachineConfigPool.Name == pool.Name {
			mosc = config
			break
		}
	}

	// No MOSC for this pool - not layered
	if mosc == nil {
		return ""
	}

	// Check if image is ready in MOSC status
	moscState := ctrlcommon.NewMachineOSConfigState(mosc)
	if !moscState.HasOSImage() {
		klog.Infof("Pool %s has MachineOSConfig but image not ready yet", pool.Name)
		return ""
	}

	mosbList, err := cs.machineOSBuildLister.List(labels.Everything())
	if err != nil {
		klog.Infof("Could not list MachineOSBuilds for pool %s: %v", pool.Name, err)
		return ""
	}

	var currentConf string
	if pool.Status.UpdatedMachineCount > 0 {
		currentConf = pool.Spec.Configuration.Name
	} else {
		currentConf = pool.Status.Configuration.Name
	}

	var mosb *mcfgv1.MachineOSBuild
	for _, build := range mosbList {
		if build.Spec.MachineOSConfig.Name == mosc.Name &&
			build.Spec.MachineConfig.Name == currentConf {
			mosb = build
			break
		}
	}

	if mosb == nil {
		klog.Infof("Pool %s has MachineOSConfig but no matching MachineOSBuild for MC %s", pool.Name, currentConf)
		return ""
	}

	// Check build is successful
	mosbState := ctrlcommon.NewMachineOSBuildState(mosb)
	if !mosbState.IsBuildSuccess() {
		klog.Infof("Pool %s has MachineOSBuild but build not successful yet", pool.Name)
		return ""
	}

	// Only serve layered images if at least one node has completed the update.
	if pool.Status.UpdatedMachineCount == 0 {
		klog.Infof("Pool %s has successful MOSB %s but no nodes have completed update yet (UpdatedMachineCount=0), will not serve layered image during bootstrap", pool.Name, mosb.Name)
		return ""
	}

	imageSpec := moscState.GetOSImage()

	// Don't serve internal registry images during bootstrap because cluster DNS is not available yet
	// New nodes will bootstrap with base image, then update to layered image after joining cluster (2 reboots)
	// External registries (Quay, etc.) will still get the 1-reboot optimization
	isInternal, err := ctrlcommon.IsOpenShiftRegistry(context.TODO(), imageSpec, cs.kubeclient, cs.routeclient)
	if err != nil {
		klog.Errorf("Failed to check if image is internal registry for pool %s: %v", pool.Name, err)
		return ""
	}

	if isInternal {
		klog.V(4).Infof("New nodes will bootstrap with base image, then update to layered image after joining cluster")
		return ""
	}

	klog.Infof("Resolved layered image for pool %s: %s", pool.Name, imageSpec)
	return imageSpec
}

// appendDesiredOSImage mutates the MC to include the desired OS image.
// This runs appendEncapsulated so mcd-firstboot sees the image on first boot.
func appendDesiredOSImage(desired string) appenderFunc {
	return func(_ *ign3types.Config, mc *mcfgv1.MachineConfig) error {
		if desired == "" {
			return nil
		}
		if mc.Spec.OSImageURL != desired {
			klog.Infof("overriding MC OSImageURL: %q -> %q", mc.Spec.OSImageURL, desired)
			mc.Spec.OSImageURL = desired
		}
		return nil
	}
}

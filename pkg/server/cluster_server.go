package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"time"

	yaml "github.com/ghodss/yaml"
	mcfginformers "github.com/openshift/client-go/machineconfiguration/informers/externalversions"
	"github.com/openshift/machine-config-operator/internal/clients"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/cache"
	clientcmdv1 "k8s.io/client-go/tools/clientcmd/api/v1"

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

	kubeconfigFunc kubeconfigFunc
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
	sharedInformerFactory := mcfginformers.NewSharedInformerFactory(machineConfigClient, resyncPeriod()())

	mcpInformer, mcInformer, ccInformer :=
		sharedInformerFactory.Machineconfiguration().V1().MachineConfigPools(),
		sharedInformerFactory.Machineconfiguration().V1().MachineConfigs(),
		sharedInformerFactory.Machineconfiguration().V1().ControllerConfigs()
	mcpLister, mcLister, ccLister := mcpInformer.Lister(), mcInformer.Lister(), ccInformer.Lister()
	mcpListerHasSynced, mcListerHasSynced, ccListerHasSynced :=
		mcpInformer.Informer().HasSynced,
		mcInformer.Informer().HasSynced,
		ccInformer.Informer().HasSynced

	var informerStopCh chan struct{}
	go sharedInformerFactory.Start(informerStopCh)

	if !cache.WaitForCacheSync(informerStopCh, mcpListerHasSynced, mcListerHasSynced, ccListerHasSynced) {
		return nil, errors.New("failed to wait for cache sync")
	}

	return &clusterServer{
		machineConfigPoolLister: mcpLister,
		machineConfigLister:     mcLister,
		controllerConfigLister:  ccLister,
		kubeconfigFunc:          func() ([]byte, []byte, error) { return kubeconfigFromSecret(bootstrapTokenDir, apiserverURL) },
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

	// strip the kargs out if we're going back to a version that doesn't support it
	if err := MigrateKernelArgsIfNecessary(&ignConf, mc, cr.version); err != nil {
		return nil, fmt.Errorf("failed to migrate kernel args %w", err)
	}

	addDataAndMaybeAppendToIgnition(caBundleFilePath, cc.Spec.KubeAPIServerServingCAData, &ignConf)
	addDataAndMaybeAppendToIgnition(cloudProviderCAPath, cc.Spec.CloudProviderCAData, &ignConf)
	addDataAndMaybeAppendToIgnition(additionalCAPath, cc.Spec.AdditionalTrustBundle, &ignConf)
	appenders := getAppenders(currConf, cr.version, cs.kubeconfigFunc, []string{}, "")
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
// and token files in secretDir
func kubeconfigFromSecret(secretDir, apiserverURL string) ([]byte, []byte, error) {
	caFile := filepath.Join(secretDir, corev1.ServiceAccountRootCAKey)
	tokenFile := filepath.Join(secretDir, corev1.ServiceAccountTokenKey)
	caData, err := os.ReadFile(caFile)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read %s: %w", caFile, err)
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

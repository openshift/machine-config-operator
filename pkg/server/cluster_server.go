package server

import (
	"context"
	"fmt"
	"io/ioutil"
	"net"
	"path/filepath"
	"sync"
	"time"

	"github.com/pkg/errors"

	yaml "github.com/ghodss/yaml"
	"github.com/golang/glog"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	rest "k8s.io/client-go/rest"
	clientcmd "k8s.io/client-go/tools/clientcmd"
	clientcmdv1 "k8s.io/client-go/tools/clientcmd/api/v1"

	oconfigv1 "github.com/openshift/client-go/config/clientset/versioned/typed/config/v1"
	v1 "github.com/openshift/machine-config-operator/pkg/generated/clientset/versioned/typed/machineconfiguration.openshift.io/v1"
)

const (
	// inClusterConfig tells the client to grab the config from the cluster it
	// is running on, instead of using a config passed to it.
	inClusterConfig = ""

	//nolint:gosec
	bootstrapTokenDir = "/etc/mcs/bootstrap-token"

	// machineProvisionedTimeoutSecs is the maximum amount of time we will wait for a reply
	// to the port.  Note that Ignition by default times out requests after 10 seconds, so
	// we need to be lower than that.
	machineProvisionedTimeoutSecs = 5
)

// machineProvisionedPorts is a set of TCP ports the MCS connects to in order
// to check if a machine has already been provisioned.  For now this is
// the SSH port and the kubelet. In the future we might have the MCD itself listen
// on a port that's inside the reserved 9000-9900 range.
var machineProvisionedPorts = []string{"22", "10250"}

// ensure clusterServer implements the
// Server interface.
var _ = Server(&clusterServer{})

type clusterServer struct {
	client kubernetes.Clientset

	configClient oconfigv1.ConfigV1Client
	// machineClient is used to interact with the
	// machine config, pool objects.
	machineClient v1.MachineconfigurationV1Interface

	kubeconfigFunc kubeconfigFunc
}

// NewClusterServer is used to initialize the machine config
// server that will be used to fetch the requested MachineConfigPool
// objects from within the cluster.
// It accepts a kubeConfig, which is not required when it's
// run from within a cluster(useful in testing).
// It accepts the apiserverURL which is the location of the KubeAPIServer.
func NewClusterServer(kubeConfig, apiserverURL string) (Server, error) {
	restConfig, err := getClientConfig(kubeConfig)
	if err != nil {
		return nil, errors.Wrapf(err, "Failed to create Kubernetes rest client")
	}

	client, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return nil, errors.Wrapf(err, "creating core client")
	}

	mc, err := v1.NewForConfig(restConfig)
	if err != nil {
		return nil, errors.Wrapf(err, "creating mc client")
	}
	oc, err := oconfigv1.NewForConfig(restConfig)
	if err != nil {
		return nil, errors.Wrapf(err, "creating config client")
	}
	return &clusterServer{
		client:         *client,
		machineClient:  mc,
		configClient:   *oc,
		kubeconfigFunc: func() ([]byte, []byte, error) { return kubeconfigFromSecret(bootstrapTokenDir, apiserverURL) },
	}, nil
}

// remoteAddrIsProvisioned returns an error if the given IP address responds on a known port.
// If an error is returned, the machine is already up and should not be served Ignition.
func remoteAddrIsProvisioned(remoteAddr string) error {
	startTime := time.Now()
	defer func() {
		glog.Infof("Checked address %s for provisioning in %v", remoteAddr, time.Since(startTime))
	}()
	var wg sync.WaitGroup
	provisionedPort := make(chan error)
	wg.Add(len(machineProvisionedPorts))
	for _, port := range machineProvisionedPorts {
		go func(port string) {
			conn, err := net.DialTimeout("tcp", net.JoinHostPort(remoteAddr, port), time.Second*machineProvisionedTimeoutSecs)
			if err != nil {
				glog.Infof("Checking provisioned port %s: %v", port, err)
			} else {
				provisionedPort <- fmt.Errorf("Address %s responds on port %s; already provisioned", remoteAddr, port)
				conn.Close()
			}
			wg.Done()
		}(port)
	}
	// The above goroutines race to return the first port we find that is provisioned.
	// But in the case where none are found, this goroutine waits for their completion
	// and sends the empty string.
	go func() {
		wg.Wait()
		provisionedPort <- nil
	}()
	ret := <-provisionedPort
	return ret
}

// remoteAddrIsFromPod returns a non-empty string containing a CIDR if the given remote IP
// is from a pod.  We don't want to allow
// pods running on a node to potentially access Ignition; it should
// only be read by the Ignition software itself when the machine
// boots up, before it joins the cluster.
// If the remote address is OK, then the empty string "" is returned.
// Otherwise, error is set.
func remoteAddrIsFromPod(configClient oconfigv1.ConfigV1Client, remoteAddr string) (string, error) {
	network, err := configClient.Networks().Get(context.TODO(), "cluster", metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	remoteIP := net.ParseIP(remoteAddr)
	if remoteIP == nil {
		return "", nil
	}
	for _, n := range network.Status.ClusterNetwork {
		_, ipr, err := net.ParseCIDR(n.CIDR)
		if err != nil {
			glog.V(4).Infof("Parsing CIDR %s: %v", n.CIDR, err)
			continue
		}
		if ipr.Contains(remoteIP) {
			return n.CIDR, nil
		}
	}
	return "", nil
}

func isLocalhost(addr string) bool {
	return addr == "127.0.0.1" || addr == "::1"
}

// Don't serve Ignition to extant nodes; see
// https://bugzilla.redhat.com/show_bug.cgi?id=1707162
// https://github.com/openshift/machine-config-operator/issues/731
func (cs *clusterServer) shouldServeIgnition(remoteAddr string) error {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return err
	}

	if !isLocalhost(host) {
		fromPod, err := remoteAddrIsFromPod(cs.configClient, host)
		if err != nil {
			return err
		}
		if fromPod != "" {
			return &configError{
				msg:       fmt.Sprintf("Address %s is in the pod network %s", host, fromPod),
				forbidden: true,
			}
		}

		err = remoteAddrIsProvisioned(host)
		if err != nil {
			return &configError{
				msg:       err.Error(),
				forbidden: true,
			}
		}
	}

	return nil
}

// GetConfig fetches the machine config(type - Ignition) from the cluster,
// based on the pool request.
func (cs *clusterServer) GetConfig(cr poolRequest) (*runtime.RawExtension, error) {
	mp, err := cs.machineClient.MachineConfigPools().Get(context.TODO(), cr.machineConfigPool, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("could not fetch pool. err: %v", err)
	}

	if cr.remoteAddr != "" {
		// By default for now, we run in warn-only mode
		provisionCheckError := false
		config, err := cs.client.CoreV1().ConfigMaps("openshift-machine-config-operator").Get(context.TODO(), "machine-config-server", metav1.GetOptions{})
		if err != nil {
			if !apierrors.IsNotFound(err) {
				return nil, errors.Wrapf(err, "Fetching mcs config")
			}
		} else {
			if config.Data["provision-check"] != "" {
				provisionCheckError = true
			}
		}
		if err := cs.shouldServeIgnition(cr.remoteAddr); err != nil {
			if provisionCheckError {
				return nil, err
			}
			glog.Warningf("Would deny serving ignition (enable configmap/machine-config-server/provision-check to enforce): %s", err)
		}
	}

	currConf := mp.Status.Configuration.Name

	mc, err := cs.machineClient.MachineConfigs().Get(context.TODO(), currConf, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("could not fetch config %s, err: %v", currConf, err)
	}

	appenders := getAppenders(currConf, cs.kubeconfigFunc, mc.Spec.OSImageURL)
	for _, a := range appenders {
		if err := a(mc); err != nil {
			return nil, err
		}
	}

	rawIgn, err := machineConfigToRawIgnition(mc)
	if err != nil {
		return nil, fmt.Errorf("server: could not convert MachineConfig to raw Ignition: %v", err)
	}

	return rawIgn, nil
}

// getClientConfig returns a Kubernetes client Config.
func getClientConfig(path string) (*rest.Config, error) {
	if path != inClusterConfig {
		// build Config from a kubeconfig filepath
		return clientcmd.BuildConfigFromFlags("", path)
	}
	// uses pod's service account to get a Config
	return rest.InClusterConfig()
}

// kubeconfigFromSecret creates a kubeconfig with the certificate
// and token files in secretDir
func kubeconfigFromSecret(secretDir, apiserverURL string) ([]byte, []byte, error) {
	caFile := filepath.Join(secretDir, corev1.ServiceAccountRootCAKey)
	tokenFile := filepath.Join(secretDir, corev1.ServiceAccountTokenKey)
	caData, err := ioutil.ReadFile(caFile)
	if err != nil {
		return nil, nil, fmt.Errorf("Failed to read %s: %v", caFile, err)
	}
	token, err := ioutil.ReadFile(tokenFile)
	if err != nil {
		return nil, nil, fmt.Errorf("Failed to read %s: %v", tokenFile, err)
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

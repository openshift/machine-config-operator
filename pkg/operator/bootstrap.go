package operator

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"k8s.io/klog/v2"

	configv1 "github.com/openshift/api/config/v1"
	mcfgv1alpha1 "github.com/openshift/api/machineconfiguration/v1alpha1"
	configscheme "github.com/openshift/client-go/config/clientset/versioned/scheme"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/scheme"

	templatectrl "github.com/openshift/machine-config-operator/pkg/controller/template"
)

type manifest struct {
	name     string
	data     []byte
	filename string
}

// RenderBootstrap writes to destinationDir static Pods.
func RenderBootstrap(
	additionalTrustBundleFile,
	proxyFile,
	clusterConfigConfigMapFile,
	infraFile, networkFile, dnsFile,
	cloudConfigFile, cloudProviderCAFile,
	mcsCAFile, kubeAPIServerServingCA, pullSecretFile string,
	imgs *Images,
	destinationDir, releaseImage string,
) error {
	filesData := map[string][]byte{}
	files := []string{
		proxyFile,
		clusterConfigConfigMapFile,
		infraFile,
		networkFile,
		mcsCAFile,
		pullSecretFile,
		dnsFile,
	}
	if kubeAPIServerServingCA != "" {
		files = append(files, kubeAPIServerServingCA)
	}
	if cloudProviderCAFile != "" {
		files = append(files, cloudProviderCAFile)
	}
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		filesData[file] = data
	}

	// create ControllerConfigSpec
	obji, err := runtime.Decode(configscheme.Codecs.UniversalDecoder(configv1.SchemeGroupVersion), filesData[infraFile])
	if err != nil {
		return err
	}
	infra, ok := obji.(*configv1.Infrastructure)
	if !ok {
		return fmt.Errorf("expected *configv1.Infrastructure found %T", obji)
	}

	obji, err = runtime.Decode(configscheme.Codecs.UniversalDecoder(configv1.SchemeGroupVersion), filesData[proxyFile])
	if err != nil {
		return err
	}
	proxy, ok := obji.(*configv1.Proxy)
	if !ok {
		return fmt.Errorf("expected *configv1.Proxy found %T", obji)
	}

	obji, err = runtime.Decode(configscheme.Codecs.UniversalDecoder(configv1.SchemeGroupVersion), filesData[networkFile])
	if err != nil {
		return err
	}
	network, ok := obji.(*configv1.Network)
	if !ok {
		return fmt.Errorf("expected *configv1.Network found %T", obji)
	}

	obji, err = runtime.Decode(configscheme.Codecs.UniversalDecoder(configv1.SchemeGroupVersion), filesData[dnsFile])
	if err != nil {
		return err
	}
	dns, ok := obji.(*configv1.DNS)
	if !ok {
		return fmt.Errorf("expected *configv1.DNS found %T", obji)
	}

	spec, err := createDiscoveredControllerConfigSpec(infra, network, proxy, dns)
	if err != nil {
		return err
	}

	additionalTrustBundleData, err := os.ReadFile(additionalTrustBundleFile)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if additionalTrustBundleData != nil {
		obji, err := runtime.Decode(scheme.Codecs.UniversalDecoder(corev1.SchemeGroupVersion), additionalTrustBundleData)
		if err != nil {
			return err
		}
		additionalTrustBundle, ok := obji.(*corev1.ConfigMap)
		if !ok {
			return fmt.Errorf("expected *corev1.ConfigMap found %T", obji)
		}
		spec.AdditionalTrustBundle = []byte(additionalTrustBundle.Data["ca-bundle.crt"])
	}

	// if the cloudConfig is set in infra read the cloudConfigFile
	if infra.Spec.CloudConfig.Name != "" {
		cloudConf, err := loadBootstrapCloudProviderConfig(infra, cloudConfigFile)
		if err != nil {
			return fmt.Errorf("failed to load the cloud provider config: %w", err)
		}
		spec.CloudProviderConfig = cloudConf
	}

	bundle := make([]byte, 0)
	bundle = append(bundle, filesData[mcsCAFile]...)
	// Append the kube-ca if given.
	if _, ok := filesData[kubeAPIServerServingCA]; ok {
		spec.KubeAPIServerServingCAData = filesData[kubeAPIServerServingCA]
	}
	// Set the cloud-provider CA if given.
	if data, ok := filesData[cloudProviderCAFile]; ok {
		spec.CloudProviderCAData = data
	}

	spec.RootCAData = bundle
	spec.PullSecret = nil
	spec.BaseOSContainerImage = imgs.BaseOSContainerImage
	spec.BaseOSExtensionsContainerImage = imgs.BaseOSExtensionsContainerImage
	spec.ReleaseImage = releaseImage
	spec.Images = map[string]string{
		templatectrl.MachineConfigOperatorKey: imgs.MachineConfigOperator,

		templatectrl.APIServerWatcherKey:    imgs.MachineConfigOperator,
		templatectrl.InfraImageKey:          imgs.InfraImage,
		templatectrl.KeepalivedKey:          imgs.Keepalived,
		templatectrl.CorednsKey:             imgs.Coredns,
		templatectrl.HaproxyKey:             imgs.Haproxy,
		templatectrl.BaremetalRuntimeCfgKey: imgs.BaremetalRuntimeCfg,
		templatectrl.KubeRbacProxyKey:       imgs.KubeRbacProxy,
	}

	config := getRenderConfig("", string(filesData[kubeAPIServerServingCA]), spec, &imgs.RenderConfigImages, infra.Status.APIServerInternalURL, nil, []*mcfgv1alpha1.MachineOSConfig{})

	manifests := []manifest{
		{
			name:     "manifests/machineconfigcontroller/controllerconfig.yaml",
			filename: "bootstrap/manifests/machineconfigcontroller-controllerconfig.yaml",
		}, {
			name:     "manifests/master.machineconfigpool.yaml",
			filename: "bootstrap/manifests/master.machineconfigpool.yaml",
		}, {
			name:     "manifests/worker.machineconfigpool.yaml",
			filename: "bootstrap/manifests/worker.machineconfigpool.yaml",
		}, {
			name:     "manifests/bootstrap-pod-v2.yaml",
			filename: "bootstrap/machineconfigoperator-bootstrap-pod.yaml",
		}, {
			data:     filesData[pullSecretFile],
			filename: "bootstrap/manifests/machineconfigcontroller-pull-secret",
		}, {
			name:     "manifests/machineconfigserver/csr-bootstrap-role-binding.yaml",
			filename: "manifests/csr-bootstrap-role-binding.yaml",
		}, {
			name:     "manifests/machineconfigserver/kube-apiserver-serving-ca-configmap.yaml",
			filename: "manifests/kube-apiserver-serving-ca-configmap.yaml",
		},
	}

	manifests = appendManifestsByPlatform(manifests, *infra)

	for _, m := range manifests {
		var b []byte
		var err error
		switch {
		case len(m.name) > 0:
			klog.Info(m.name)
			b, err = renderAsset(config, m.name)
			if err != nil {
				return err
			}
		case len(m.data) > 0:
			b = m.data
		default:
			continue
		}

		path := filepath.Join(destinationDir, m.filename)
		dirname := filepath.Dir(path)
		if err := os.MkdirAll(dirname, 0o755); err != nil {
			return err
		}
		// Disable gosec here to avoid throwing
		// G306: Expect WriteFile permissions to be 0600 or less
		// #nosec
		if err := os.WriteFile(path, b, 0o644); err != nil {
			return err
		}
	}
	return nil
}

func appendManifestsByPlatform(manifests []manifest, infra configv1.Infrastructure) []manifest {
	lbType := configv1.LoadBalancerTypeOpenShiftManagedDefault
	if infra.Status.PlatformStatus.BareMetal != nil {
		if infra.Status.PlatformStatus.BareMetal.LoadBalancer != nil {
			lbType = infra.Status.PlatformStatus.BareMetal.LoadBalancer.Type
		}
		manifests = getPlatformManifests(manifests, strings.ToLower(string(configv1.BareMetalPlatformType)), lbType)
	}

	if infra.Status.PlatformStatus.OpenStack != nil {
		if infra.Status.PlatformStatus.OpenStack.LoadBalancer != nil {
			lbType = infra.Status.PlatformStatus.OpenStack.LoadBalancer.Type
		}
		manifests = getPlatformManifests(manifests, strings.ToLower(string(configv1.OpenStackPlatformType)), lbType)
	}

	if infra.Status.PlatformStatus.Ovirt != nil {
		if infra.Status.PlatformStatus.Ovirt.LoadBalancer != nil {
			lbType = infra.Status.PlatformStatus.Ovirt.LoadBalancer.Type
		}
		manifests = getPlatformManifests(manifests, strings.ToLower(string(configv1.OvirtPlatformType)), lbType)
	}

	if infra.Status.PlatformStatus.VSphere != nil {
		deployInternalLB := true
		// vSphere allows to use a user managed load balancer by not setting the VIPs in PlatformStatus.
		// We will maintain backward compatibility by checking if the VIPs are not set, we will not deploy
		// Keepalived and CoreDNS.
		if len(infra.Status.PlatformStatus.VSphere.APIServerInternalIPs) == 0 {
			deployInternalLB = false
		}

		if infra.Status.PlatformStatus.VSphere.LoadBalancer != nil {
			deployInternalLB = configv1.LoadBalancerTypeOpenShiftManagedDefault == infra.Status.PlatformStatus.VSphere.LoadBalancer.Type
		}

		if deployInternalLB {
			manifests = append(manifests,
				manifest{
					name:     "manifests/on-prem/coredns.yaml",
					filename: "vsphere/manifests/coredns.yaml",
				},
				manifest{
					name:     "manifests/on-prem/coredns-corefile.tmpl",
					filename: "vsphere/static-pod-resources/coredns/Corefile.tmpl",
				},
				manifest{
					name:     "manifests/on-prem/keepalived.yaml",
					filename: "vsphere/manifests/keepalived.yaml",
				},
				manifest{
					name:     "manifests/on-prem/keepalived.conf.tmpl",
					filename: "vsphere/static-pod-resources/keepalived/keepalived.conf.tmpl",
				},
			)
		}
	}

	if infra.Status.PlatformStatus.Nutanix != nil {
		if infra.Status.PlatformStatus.Nutanix.LoadBalancer != nil {
			lbType = infra.Status.PlatformStatus.Nutanix.LoadBalancer.Type
		}
		manifests = getPlatformManifests(manifests, strings.ToLower(string(configv1.NutanixPlatformType)), lbType)
	}

	if infra.Status.PlatformStatus.GCP != nil {
		// Generate just the CoreDNS manifests for the GCP platform only when the DNSType is `ClusterHosted`.
		if infra.Status.PlatformStatus.GCP.CloudLoadBalancerConfig != nil && infra.Status.PlatformStatus.GCP.CloudLoadBalancerConfig.DNSType == configv1.ClusterHostedDNSType {
			// We do not need the keepalived manifests to be generated because the cloud default Load Balancers are in use.
			// So, setting the lbType to `UserManaged` although the default cloud LBs are not user managed.
			lbType = configv1.LoadBalancerTypeUserManaged
			manifests = getPlatformManifests(manifests, strings.ToLower(string(configv1.GCPPlatformType)), lbType)
		}
	}

	return manifests
}

// loadBootstrapCloudProviderConfig reads the cloud provider config from cloudConfigFile based on infra object.
func loadBootstrapCloudProviderConfig(infra *configv1.Infrastructure, cloudConfigFile string) (string, error) {
	data, err := os.ReadFile(cloudConfigFile)
	if err != nil {
		return "", err
	}
	obji, err := runtime.Decode(scheme.Codecs.UniversalDecoder(corev1.SchemeGroupVersion), data)
	if err != nil {
		return "", err
	}
	cm, ok := obji.(*corev1.ConfigMap)
	if !ok {
		return "", fmt.Errorf("expected *corev1.ConfigMap found %T", obji)
	}
	cloudConf, ok := cm.Data["cloud.conf"]
	if !ok {
		klog.Infof("falling back to reading cloud provider config from user specified key %s", infra.Spec.CloudConfig.Key)
		cloudConf = cm.Data[infra.Spec.CloudConfig.Key]
	}
	return cloudConf, nil
}

func getPlatformManifests(manifests []manifest, platformName string, lbType configv1.PlatformLoadBalancerType) []manifest {
	var corednsName string
	var corefileName string
	switch platformName {
	case strings.ToLower(string(configv1.GCPPlatformType)):
		corednsName = "manifests/cloud-platform-alt-dns/coredns.yaml"
		corefileName = "manifests/cloud-platform-alt-dns/coredns-corefile.tmpl"
	default:
		corednsName = "manifests/on-prem/coredns.yaml"
		corefileName = "manifests/on-prem/coredns-corefile.tmpl"
	}

	platformManifests := append(manifests,
		manifest{
			name:     corednsName,
			filename: platformName + "/manifests/coredns.yaml",
		},
		manifest{
			name:     corefileName,
			filename: platformName + "/static-pod-resources/coredns/Corefile.tmpl",
		},
	)
	if lbType == configv1.LoadBalancerTypeOpenShiftManagedDefault || lbType == "" {
		platformManifests = append(platformManifests,
			manifest{
				name:     "manifests/on-prem/keepalived.yaml",
				filename: platformName + "/manifests/keepalived.yaml",
			},
			manifest{
				name:     "manifests/on-prem/keepalived.conf.tmpl",
				filename: platformName + "/static-pod-resources/keepalived/keepalived.conf.tmpl",
			},
		)
	}
	return platformManifests
}

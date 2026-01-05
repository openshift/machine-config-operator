package operator

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"k8s.io/klog/v2"

	configv1 "github.com/openshift/api/config/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	templatectrl "github.com/openshift/machine-config-operator/pkg/controller/template"
)

type manifest struct {
	name     string
	data     []byte
	filename string
}

// RenderBootstrap writes to destinationDir static Pods.
func RenderBootstrap(
	dependenciesFiles BootstrapDependenciesFiles,
	imgs *ctrlcommon.Images,
	destinationDir, releaseImage string,
) error {
	dependencies, err := NewBootstrapDependencies(dependenciesFiles)
	if err != nil {
		return fmt.Errorf("error parsing dependencies for MCO bootstrap: %w", err)
	}

	config, err := buildSpec(dependencies, imgs, releaseImage)
	if err != nil {
		return fmt.Errorf("error building spec for MCO bootstrap: %w", err)
	}

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
			data:     []byte(dependencies.PullSecret),
			filename: "bootstrap/manifests/machineconfigcontroller-pull-secret",
		}, {
			name:     "manifests/machineconfigserver/csr-bootstrap-role-binding.yaml",
			filename: "manifests/csr-bootstrap-role-binding.yaml",
		}, {
			name:     "manifests/machineconfigserver/kube-apiserver-serving-ca-configmap.yaml",
			filename: "manifests/kube-apiserver-serving-ca-configmap.yaml",
		},
	}

	if dependencies.Infrastructure.Status.ControlPlaneTopology == configv1.HighlyAvailableArbiterMode {
		manifests = append(manifests, manifest{
			name:     "manifests/arbiter.machineconfigpool.yaml",
			filename: "bootstrap/manifests/arbiter.machineconfigpool.yaml",
		})
	}

	manifests = appendManifestsByPlatform(manifests, dependencies.Infrastructure)

	for _, m := range manifests {
		var b []byte
		var err error
		switch {
		case m.name != "":
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

func buildSpec(dependencies *BootstrapDependencies, imgs *ctrlcommon.Images, releaseImage string) (*renderConfig, error) {

	// create ControllerConfigSpec
	spec, err := createDiscoveredControllerConfigSpec(
		dependencies.Infrastructure,
		dependencies.Network,
		dependencies.Proxy,
		dependencies.DNS)
	if err != nil {
		return nil, err
	}

	if dependencies.AdditionalTrustBundle != "" {
		spec.AdditionalTrustBundle = []byte(dependencies.AdditionalTrustBundle)
	}

	if dependencies.CloudConfig != "" {
		spec.CloudProviderConfig = dependencies.CloudConfig
	}

	// Append the kube-ca if given.
	if dependencies.KubeAPIServerServingCA != "" {
		spec.KubeAPIServerServingCAData = []byte(dependencies.KubeAPIServerServingCA)
	}
	// Set the cloud-provider CA if given.
	if dependencies.CloudProviderCA != "" {
		spec.CloudProviderCAData = []byte(dependencies.CloudProviderCA)
	}

	spec.RootCAData = []byte(dependencies.MCSCA)
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
		templatectrl.DockerRegistryKey:      imgs.DockerRegistry,
	}

	config := getRenderConfig("", dependencies.KubeAPIServerServingCA, spec,
		&imgs.RenderConfigImages, dependencies.Infrastructure, nil, nil, "2")
	return config, nil
}

func appendManifestsByPlatform(manifests []manifest, infra *configv1.Infrastructure) []manifest {
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
		// TODO(mko) It is not clear why for user-managed LB and for ELB we want to skip CoreDNS as every
		// other platform deploys CoreDNS without keepalived, and only vSphere skips both.
		// As this only refactors the existing code, I do not want to change this behaviour.

		// vSphere allows setting user-managed LB by simply leaving the VIPs in PlatformStatus empty.
		if len(infra.Status.PlatformStatus.VSphere.APIServerInternalIPs) == 0 {
			return manifests
		}
		if infra.Status.PlatformStatus.VSphere.LoadBalancer != nil {
			if infra.Status.PlatformStatus.VSphere.LoadBalancer.Type != configv1.LoadBalancerTypeOpenShiftManagedDefault {
				return manifests
			}
		}
		manifests = getPlatformManifests(manifests, strings.ToLower(string(configv1.VSpherePlatformType)), lbType)
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
	if infra.Status.PlatformStatus.AWS != nil {
		// Generate just the CoreDNS manifests for the AWS platform only when the DNSType is `ClusterHosted`.
		if infra.Status.PlatformStatus.AWS.CloudLoadBalancerConfig != nil && infra.Status.PlatformStatus.AWS.CloudLoadBalancerConfig.DNSType == configv1.ClusterHostedDNSType {
			// We do not need the keepalived manifests to be generated because the cloud default Load Balancers are in use.
			// So, setting the lbType to `UserManaged` although the default cloud LBs are not user managed.
			lbType = configv1.LoadBalancerTypeUserManaged
			manifests = getPlatformManifests(manifests, strings.ToLower(string(configv1.AWSPlatformType)), lbType)
		}
	}
	if infra.Status.PlatformStatus.Azure != nil {
		// Generate just the CoreDNS manifests for the Azure platform only when the DNSType is `ClusterHosted`.
		if infra.Status.PlatformStatus.Azure.CloudLoadBalancerConfig != nil && infra.Status.PlatformStatus.Azure.CloudLoadBalancerConfig.DNSType == configv1.ClusterHostedDNSType {
			// We do not need the keepalived manifests to be generated because the cloud default Load Balancers are in use.
			// So, setting the lbType to `UserManaged` although the default cloud LBs are not user managed.
			lbType = configv1.LoadBalancerTypeUserManaged
			manifests = getPlatformManifests(manifests, strings.ToLower(string(configv1.AzurePlatformType)), lbType)
		}
	}

	return manifests
}

func getPlatformManifests(manifests []manifest, platformName string, lbType configv1.PlatformLoadBalancerType) []manifest {
	var corednsName string
	var corefileName string
	switch platformName {
	case strings.ToLower(string(configv1.GCPPlatformType)), strings.ToLower(string(configv1.AWSPlatformType)), strings.ToLower(string(configv1.AzurePlatformType)):
		corednsName = "manifests/cloud-platform-alt-dns/coredns.yaml"
		corefileName = "manifests/cloud-platform-alt-dns/coredns-corefile.tmpl"
	default:
		corednsName = "manifests/on-prem/coredns.yaml"
		corefileName = "manifests/on-prem/coredns-corefile.tmpl"
	}

	platformManifests := append([]manifest{},
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

	return append(manifests, platformManifests...)
}

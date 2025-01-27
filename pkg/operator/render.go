package operator

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"net"
	"strings"
	"text/template"

	"github.com/apparentlymart/go-cidr/cidr"
	"github.com/ghodss/yaml"
	"k8s.io/klog/v2"

	configv1 "github.com/openshift/api/config/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	mcfgv1alpha1 "github.com/openshift/api/machineconfiguration/v1alpha1"
	"github.com/openshift/machine-config-operator/manifests"
	"github.com/openshift/machine-config-operator/pkg/constants"
	utilrand "k8s.io/apimachinery/pkg/util/rand"
	utilnet "k8s.io/utils/net"
)

type LoadBalancerIPState string

const (
	availableLBIPState LoadBalancerIPState = "Available"
	absentLBIPState    LoadBalancerIPState = "Absent"
	defaultLBIPState   LoadBalancerIPState = "Default"
)

type renderConfig struct {
	TargetNamespace        string
	Version                string
	ReleaseVersion         string
	ControllerConfig       mcfgv1.ControllerConfigSpec
	KubeAPIServerServingCA string
	APIServerURL           string
	Images                 *ctrlcommon.RenderConfigImages
	Infra                  configv1.Infrastructure
	Constants              map[string]string
	PointerConfig          string
	MachineOSConfigs       []*mcfgv1alpha1.MachineOSConfig
	TLSMinVersion          string
	TLSCipherSuites        []string
}

type assetRenderer struct {
	Path         string
	tmpl         *template.Template
	templateData string
}

func newAssetRenderer(path string) *assetRenderer {
	return &assetRenderer{
		Path: path,
		tmpl: template.New(path),
	}
}

func (a *assetRenderer) read() error {
	objBytes, err := manifests.ReadFile(a.Path)
	if err != nil {
		return fmt.Errorf("error getting asset %s: %w", a.Path, err)
	}
	a.templateData = string(objBytes)
	return nil
}

func (a *assetRenderer) addTemplateFuncs() {
	funcs := ctrlcommon.GetTemplateFuncMap()
	funcs["toYAML"] = toYAML
	funcs["onPremPlatformAPIServerInternalIP"] = onPremPlatformAPIServerInternalIP
	funcs["onPremPlatformAPIServerInternalIPs"] = onPremPlatformAPIServerInternalIPs
	funcs["onPremPlatformIngressIP"] = onPremPlatformIngressIP
	funcs["onPremPlatformIngressIPs"] = onPremPlatformIngressIPs
	funcs["onPremPlatformShortName"] = onPremPlatformShortName
	funcs["cloudPlatformAPIIntLoadBalancerIPs"] = cloudPlatformAPIIntLoadBalancerIPs
	funcs["cloudPlatformAPILoadBalancerIPs"] = cloudPlatformAPILoadBalancerIPs
	funcs["cloudPlatformIngressLoadBalancerIPs"] = cloudPlatformIngressLoadBalancerIPs
	funcs["cloudPlatformLBIPAvailable"] = cloudPlatformLBIPAvailable
	funcs["join"] = strings.Join

	a.tmpl = a.tmpl.Funcs(funcs)
}

func (a *assetRenderer) render(config interface{}) ([]byte, error) {
	tmpl, err := a.tmpl.Parse(a.templateData)
	if err != nil {
		return nil, fmt.Errorf("failed to parse asset %s: %w", a.Path, err)
	}

	buf := new(bytes.Buffer)
	if err := tmpl.Execute(buf, config); err != nil {
		return nil, fmt.Errorf("failed to execute template: %w", err)
	}

	return buf.Bytes(), nil
}

func renderAsset(config *renderConfig, path string) ([]byte, error) {
	asset := newAssetRenderer(path)
	if err := asset.read(); err != nil {
		return nil, err
	}

	if config.Constants == nil {
		config.Constants = constants.ConstantsByName
	}

	asset.addTemplateFuncs()

	return asset.render(config)
}

func toYAML(i interface{}) []byte {
	out, err := yaml.Marshal(i)
	if err != nil {
		panic(err)
	}
	return out
}

// createDiscoveredControllerConfigSpec uses the Infrastructure and Network global configuration to discover various
// fields for the controller spec.
// Infrastructure provides information about the platform, etcd discovery domain.
// Network provides the service network that is used to calculate the cluster DNS IP.
func createDiscoveredControllerConfigSpec(infra *configv1.Infrastructure, network *configv1.Network, proxy *configv1.Proxy, dns *configv1.DNS) (*mcfgv1.ControllerConfigSpec, error) {
	if len(network.Spec.ServiceNetwork) == 0 {
		return nil, fmt.Errorf("service cidr is empty in Network")
	}
	dnsIP, err := clusterDNSIP(network.Spec.ServiceNetwork[0])
	if err != nil {
		return nil, err
	}
	ipFamilies, err := ipFamilies(network.Spec.ServiceNetwork)
	if err != nil {
		return nil, err
	}

	// The PlatformStatus field is set in cluster versions >= 4.2
	// Otherwise default to NonePlatformType
	//nolint:staticcheck
	if infra.Status.PlatformStatus == nil && infra.Status.Platform != "" {
		infra.Status.PlatformStatus = &configv1.PlatformStatus{
			Type: infra.Status.Platform,
		}
	} else if infra.Status.PlatformStatus == nil || infra.Status.PlatformStatus.Type == "" {
		infra.Status.PlatformStatus = &configv1.PlatformStatus{
			Type: configv1.NonePlatformType,
		}
	}

	platform := "none"
	if infra.Status.PlatformStatus.Type != "" {
		platform = strings.ToLower(string(infra.Status.PlatformStatus.Type))
	}

	ccSpec := &mcfgv1.ControllerConfigSpec{
		ClusterDNSIP:        dnsIP,
		IPFamilies:          ipFamilies,
		CloudProviderConfig: "",
		// EtcdDiscoveryDomain is unused and deprecated in favour of using Infra.Status.EtcdDiscoveryDomain directly
		// Still populating it here for now until it will be removed eventually
		EtcdDiscoveryDomain: infra.Status.EtcdDiscoveryDomain,
		// Platform is unused and deprecated in favour of using Infra.Status.PlatformStatus.Type directly
		// Still populating it here for now until it will be removed eventually
		Platform: platform,
		Infra:    infra,
		DNS:      dns,
	}
	switch {
	case network.Status.NetworkType == "":
		// At install time, when CNO has not started, status is unset, use the value in spec.
		ccSpec.NetworkType = network.Spec.NetworkType
	case network.Status.Migration != nil:
		// At sdn migration prepare phase, use the value in status.migration to prepare the node.
		ccSpec.NetworkType = network.Status.Migration.NetworkType

		// Set any MTU migration parameters as well
		if network.Status.Migration.MTU != nil {
			ccSpec.Network = &mcfgv1.NetworkInfo{
				MTUMigration: network.Status.Migration.MTU,
			}
		}
	default:
		// After installation, the MCO should not assume the network is changing just because the spec changed, it needs to wait until CNO updates the status.
		ccSpec.NetworkType = network.Status.NetworkType
	}

	if proxy != nil {
		if proxy.Status == (configv1.ProxyStatus{}) {
			klog.V(2).Info("Not setting proxy config because Proxy status is empty")
		} else {
			ccSpec.Proxy = &proxy.Status
		}
	}

	return ccSpec, nil
}

func clusterDNSIP(iprange string) (string, error) {
	_, network, err := net.ParseCIDR(iprange)
	if err != nil {
		return "", err
	}
	ip, err := cidr.Host(network, 10)
	if err != nil {
		return "", err
	}
	return ip.String(), nil
}

func ipFamilies(serviceCIDRs []string) (mcfgv1.IPFamiliesType, error) {
	var ipv4, ipv6, v6primary bool
	for i, cidr := range serviceCIDRs {
		if utilnet.IsIPv6CIDRString(cidr) {
			ipv6 = true
			if i == 0 {
				v6primary = true
			}
		} else {
			ipv4 = true
		}
	}

	switch {
	case ipv4 && ipv6:
		if v6primary {
			return mcfgv1.IPFamiliesDualStackIPv6Primary, nil
		}
		return mcfgv1.IPFamiliesDualStack, nil
	case ipv4:
		return mcfgv1.IPFamiliesIPv4, nil
	case ipv6:
		return mcfgv1.IPFamiliesIPv6, nil
	default:
		return "", fmt.Errorf("could not determine cluster IP families from config")
	}
}

// GenerateProxyCookieSecret creates a random b64 encoded secret
// for the proxy cookie secret object
func (rc renderConfig) GenerateProxyCookieSecret() string {
	return base64.StdEncoding.EncodeToString([]byte(utilrand.String(32)))
}

func onPremPlatformShortName(cfg mcfgv1.ControllerConfigSpec) interface{} {
	if cfg.Infra.Status.PlatformStatus != nil {
		switch cfg.Infra.Status.PlatformStatus.Type {
		case configv1.BareMetalPlatformType:
			return "kni"
		case configv1.OvirtPlatformType:
			return "ovirt"
		case configv1.OpenStackPlatformType:
			return "openstack"
		case configv1.VSpherePlatformType:
			return "vsphere"
		case configv1.NutanixPlatformType:
			return "nutanix"
		default:
			return ""
		}
	}
	return ""
}

// This function should be removed in 4.13 when we no longer have to worry
// about upgrades from releases that still use it.
//
//nolint:dupl
func onPremPlatformIngressIP(cfg mcfgv1.ControllerConfigSpec) (interface{}, error) {
	if cfg.Infra.Status.PlatformStatus != nil {
		switch cfg.Infra.Status.PlatformStatus.Type {
		case configv1.BareMetalPlatformType:
			return cfg.Infra.Status.PlatformStatus.BareMetal.IngressIPs[0], nil
		case configv1.OvirtPlatformType:
			return cfg.Infra.Status.PlatformStatus.Ovirt.IngressIPs[0], nil
		case configv1.OpenStackPlatformType:
			return cfg.Infra.Status.PlatformStatus.OpenStack.IngressIPs[0], nil
		case configv1.VSpherePlatformType:
			if len(cfg.Infra.Status.PlatformStatus.VSphere.IngressIPs) > 0 {
				return cfg.Infra.Status.PlatformStatus.VSphere.IngressIPs[0], nil
			}
			return nil, nil
		case configv1.NutanixPlatformType:
			return cfg.Infra.Status.PlatformStatus.Nutanix.IngressIPs[0], nil
		default:
			return nil, fmt.Errorf("invalid platform for Ingress IP")
		}
	}
	return nil, fmt.Errorf("")
}

//nolint:dupl
func onPremPlatformIngressIPs(cfg mcfgv1.ControllerConfigSpec) (interface{}, error) {
	if cfg.Infra.Status.PlatformStatus != nil {
		switch cfg.Infra.Status.PlatformStatus.Type {
		case configv1.BareMetalPlatformType:
			return cfg.Infra.Status.PlatformStatus.BareMetal.IngressIPs, nil
		case configv1.OvirtPlatformType:
			return cfg.Infra.Status.PlatformStatus.Ovirt.IngressIPs, nil
		case configv1.OpenStackPlatformType:
			return cfg.Infra.Status.PlatformStatus.OpenStack.IngressIPs, nil
		case configv1.VSpherePlatformType:
			if cfg.Infra.Status.PlatformStatus.VSphere.IngressIPs != nil {
				return cfg.Infra.Status.PlatformStatus.VSphere.IngressIPs, nil
			}
			return []string{}, nil
		case configv1.NutanixPlatformType:
			return cfg.Infra.Status.PlatformStatus.Nutanix.IngressIPs, nil
		default:
			return nil, fmt.Errorf("invalid platform for Ingress IP")
		}
	}
	return nil, fmt.Errorf("")
}

// This function should be removed in 4.13 when we no longer have to worry
// about upgrades from releases that still use it.
//
//nolint:dupl
func onPremPlatformAPIServerInternalIP(cfg mcfgv1.ControllerConfigSpec) (interface{}, error) {
	if cfg.Infra.Status.PlatformStatus != nil {
		switch cfg.Infra.Status.PlatformStatus.Type {
		case configv1.BareMetalPlatformType:
			return cfg.Infra.Status.PlatformStatus.BareMetal.APIServerInternalIPs[0], nil
		case configv1.OvirtPlatformType:
			return cfg.Infra.Status.PlatformStatus.Ovirt.APIServerInternalIPs[0], nil
		case configv1.OpenStackPlatformType:
			return cfg.Infra.Status.PlatformStatus.OpenStack.APIServerInternalIPs[0], nil
		case configv1.VSpherePlatformType:
			if len(cfg.Infra.Status.PlatformStatus.VSphere.APIServerInternalIPs) > 0 {
				return cfg.Infra.Status.PlatformStatus.VSphere.APIServerInternalIPs[0], nil
			}
			return nil, nil
		case configv1.NutanixPlatformType:
			return cfg.Infra.Status.PlatformStatus.Nutanix.APIServerInternalIPs[0], nil
		default:
			return nil, fmt.Errorf("invalid platform for API Server Internal IP")
		}
	}
	return nil, fmt.Errorf("")
}

//nolint:dupl
func onPremPlatformAPIServerInternalIPs(cfg mcfgv1.ControllerConfigSpec) (interface{}, error) {
	if cfg.Infra.Status.PlatformStatus != nil {
		switch cfg.Infra.Status.PlatformStatus.Type {
		case configv1.BareMetalPlatformType:
			return cfg.Infra.Status.PlatformStatus.BareMetal.APIServerInternalIPs, nil
		case configv1.OvirtPlatformType:
			return cfg.Infra.Status.PlatformStatus.Ovirt.APIServerInternalIPs, nil
		case configv1.OpenStackPlatformType:
			return cfg.Infra.Status.PlatformStatus.OpenStack.APIServerInternalIPs, nil
		case configv1.VSpherePlatformType:
			if cfg.Infra.Status.PlatformStatus.VSphere.APIServerInternalIPs != nil {
				return cfg.Infra.Status.PlatformStatus.VSphere.APIServerInternalIPs, nil
			}
			return []string{}, nil
		case configv1.NutanixPlatformType:
			return cfg.Infra.Status.PlatformStatus.Nutanix.APIServerInternalIPs, nil
		default:
			return nil, fmt.Errorf("invalid platform for API Server Internal IP")
		}
	}
	return nil, fmt.Errorf("")
}

// cloudPlatformAPIIntLoadBalancerIPs provides the API-Int Server IPs for
// supported cloud platforms when the DNSType is set to `ClusterHosted`.
func cloudPlatformAPIIntLoadBalancerIPs(cfg mcfgv1.ControllerConfigSpec) (interface{}, error) {
	if cfg.Infra.Status.PlatformStatus != nil {
		switch cfg.Infra.Status.PlatformStatus.Type {
		case configv1.GCPPlatformType:
			switch cloudPlatformLoadBalancerIPState(cfg) {
			case availableLBIPState:
				return cfg.Infra.Status.PlatformStatus.GCP.CloudLoadBalancerConfig.ClusterHosted.APIIntLoadBalancerIPs, nil
			case absentLBIPState:
				return nil, fmt.Errorf("GCP API Server Internal IPs unavailable when the DNSType is ClusterHosted")
			default:
				return nil, fmt.Errorf("")
			}
		case configv1.AWSPlatformType:
			switch cloudPlatformLoadBalancerIPState(cfg) {
			case availableLBIPState:
				return cfg.Infra.Status.PlatformStatus.AWS.CloudLoadBalancerConfig.ClusterHosted.APIIntLoadBalancerIPs, nil
			case absentLBIPState:
				return nil, fmt.Errorf("AWS API Server Internal IPs unavailable when the DNSType is ClusterHosted")
			default:
				return nil, fmt.Errorf("")
			}
		default:
			return nil, fmt.Errorf("invalid cloud platform for API Server Internal IP")
		}
	} else {
		return nil, fmt.Errorf("")
	}
}

// cloudPlatformAPILoadBalancerIPs provides the API Server IPs for supported
// cloud platforms when the DNSType is set to `ClusterHosted`.
func cloudPlatformAPILoadBalancerIPs(cfg mcfgv1.ControllerConfigSpec) (interface{}, error) {
	if cfg.Infra.Status.PlatformStatus != nil {
		switch cfg.Infra.Status.PlatformStatus.Type {
		case configv1.GCPPlatformType:
			switch cloudPlatformLoadBalancerIPState(cfg) {
			case availableLBIPState:
				return cfg.Infra.Status.PlatformStatus.GCP.CloudLoadBalancerConfig.ClusterHosted.APILoadBalancerIPs, nil
			case absentLBIPState:
				return nil, fmt.Errorf("GCP API Server IPs unavailable when the DNSType is ClusterHosted")
			default:
				return nil, fmt.Errorf("")
			}
		case configv1.AWSPlatformType:
			switch cloudPlatformLoadBalancerIPState(cfg) {
			case availableLBIPState:
				return cfg.Infra.Status.PlatformStatus.AWS.CloudLoadBalancerConfig.ClusterHosted.APILoadBalancerIPs, nil
			case absentLBIPState:
				return nil, fmt.Errorf("AWS API Server IPs unavailable when the DNSType is ClusterHosted")
			default:
				return nil, fmt.Errorf("")
			}
		default:
			return nil, fmt.Errorf("invalid cloud platform for API Server IPs")
		}
	} else {
		return nil, fmt.Errorf("")
	}
}

// cloudPlatformIngressLoadBalancerIPs provides the Ingress IPs for supported
// cloud platforms when the DNSType is set to `ClusterHosted`.
func cloudPlatformIngressLoadBalancerIPs(cfg mcfgv1.ControllerConfigSpec) (interface{}, error) {
	if cfg.Infra.Status.PlatformStatus != nil {
		switch cfg.Infra.Status.PlatformStatus.Type {
		case configv1.GCPPlatformType:
			switch cloudPlatformLoadBalancerIPState(cfg) {
			case availableLBIPState:
				return cfg.Infra.Status.PlatformStatus.GCP.CloudLoadBalancerConfig.ClusterHosted.IngressLoadBalancerIPs, nil
			case absentLBIPState:
				return nil, fmt.Errorf("GCP Ingress IPs unavailable when the DNSType is ClusterHosted")
			default:
				return nil, fmt.Errorf("")
			}
		case configv1.AWSPlatformType:
			switch cloudPlatformLoadBalancerIPState(cfg) {
			case availableLBIPState:
				return cfg.Infra.Status.PlatformStatus.AWS.CloudLoadBalancerConfig.ClusterHosted.IngressLoadBalancerIPs, nil
			case absentLBIPState:
				return nil, fmt.Errorf("AWS Ingress IPs unavailable when the DNSType is ClusterHosted")
			default:
				return nil, fmt.Errorf("")
			}
		default:
			return nil, fmt.Errorf("invalid cloud platform for Ingress LoadBalancer IPs")
		}
	} else {
		return nil, fmt.Errorf("")
	}
}

// cloudPlatformLBIPAvailable returns true when DNSType is set to `ClusterHosted`
// and LB IPs are provided as part of `PlatformStatus`.
func cloudPlatformLBIPAvailable(cfg mcfgv1.ControllerConfigSpec) bool {
	if cfg.Infra.Status.PlatformStatus != nil {
		switch cfg.Infra.Status.PlatformStatus.Type {
		case configv1.GCPPlatformType:
			switch cloudPlatformLoadBalancerIPState(cfg) {
			case availableLBIPState:
				return true
			default:
				return false
			}
		case configv1.AWSPlatformType:
			switch cloudPlatformLoadBalancerIPState(cfg) {
			case availableLBIPState:
				return true
			default:
				return false
			}
		default:
			return false
		}
	} else {
		return false
	}
}

// cloudPlatformLoadBalancerIPState is a helper function that determines if
// LoadBalancer config has been set.
func cloudPlatformLoadBalancerIPState(cfg mcfgv1.ControllerConfigSpec) LoadBalancerIPState {
	lbIPState := defaultLBIPState
	if cfg.Infra.Status.PlatformStatus != nil {
		switch cfg.Infra.Status.PlatformStatus.Type {
		case configv1.GCPPlatformType:
			// If DNSType is set to `ClusterHosted`, we expect the Load Balancer IP addresses to be set.
			// If absent, that is expected to be temporary.
			if cfg.Infra.Status.PlatformStatus.GCP != nil && cfg.Infra.Status.PlatformStatus.GCP.CloudLoadBalancerConfig != nil && cfg.Infra.Status.PlatformStatus.GCP.CloudLoadBalancerConfig.DNSType == configv1.ClusterHostedDNSType {
				if cfg.Infra.Status.PlatformStatus.GCP.CloudLoadBalancerConfig.ClusterHosted != nil {
					lbIPState = availableLBIPState
				} else {
					lbIPState = absentLBIPState
				}
			}
		case configv1.AWSPlatformType:
			// If DNSType is set to `ClusterHosted`, we expect the Load Balancer IP addresses to be set.
			// If absent, that is expected to be temporary.
			if cfg.Infra.Status.PlatformStatus.AWS != nil && cfg.Infra.Status.PlatformStatus.AWS.CloudLoadBalancerConfig != nil && cfg.Infra.Status.PlatformStatus.AWS.CloudLoadBalancerConfig.DNSType == configv1.ClusterHostedDNSType {
				if cfg.Infra.Status.PlatformStatus.AWS.CloudLoadBalancerConfig.ClusterHosted != nil {
					lbIPState = availableLBIPState
				} else {
					lbIPState = absentLBIPState
				}
			}
		}
	}
	return lbIPState
}

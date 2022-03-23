package operator

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"net"
	"strings"
	"text/template"

	"github.com/Masterminds/sprig"
	"github.com/apparentlymart/go-cidr/cidr"
	"github.com/ghodss/yaml"
	"github.com/golang/glog"

	configv1 "github.com/openshift/api/config/v1"

	"github.com/openshift/machine-config-operator/manifests"
	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	"github.com/openshift/machine-config-operator/pkg/constants"
	utilrand "k8s.io/apimachinery/pkg/util/rand"
	utilnet "k8s.io/utils/net"
)

type renderConfig struct {
	TargetNamespace        string
	Version                string
	ControllerConfig       mcfgv1.ControllerConfigSpec
	APIServerURL           string
	Images                 *RenderConfigImages
	KubeAPIServerServingCA string
	Infra                  configv1.Infrastructure
	Constants              map[string]string
	PointerConfig          string
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
		return fmt.Errorf("error getting asset %s: %v", a.Path, err)
	}
	a.templateData = string(objBytes)
	return nil
}

func (a *assetRenderer) addTemplateFuncs() {
	funcs := sprig.TxtFuncMap()
	funcs["toYAML"] = toYAML
	funcs["onPremPlatformAPIServerInternalIP"] = onPremPlatformAPIServerInternalIP
	funcs["onPremPlatformIngressIP"] = onPremPlatformIngressIP
	funcs["onPremPlatformShortName"] = onPremPlatformShortName
	funcs["onPremPlatformKeepalivedEnableUnicast"] = onPremPlatformKeepalivedEnableUnicast

	a.tmpl = a.tmpl.Funcs(funcs)
}

func (a *assetRenderer) render(config interface{}) ([]byte, error) {
	tmpl, err := a.tmpl.Parse(a.templateData)
	if err != nil {
		return nil, fmt.Errorf("failed to parse asset %s: %v", a.Path, err)
	}

	buf := new(bytes.Buffer)
	if err := tmpl.Execute(buf, config); err != nil {
		return nil, fmt.Errorf("failed to execute template: %v", err)
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
	if network.Status.NetworkType == "" {
		// At install time, when CNO has not started, status is unset, use the value in spec.
		ccSpec.NetworkType = network.Spec.NetworkType
	} else if network.Status.Migration != nil {
		// At sdn migration prepare phase, use the value in status.migration to prepare the node.
		ccSpec.NetworkType = network.Status.Migration.NetworkType

		// Set any MTU migration parameters as well
		if network.Status.Migration.MTU != nil {
			ccSpec.Network = &mcfgv1.NetworkInfo{
				MTUMigration: network.Status.Migration.MTU,
			}
		}
	} else {
		// After installation, the MCO should not assume the network is changing just because the spec changed, it needs to wait until CNO updates the status.
		ccSpec.NetworkType = network.Status.NetworkType
	}

	if proxy != nil {
		if proxy.Status == (configv1.ProxyStatus{}) {
			glog.V(2).Info("Not setting proxy config because Proxy status is empty")
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
	var ipv4, ipv6 bool
	for _, cidr := range serviceCIDRs {
		if utilnet.IsIPv6CIDRString(cidr) {
			ipv6 = true
		} else {
			ipv4 = true
		}
	}

	switch {
	case ipv4 && ipv6:
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
		case configv1.KubevirtPlatformType:
			return "kubevirt"
		case configv1.NutanixPlatformType:
			return "nutanix"
		default:
			return ""
		}
	} else {
		return ""
	}
}

func onPremPlatformKeepalivedEnableUnicast(cfg mcfgv1.ControllerConfigSpec) (interface{}, error) {
	if cfg.Infra.Status.PlatformStatus != nil {
		switch cfg.Infra.Status.PlatformStatus.Type {
		case configv1.BareMetalPlatformType, configv1.KubevirtPlatformType:
			return "yes", nil
		default:
			return "no", nil
		}
	} else {
		return "no", nil
	}
}

//nolint:dupl
func onPremPlatformIngressIP(cfg mcfgv1.ControllerConfigSpec) (interface{}, error) {
	if cfg.Infra.Status.PlatformStatus != nil {
		switch cfg.Infra.Status.PlatformStatus.Type {
		case configv1.BareMetalPlatformType:
			return cfg.Infra.Status.PlatformStatus.BareMetal.IngressIP, nil
		case configv1.OvirtPlatformType:
			return cfg.Infra.Status.PlatformStatus.Ovirt.IngressIP, nil
		case configv1.OpenStackPlatformType:
			return cfg.Infra.Status.PlatformStatus.OpenStack.IngressIP, nil
		case configv1.VSpherePlatformType:
			return cfg.Infra.Status.PlatformStatus.VSphere.IngressIP, nil
		case configv1.KubevirtPlatformType:
			return cfg.Infra.Status.PlatformStatus.Kubevirt.IngressIP, nil
		case configv1.NutanixPlatformType:
			return cfg.Infra.Status.PlatformStatus.Nutanix.IngressIP, nil
		default:
			return nil, fmt.Errorf("invalid platform for Ingress IP")
		}
	} else {
		return nil, fmt.Errorf("")
	}
}

//nolint:dupl
func onPremPlatformAPIServerInternalIP(cfg mcfgv1.ControllerConfigSpec) (interface{}, error) {
	if cfg.Infra.Status.PlatformStatus != nil {
		switch cfg.Infra.Status.PlatformStatus.Type {
		case configv1.BareMetalPlatformType:
			return cfg.Infra.Status.PlatformStatus.BareMetal.APIServerInternalIP, nil
		case configv1.OvirtPlatformType:
			return cfg.Infra.Status.PlatformStatus.Ovirt.APIServerInternalIP, nil
		case configv1.OpenStackPlatformType:
			return cfg.Infra.Status.PlatformStatus.OpenStack.APIServerInternalIP, nil
		case configv1.VSpherePlatformType:
			return cfg.Infra.Status.PlatformStatus.VSphere.APIServerInternalIP, nil
		case configv1.KubevirtPlatformType:
			return cfg.Infra.Status.PlatformStatus.Kubevirt.APIServerInternalIP, nil
		case configv1.NutanixPlatformType:
			return cfg.Infra.Status.PlatformStatus.Nutanix.APIServerInternalIP, nil
		default:
			return nil, fmt.Errorf("invalid platform for API Server Internal IP")
		}
	} else {
		return nil, fmt.Errorf("")
	}
}

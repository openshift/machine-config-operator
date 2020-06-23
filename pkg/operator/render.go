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

	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	"github.com/openshift/machine-config-operator/pkg/operator/assets"
	utilrand "k8s.io/apimachinery/pkg/util/rand"
)

type renderConfig struct {
	TargetNamespace        string
	Version                string
	ControllerConfig       mcfgv1.ControllerConfigSpec
	APIServerURL           string
	Images                 *RenderConfigImages
	KubeAPIServerServingCA string
	Infra                  configv1.Infrastructure
}

func renderAsset(config *renderConfig, path string) ([]byte, error) {
	objBytes, err := assets.Asset(path)
	if err != nil {
		return nil, fmt.Errorf("error getting asset %s: %v", path, err)
	}

	funcs := sprig.TxtFuncMap()
	funcs["toYAML"] = toYAML
	tmpl, err := template.New(path).Funcs(funcs).Parse(string(objBytes))
	if err != nil {
		return nil, fmt.Errorf("failed to parse asset %s: %v", path, err)
	}

	buf := new(bytes.Buffer)
	if err := tmpl.Execute(buf, config); err != nil {
		return nil, fmt.Errorf("failed to execute template: %v", err)
	}

	return buf.Bytes(), nil
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
func createDiscoveredControllerConfigSpec(infra *configv1.Infrastructure, network *configv1.Network, proxy *configv1.Proxy) (*mcfgv1.ControllerConfigSpec, error) {
	if len(network.Spec.ServiceNetwork) == 0 {
		return nil, fmt.Errorf("service cidr is empty in Network")
	}
	dnsIP, err := clusterDNSIP(network.Spec.ServiceNetwork[0])
	if err != nil {
		return nil, err
	}
	ipv6, err := isSingleStackIPv6(network.Spec.ServiceNetwork)
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
		KubeletIPv6:         ipv6,
		CloudProviderConfig: "",
		// EtcdDiscoveryDomain is unused and deprecated in favour of using Infra.Status.EtcdDiscoveryDomain directly
		// Still populating it here for now until it will be removed eventually
		EtcdDiscoveryDomain: infra.Status.EtcdDiscoveryDomain,
		// Platform is unused and deprecated in favour of using Infra.Status.PlatformStatus.Type directly
		// Still populating it here for now until it will be removed eventually
		Platform: platform,
		Infra:    infra,
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

func isSingleStackIPv6(serviceCIDRs []string) (bool, error) {
	for _, cidr := range serviceCIDRs {
		ip, _, err := net.ParseCIDR(cidr)
		if err != nil {
			return false, err
		}
		if ip.To4() != nil {
			return false, nil
		}
	}
	return true, nil
}

// GenerateProxyCookieSecret creates a random b64 encoded secret
// for the proxy cookie secret object
func (rc renderConfig) GenerateProxyCookieSecret() string {
	return base64.StdEncoding.EncodeToString([]byte(utilrand.String(32)))
}

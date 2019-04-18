package operator

import (
	"bytes"
	"fmt"
	"net"
	"text/template"

	"github.com/Masterminds/sprig"
	"github.com/apparentlymart/go-cidr/cidr"
	"github.com/ghodss/yaml"

	configv1 "github.com/openshift/api/config/v1"

	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	"github.com/openshift/machine-config-operator/pkg/operator/assets"
)

type renderConfig struct {
	TargetNamespace  string
	Version          string
	ControllerConfig mcfgv1.ControllerConfigSpec
	APIServerURL     string
	Images           Images
	KubeAPIServerServingCA string
}

func renderAsset(config renderConfig, path string) ([]byte, error) {
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
func createDiscoveredControllerConfigSpec(infra *configv1.Infrastructure, network *configv1.Network) (*mcfgv1.ControllerConfigSpec, error) {
	if len(network.Spec.ServiceNetwork) == 0 {
		return nil, fmt.Errorf("service cidr is empty in Network")
	}
	dnsIP, err := clusterDNSIP(network.Spec.ServiceNetwork[0])
	if err != nil {
		return nil, err
	}

	platform := "none"
	switch infra.Status.Platform {
	case configv1.AWSPlatformType:
		platform = "aws"
	case configv1.AzurePlatformType:
		platform = "azure"
	case configv1.OpenStackPlatformType:
		platform = "openstack"
	case configv1.LibvirtPlatformType:
		platform = "libvirt"
	case configv1.VSpherePlatformType:
		platform = "vsphere"
	}

	return &mcfgv1.ControllerConfigSpec{
		ClusterDNSIP:        dnsIP,
		CloudProviderConfig: "",
		EtcdDiscoveryDomain: infra.Status.EtcdDiscoveryDomain,
		Platform:            platform,
	}, nil
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

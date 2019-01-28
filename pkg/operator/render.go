package operator

import (
	"bytes"
	"fmt"
	"net"
	"text/template"

	"github.com/Masterminds/sprig"
	"github.com/apparentlymart/go-cidr/cidr"
	"github.com/ghodss/yaml"
	"github.com/golang/glog"
	installertypes "github.com/openshift/installer/pkg/types"
	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	"github.com/openshift/machine-config-operator/pkg/operator/assets"
)

type renderConfig struct {
	TargetNamespace  string
	Version          string
	ControllerConfig mcfgv1.ControllerConfigSpec
	Images           Images
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

type installConfigGetter func() (installertypes.InstallConfig, error)

func discoverMCOConfig(f installConfigGetter) (*mcfgv1.MCOConfig, error) {
	ic, err := f()
	if err != nil {
		return nil, err
	}
	dnsIP, err := clusterDNSIP(ic.Networking.ServiceCIDR.String())
	if err != nil {
		return nil, err
	}

	platform, err := platformFromInstallConfig(ic)
	if err != nil {
		glog.Warningf("Warning: %v, using %s", err, platform)
	}

	return &mcfgv1.MCOConfig{
		Spec: mcfgv1.MCOConfigSpec{
			ClusterDNSIP:        dnsIP,
			CloudProviderConfig: "",
			ClusterName:         ic.ObjectMeta.Name,
			Platform:            platform,
			BaseDomain:          ic.BaseDomain,
			SSHKey:              ic.SSHKey,
		},
	}, nil
}

func platformFromInstallConfig(ic installertypes.InstallConfig) (string, error) {
	// TODO: these constants are wrong, they should match what is reported by the infrastructure provider
	switch {
	case ic.Platform.AWS != nil:
		return "aws", nil
	case ic.Platform.OpenStack != nil:
		return "openstack", nil
	case ic.Libvirt != nil:
		return "libvirt", nil
	case ic.None != nil:
		return "none", nil
	default:
		return "none", fmt.Errorf("the install config referenced a platform other than 'aws', 'libvirt', 'openstack', or 'none'")
	}
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

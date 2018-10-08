package operator

import (
	"bytes"
	"fmt"
	"net"
	"text/template"

	"github.com/Masterminds/sprig"
	cidr "github.com/apparentlymart/go-cidr/cidr"
	installertypes "github.com/openshift/installer/pkg/types"

	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	"github.com/openshift/machine-config-operator/pkg/operator/assets"
)

type renderConfig struct {
	TargetNamespace  string
	Version          string
	ControllerConfig mcfgv1.ControllerConfigSpec
	Images           images
}

func renderAsset(config renderConfig, path string) ([]byte, error) {
	objBytes, err := assets.Asset(path)
	if err != nil {
		return nil, fmt.Errorf("error getting asset %s: %v", path, err)
	}

	funcs := sprig.TxtFuncMap()
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

	var eic int
	for _, m := range ic.Machines {
		if m.Name == "master" && m.Replicas != nil {
			eic = int(*(m.Replicas))
		}
	}
	if eic == 0 {
		return nil, fmt.Errorf("EtcdInitialCount cannot be empty")
	}

	return &mcfgv1.MCOConfig{
		Spec: mcfgv1.MCOConfigSpec{
			ClusterDNSIP:        dnsIP,
			CloudProviderConfig: "",
			ClusterName:         ic.Name,
			Platform:            platformFromInstallConfig(ic),
			BaseDomain:          ic.BaseDomain,
			EtcdInitialCount:    eic,
		},
	}, nil
}

func platformFromInstallConfig(ic installertypes.InstallConfig) string {
	switch {
	case ic.Platform.AWS != nil:
		return "aws"
	case ic.Platform.OpenStack != nil:
		return "openstack"
	case ic.Libvirt != nil:
		return "libvirt"
	default:
		panic("invalid platform")
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

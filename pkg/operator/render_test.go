package operator

import (
	"fmt"
	"testing"

	"github.com/ghodss/yaml"
	installertypes "github.com/openshift/installer/pkg/types"
)

func TestClusterDNSIP(t *testing.T) {
	tests := []struct {
		Range  string
		Output string
		Error  bool
	}{{
		Range:  "192.168.2.0/20",
		Output: "192.168.0.10",
	}, {
		Range:  "2001:db8::/32",
		Output: "2001:db8::a",
	}, {
		Range: "192.168.1.254/32",
		Error: true,
	}}
	for idx, test := range tests {
		t.Run(fmt.Sprintf("case#%d", idx), func(t *testing.T) {
			desc := fmt.Sprintf("clusterDNSIP(%#v)", test.Range)
			gotIP, err := clusterDNSIP(test.Range)
			if err != nil {
				if !test.Error {
					t.Fatalf("%s failed: %s", desc, err.Error())
				}
			}
			if gotIP != test.Output {
				t.Fatalf("%s failed: got = %s want = %s", desc, gotIP, test.Output)
			}
		})
	}
}

var (
	testInstallConfig = []byte(`
sshKey: ssh-rsa AAAA....
baseDomain: tt.testing
clusterID: 2d149e46-90ee-3436-018a-1b02f6864006
machines:
- name: master
  platform:
    libvirt:
      qcowImagePath: /path/rhcos-qemu.qcow2
  replicas: 1
- name: worker
  platform:
    libvirt:
      qcowImagePath: /path/rhcos-qemu.qcow2
  replicas: 2
metadata:
  creationTimestamp: null
  name: test-0
networking:
  podCIDR: 10.2.0.0/16
  serviceCIDR: 10.3.0.0/16
  type: flannel
platform:
  libvirt:
    URI: qemu:///system
    masterIPs: null
    network:
      if: tt0
      ipRange: 192.168.124.0/24
      name: tectonic
      resolver: 8.8.8.8
pullSecret: ''
`)
)

func TestDiscoverMCOConfig(t *testing.T) {
	icgetter := func() (installertypes.InstallConfig, error) {
		var ic installertypes.InstallConfig
		if err := yaml.Unmarshal(testInstallConfig, &ic); err != nil {
			return ic, err
		}
		return ic, nil
	}

	mco, err := discoverMCOConfig(icgetter)
	if err != nil {
		t.Fatalf("couldn't parse installconfig: %v", err)
	}

	if got, want := mco.Spec.ClusterDNSIP, "10.3.0.10"; got != want {
		t.Fatalf("mismatch got = %v want = %v", got, want)
	}
	if got, want := mco.Spec.ClusterName, "test-0"; got != want {
		t.Fatalf("mismatch got = %v want = %v", got, want)
	}
	if got, want := mco.Spec.Platform, "libvirt"; got != want {
		t.Fatalf("mismatch got = %v want = %v", got, want)
	}
	if got, want := mco.Spec.BaseDomain, "tt.testing"; got != want {
		t.Fatalf("mismatch got = %v want = %v", got, want)
	}
}

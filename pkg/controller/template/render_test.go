package template

import (
	"bytes"
	"flag"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"testing"

	igntypes "github.com/coreos/ignition/v2/config/v3_0/types"
	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	"k8s.io/client-go/kubernetes/scheme"
)

func TestMain(m *testing.M) {
	flag.Parse()
	os.Exit(m.Run())
}

func TestCloudProvider(t *testing.T) {
	dummyTemplate := []byte(`{{cloudProvider .}}`)

	cases := []struct {
		platform string
		res      string
	}{{
		platform: "aws",
		res:      "aws",
	}, {
		platform: "openstack",
		res:      "openstack",
	}, {
		platform: "baremetal",
		res:      "",
	}, {
		platform: "gcp",
		res:      "gce",
	}, {
		platform: "libvirt",
		res:      "",
	}, {
		platform: "none",
		res:      "",
	}, {
		platform: "vsphere",
		res:      "vsphere",
	}}
	for idx, c := range cases {
		name := fmt.Sprintf("case #%d", idx)
		t.Run(name, func(t *testing.T) {
			config := &mcfgv1.ControllerConfig{
				Spec: mcfgv1.ControllerConfigSpec{
					Platform: c.platform,
				},
			}
			got, err := renderTemplate(RenderConfig{&config.Spec, `{"dummy":"dummy"}`}, name, dummyTemplate)
			if err != nil {
				t.Fatalf("expected nil error %v", err)
			}

			if string(got) != c.res {
				t.Fatalf("mismatch got: %s want: %s", got, c.res)
			}
		})
	}
}

func TestCloudConfigFlag(t *testing.T) {
	dummyTemplate := []byte(`{{cloudConfigFlag .}}`)

	cases := []struct {
		platform string
		content  string
		res      string
	}{{
		platform: "aws",
		content:  "",
		res:      "",
	}, {
		platform: "azure",
		content:  "",
		res:      "",
	}, {
		platform: "aws",
		content: `
[dummy-config]
    option = a
`,
		res: "",
	}, {
		platform: "azure",
		content: `
[dummy-config]
    option = a
`,
		res: "--cloud-config=/etc/kubernetes/cloud.conf",
	}}
	for idx, c := range cases {
		name := fmt.Sprintf("case #%d", idx)
		t.Run(name, func(t *testing.T) {
			config := &mcfgv1.ControllerConfig{
				Spec: mcfgv1.ControllerConfigSpec{
					Platform:            c.platform,
					CloudProviderConfig: c.content,
				},
			}
			got, err := renderTemplate(RenderConfig{&config.Spec, `{"dummy":"dummy"}`}, name, dummyTemplate)
			if err != nil {
				t.Fatalf("expected nil error %v", err)
			}

			if string(got) != c.res {
				t.Fatalf("mismatch got: %s want: %s", got, c.res)
			}
		})
	}
}

func TestEtcdPeerCertDNSNames(t *testing.T) {
	dummyTemplate := []byte(`{{etcdPeerCertDNSNames .}}`)

	cases := []struct {
		etcdDiscoveryDomain string

		url string
		err bool
	}{{
		etcdDiscoveryDomain: "",
		url:                 "",
		err:                 true,
	}, {
		etcdDiscoveryDomain: "my-test-cluster.tt.testing",
		url:                 "${ETCD_DNS_NAME},my-test-cluster.tt.testing",
		err:                 false,
	}}
	for idx, c := range cases {
		name := fmt.Sprintf("case #%d", idx)
		t.Run(name, func(t *testing.T) {
			config := &mcfgv1.ControllerConfig{
				Spec: mcfgv1.ControllerConfigSpec{
					EtcdDiscoveryDomain: c.etcdDiscoveryDomain,
				},
			}
			got, err := renderTemplate(RenderConfig{&config.Spec, `{"dummy":"dummy"}`}, name, dummyTemplate)
			if err != nil && !c.err {
				t.Fatalf("expected nil error %v", err)
			}

			if string(got) != c.url {
				t.Fatalf("mismatch got: %s want: %s", got, c.url)
			}
		})
	}
}

func TestEtcdServerCertDNSNames(t *testing.T) {
	dummyTemplate := []byte(`{{etcdServerCertDNSNames .}}`)

	cases := []struct {
		url string
		err bool
	}{{
		url: "localhost,etcd.kube-system.svc,etcd.kube-system.svc.cluster.local,etcd.openshift-etcd.svc,etcd.openshift-etcd.svc.cluster.local,${ETCD_WILDCARD_DNS_NAME}",
		err: false,
	}}
	for idx, c := range cases {
		name := fmt.Sprintf("case #%d", idx)
		t.Run(name, func(t *testing.T) {
			config := &mcfgv1.ControllerConfig{
				Spec: mcfgv1.ControllerConfigSpec{},
			}
			got, err := renderTemplate(RenderConfig{&config.Spec, `{"dummy":"dummy"}`}, name, dummyTemplate)
			if err != nil && !c.err {
				t.Fatalf("expected nil error %v", err)
			}

			if string(got) != c.url {
				t.Fatalf("mismatch got: %s want: %s", got, c.url)
			}
		})
	}
}

func TestSkipMissing(t *testing.T) {
	dummyTemplate := `{{skip "%s"}}`

	cases := []struct {
		key string
		err bool
		res string
	}{{
		key: "",
		err: true,
		res: "",
	}, {
		key: "2two",
		err: true,
		res: "",
	}, {
		key: "test index",
		err: true,
		res: "",
	}, {
		key: "index",
		err: false,
		res: "{{.index}}",
	}, {
		key: "etcd_index",
		err: false,
		res: "{{.etcd_index}}",
	}}

	for idx, c := range cases {
		name := fmt.Sprintf("case #%d", idx)
		t.Run(name, func(t *testing.T) {
			tmpl := []byte(fmt.Sprintf(dummyTemplate, c.key))
			got, err := renderTemplate(RenderConfig{}, name, tmpl)
			if err != nil && !c.err {
				t.Fatalf("expected nil error %v", err)
			}

			if string(got) != c.res {
				t.Fatalf("mismatch got: %s want: %s", got, c.res)
			}
		})
	}
}

const (
	templateDir = "../../../templates"
	resultDir   = "./test_data/templates"
)

var (
	configs = map[string]string{
		"aws":       "./test_data/controller_config_aws.yaml",
		"baremetal": "./test_data/controller_config_baremetal.yaml",
		"gcp":       "./test_data/controller_config_gcp.yaml",
		"openstack": "./test_data/controller_config_openstack.yaml",
		"libvirt":   "./test_data/controller_config_libvirt.yaml",
		"none":      "./test_data/controller_config_none.yaml",
		"vsphere":   "./test_data/controller_config_vsphere.yaml",
	}
)

func TestInvalidPlatform(t *testing.T) {
	controllerConfig, err := controllerConfigFromFile(configs["aws"])
	if err != nil {
		t.Fatalf("failed to get controllerconfig config: %v", err)
	}

	expectErr := func(err error, want string) {
		t.Helper()
		if err == nil {
			t.Fatalf("expect err %s, got nil", want)
		}
		if err.Error() != want {
			t.Fatalf("expect err %s, got %s", want, err.Error())
		}
	}

	// we must treat unrecognized constants as "none"
	controllerConfig.Spec.Platform = "_bad_"
	_, err = generateTemplateMachineConfigs(&RenderConfig{&controllerConfig.Spec, `{"dummy":"dummy"}`}, templateDir)
	if err != nil {
		t.Errorf("expect nil error, got: %v", err)
	}

	// explicitly blocked
	controllerConfig.Spec.Platform = "_base"
	_, err = generateTemplateMachineConfigs(&RenderConfig{&controllerConfig.Spec, `{"dummy":"dummy"}`}, templateDir)
	expectErr(err, "failed to create MachineConfig for role master: platform _base unsupported")
}

func TestGenerateMachineConfigs(t *testing.T) {
	for _, config := range configs {
		controllerConfig, err := controllerConfigFromFile(config)
		if err != nil {
			t.Fatalf("failed to get controllerconfig config: %v", err)
		}

		cfgs, err := generateTemplateMachineConfigs(&RenderConfig{&controllerConfig.Spec, `{"dummy":"dummy"}`}, templateDir)
		if err != nil {
			t.Fatalf("failed to generate machine configs: %v", err)
		}

		foundPullSecretMaster := false
		foundPullSecretWorker := false
		foundKubeletUnitMaster := false
		foundKubeletUnitWorker := false

		for _, cfg := range cfgs {
			if cfg.Labels == nil {
				t.Fatal("non-nil labels expected")
			}

			role, ok := cfg.Labels[mcfgv1.MachineConfigRoleLabelKey]
			if !ok || role == "" {
				t.Fatal("role label missing")
			}

			ign := cfg.Spec.Config
			if role == "master" {
				if !foundPullSecretMaster {
					foundPullSecretMaster = findIgnFile(ign.Storage.Files, "/var/lib/kubelet/config.json", t)
				}
				if !foundKubeletUnitMaster {
					foundKubeletUnitMaster = findIgnUnit(ign.Systemd.Units, "kubelet.service", t)
				}
			} else if role == "worker" {
				if !foundPullSecretWorker {
					foundPullSecretWorker = findIgnFile(ign.Storage.Files, "/var/lib/kubelet/config.json", t)
				}
				if !foundKubeletUnitWorker {
					foundKubeletUnitWorker = findIgnUnit(ign.Systemd.Units, "kubelet.service", t)
				}
			} else {
				t.Fatalf("Unknown role %s", role)
			}
		}

		if !foundPullSecretMaster {
			t.Errorf("Failed to find pull secret for master")
		}
		if !foundKubeletUnitMaster {
			t.Errorf("Failed to find kubelet unit")
		}
		if !foundPullSecretWorker {
			t.Errorf("Failed to find pull secret")
		}
		if !foundKubeletUnitWorker {
			t.Errorf("Failed to find kubelet unit")
		}
	}
}

func controllerConfigFromFile(path string) (*mcfgv1.ControllerConfig, error) {
	data, err := ioutil.ReadFile(path)
	if err != nil {
		return nil, err
	}
	cci, _, err := scheme.Codecs.UniversalDecoder().Decode(data, nil, &mcfgv1.ControllerConfig{})
	if err != nil {
		return nil, fmt.Errorf("unable to decode ControllerConfig manifest: %v", err)
	}
	cc, ok := cci.(*mcfgv1.ControllerConfig)
	if !ok {
		return nil, fmt.Errorf("expected manifest to decode into *mcfgv1.ControllerConfig, got %T", cci)
	}
	return cc, nil
}

func findIgnFile(files []igntypes.File, path string, t *testing.T) bool {
	for _, f := range files {
		if f.Path == path {
			return true
		}
	}
	return false
}

func findIgnUnit(units []igntypes.Unit, name string, t *testing.T) bool {
	for _, u := range units {
		if u.Name == name {
			return true
		}
	}
	return false
}

func verifyIgn(actual [][]byte, dir string, t *testing.T) {

	expected := make(map[string][]byte)
	if err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if path != dir {
				return fmt.Errorf("unexpected dir: %v", err)
			}
			return nil
		}
		data, err := ioutil.ReadFile(path)
		if err != nil {
			return err
		}

		expected[path] = data
		return nil
	}); err != nil {
		t.Fatalf("failed to walk dir: %v", err)
	}

	for _, a := range actual {
		var found bool
		for key, d := range expected {
			if bytes.Equal(d, a) {
				found = true
				delete(expected, key)
			}
		}

		if !found {
			t.Errorf("can't find actual file %v:\n%v", dir, string(a))
		}
	}

	for key := range expected {
		t.Errorf("can't find expected file:\n%v", key)
	}
}

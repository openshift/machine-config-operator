package template

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"testing"

	ignv2_2types "github.com/coreos/ignition/config/v2_2/types"
	"github.com/ghodss/yaml"
	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	"k8s.io/client-go/kubernetes/scheme"
)

var (
	updateGoldenFiles = flag.Bool("u", false, "If set to True, the tests will update the golden files before testing.")
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
		platform: "libvirt",
		res:      "",
	}}
	for idx, c := range cases {
		name := fmt.Sprintf("case #%d", idx)
		t.Run(name, func(t *testing.T) {
			config := &mcfgv1.ControllerConfig{
				Spec: mcfgv1.ControllerConfigSpec{
					Platform: c.platform,
				},
			}
			got, err := renderTemplate(renderConfig{&config.Spec, `{"dummy":"dummy"}`}, name, dummyTemplate)
			if err != nil {
				t.Fatalf("expected nil error %v", err)
			}

			if string(got) != c.res {
				t.Fatalf("mismatch got: %s want: %s", got, c.res)
			}
		})
	}
}

func TestAPIServerURL(t *testing.T) {
	dummyTemplate := []byte(`{{apiServerURL .}}`)

	cases := []struct {
		clusterName string
		baseDomain  string

		url string
		err bool
	}{{
		clusterName: "",
		baseDomain:  "",
		url:         "",
		err:         true,
	}, {
		clusterName: "test-cluster",
		baseDomain:  "",
		url:         "",
		err:         true,
	}, {
		clusterName: "test-cluster",
		baseDomain:  "tt.testing",
		url:         "https://test-cluster-api.tt.testing:6443",
		err:         false,
	}}
	for idx, c := range cases {
		name := fmt.Sprintf("case #%d", idx)
		t.Run(name, func(t *testing.T) {
			config := &mcfgv1.ControllerConfig{
				Spec: mcfgv1.ControllerConfigSpec{
					BaseDomain:  c.baseDomain,
					ClusterName: c.clusterName,
				},
			}
			got, err := renderTemplate(renderConfig{&config.Spec, `{"dummy":"dummy"}`}, name, dummyTemplate)
			if err != nil && !c.err {
				t.Fatalf("expected nil error %v", err)
			}

			if string(got) != c.url {
				t.Fatalf("mismatch got: %s want: %s", got, c.url)
			}
		})
	}
}

func TestEtcdPeerCertDNSNames(t *testing.T) {
	dummyTemplate := []byte(`{{etcdPeerCertDNSNames .}}`)

	cases := []struct {
		clusterName string
		baseDomain  string

		url string
		err bool
	}{{
		clusterName: "",
		baseDomain:  "",
		url:         "",
		err:         true,
	}, {
		clusterName: "my-test-cluster",
		baseDomain:  "",
		url:         "",
		err:         true,
	}, {
		clusterName: "",
		baseDomain:  "tt.testing",
		url:         "",
		err:         true,
	}, {
		clusterName: "my-test-cluster",
		baseDomain:  "tt.testing",
		url:         "${ETCD_DNS_NAME},my-test-cluster.tt.testing",
		err:         false,
	}}
	for idx, c := range cases {
		name := fmt.Sprintf("case #%d", idx)
		t.Run(name, func(t *testing.T) {
			config := &mcfgv1.ControllerConfig{
				Spec: mcfgv1.ControllerConfigSpec{
					ClusterName: c.clusterName,
					BaseDomain:  c.baseDomain,
				},
			}
			got, err := renderTemplate(renderConfig{&config.Spec, `{"dummy":"dummy"}`}, name, dummyTemplate)
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
		baseDomain string

		url string
		err bool
	}{{
		baseDomain: "",
		url:        "",
		err:        true,
	}, {
		baseDomain: "tt.testing",
		url:        "localhost,etcd.kube-system.svc,etcd.kube-system.svc.cluster.local,${ETCD_DNS_NAME}",
		err:        false,
	}}
	for idx, c := range cases {
		name := fmt.Sprintf("case #%d", idx)
		t.Run(name, func(t *testing.T) {
			config := &mcfgv1.ControllerConfig{
				Spec: mcfgv1.ControllerConfigSpec{
					BaseDomain: c.baseDomain,
				},
			}
			got, err := renderTemplate(renderConfig{&config.Spec, `{"dummy":"dummy"}`}, name, dummyTemplate)
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
			got, err := renderTemplate(renderConfig{}, name, tmpl)
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
		"openstack": "./test_data/controller_config_openstack.yaml",
		"libvirt":   "./test_data/controller_config_libvirt.yaml",
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

	controllerConfig.Spec.Platform = "_bad_"
	_, err = generateMachineConfigs(&renderConfig{&controllerConfig.Spec, `{"dummy":"dummy"}`}, templateDir)
	expectErr(err, "failed to create MachineConfig for role master: platform _bad_ unsupported")

	controllerConfig.Spec.Platform = "_base"
	_, err = generateMachineConfigs(&renderConfig{&controllerConfig.Spec, `{"dummy":"dummy"}`}, templateDir)
	expectErr(err, "platform _base unsupported")
}

func TestGenerateMachineConfigs(t *testing.T) {
	for platform, config := range configs {
		controllerConfig, err := controllerConfigFromFile(config)
		if err != nil {
			t.Fatalf("failed to get controllerconfig config: %v", err)
		}

		cfgs, err := generateMachineConfigs(&renderConfig{&controllerConfig.Spec, `{"dummy":"dummy"}`}, templateDir)
		if err != nil {
			t.Fatalf("failed to generate machine configs: %v", err)
		}

		for _, cfg := range cfgs {
			if cfg.Labels == nil {
				t.Fatal("non-nil labels expected")
			}

			role, ok := cfg.Labels[machineConfigRoleLabelKey]
			if !ok || role == "" {
				t.Fatal("role label missing")
			}

			if got, exp := cfg.Name, fmt.Sprintf(machineConfigNameTmpl, role); got != exp {
				t.Fatal("mismatch name found")
			}

			ign := cfg.Spec.Config
			verifyIgnFiles(ign.Storage.Files, filepath.Join(resultDir, platform, role, "files"), *updateGoldenFiles, t)
			verifyIgnUnits(ign.Systemd.Units, filepath.Join(resultDir, platform, role, "units"), *updateGoldenFiles, t)
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

func verifyIgnFiles(files []ignv2_2types.File, dir string, update bool, t *testing.T) {
	var actual [][]byte

	for _, f := range files {
		j, err := json.MarshalIndent(f, "", "  ")
		if err != nil {
			t.Fatalf("failed to marshal file: %v", err)
		}

		data, err := yaml.JSONToYAML(j)
		if err != nil {
			t.Fatalf("failed to convert to yaml: %v", err)
		}

		actual = append(actual, data)

		if update {
			name := strings.Replace(f.Path, "/", "-", -1)
			if err := os.MkdirAll(dir, 0755); err != nil {
				t.Logf("error creating dir %s: %v", dir, err)
			}
			if err := ioutil.WriteFile(filepath.Join(dir, name), data, 0644); err != nil {
				t.Logf("error writing ign unit %s to disk: %v", name, err)
			}
		}
	}

	verifyIgn(actual, dir, t)
}

func verifyIgnUnits(units []ignv2_2types.Unit, dir string, update bool, t *testing.T) {
	var actual [][]byte
	for _, u := range units {
		j, err := json.MarshalIndent(u, "", "  ")
		if err != nil {
			t.Fatalf("failed to marshal file: %v", err)
		}

		data, err := yaml.JSONToYAML(j)
		if err != nil {
			t.Fatalf("failed to convert to yaml: %v", err)
		}

		actual = append(actual, data)

		if update {
			if err := os.MkdirAll(dir, 0755); err != nil {
				t.Logf("error creating dir %s: %v", dir, err)
			}
			if err := ioutil.WriteFile(filepath.Join(dir, u.Name), data, 0644); err != nil {
				t.Logf("error writing ign unit %s to disk: %v", u.Name, err)
			}
		}
	}

	verifyIgn(actual, dir, t)
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

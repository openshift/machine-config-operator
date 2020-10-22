package server

import (
	"context"
	"encoding/base64"
	"fmt"
	"io/ioutil"
	"path"
	"path/filepath"
	"strings"
	"testing"

	"github.com/coreos/go-semver/semver"
	ign3types "github.com/coreos/ignition/v2/config/v3_1/types"
	yaml "github.com/ghodss/yaml"
	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	daemonconsts "github.com/openshift/machine-config-operator/pkg/daemon/constants"
	"github.com/openshift/machine-config-operator/pkg/generated/clientset/versioned/fake"
)

const (
	testPool   = "test-pool"
	testConfig = "test-config"
	testDir    = "./testdata"
)

var (
	testKubeConfig = fmt.Sprintf("%s/kubeconfig", testDir)
)

func TestStringDecode(t *testing.T) {
	inp := "data:,Hello%2C%20world!"
	exp := "Hello, world!"
	dec, err := getDecodedContent(inp)
	if err != nil {
		t.Errorf("expected error to be nil, received: %v", err)
	}
	if exp != dec {
		t.Errorf("string decode failed. exp: %s, got: %s", exp, dec)
	}
}

func TestStringEncode(t *testing.T) {
	inp := "Hello, world!"
	exp := "data:,Hello%2C%20world!"
	enc := getEncodedContent(inp)
	if exp != enc {
		t.Errorf("string encode failed. exp: %s, got: %s", exp, enc)
	}
}

func TestEncapsulated(t *testing.T) {
	mcPath := filepath.Join(testDir, "machine-configs", testConfig+".yaml")
	mcData, err := ioutil.ReadFile(mcPath)
	assert.Nil(t, err)
	mc := new(mcfgv1.MachineConfig)
	err = yaml.Unmarshal([]byte(mcData), mc)
	assert.Nil(t, err)
	mcIgnCfg, err := ctrlcommon.ParseAndConvertConfig(mc.Spec.Config.Raw)
	if err != nil {
		t.Errorf("decoding Ignition Config failed: %s", err)
	}
	assert.Equal(t, 1, len(mcIgnCfg.Storage.Files))
	assert.Equal(t, mcIgnCfg.Storage.Files[0].Path, "/etc/coreos/update.conf")

	origMc := mc.DeepCopy()
	origIgnCfg, err := ctrlcommon.ParseAndConvertConfig(origMc.Spec.Config.Raw)
	if err != nil {
		t.Errorf("decoding Ignition Config failed: %s", err)
	}
	// Pass a nil config to test the fallback path
	err = appendEncapsulated(&mcIgnCfg, mc, nil)
	if err != nil {
		t.Errorf("converting MachineConfig to raw Ignition config failed: %s", err)
	}
	assert.Equal(t, 1, len(origIgnCfg.Storage.Files))
	assert.Equal(t, 2, len(mcIgnCfg.Storage.Files))
	assert.Equal(t, mcIgnCfg.Storage.Files[0].Path, "/etc/coreos/update.conf")
	assert.Equal(t, mcIgnCfg.Storage.Files[1].Path, daemonconsts.MachineConfigEncapsulatedPath)

	vers := []*semver.Version{semver.New("3.1.0"), semver.New("2.2.0")}
	for _, v := range vers {
		mcIgnCfg, err = ctrlcommon.ParseAndConvertConfig(mc.Spec.Config.Raw)
		assert.Nil(t, err)
		err = appendEncapsulated(&mcIgnCfg, mc, v)
		if err != nil {
			t.Errorf("converting MachineConfig to raw Ignition config failed: %s", err)
		}
		assert.Equal(t, 1, len(origIgnCfg.Storage.Files))
		assert.Equal(t, 2, len(mcIgnCfg.Storage.Files))
		assert.Equal(t, mcIgnCfg.Storage.Files[0].Path, "/etc/coreos/update.conf")
		assert.Equal(t, mcIgnCfg.Storage.Files[1].Path, daemonconsts.MachineConfigEncapsulatedPath)
	}
}

// TestBootstrapServer tests the behavior of the machine config server
// when it's running in bootstrap mode.
// The test does the following:
//
// 1. Fetch the MachineConfigPool from the testdata.
// 2. Fetch the MachineConfig from the testdata.
// 3. Manually update the ignition config from Step 2 by adding
//    the NodeAnnotations file, the kubeconfig file (which is read
//    from the testdata). This Ignition config is then
//    labeled as expected Ignition config.
// 4. Call the Bootstrap GetConfig method by passing the reference to the
//    MachineConfigPool present in the testdata folder.
// 5. Compare the Ignition configs from Step 3 and Step 4.
func TestBootstrapServer(t *testing.T) {
	mp, err := getTestMachineConfigPool()
	if err != nil {
		t.Fatal(err)
	}

	mcPath := filepath.Join(testDir, "machine-configs", testConfig+".yaml")
	mcData, err := ioutil.ReadFile(mcPath)
	if err != nil {
		t.Fatalf("unexpected error while reading machine-config: %s, err: %v", mcPath, err)
	}

	mc := new(mcfgv1.MachineConfig)
	err = yaml.Unmarshal([]byte(mcData), mc)
	if err != nil {
		t.Fatalf("unexpected error while unmarshaling machine-config: %s, err: %v", mcPath, err)
	}
	mcIgnCfg, err := ctrlcommon.ParseAndConvertConfig(mc.Spec.Config.Raw)
	if err != nil {
		t.Errorf("decoding Ignition Config failed: %s", err)
	}

	// add new files, units in the expected Ignition.
	kc, _, err := getKubeConfigContent(t)
	if err != nil {
		t.Fatal(err)
	}
	err = appendFileToIgnition(&mcIgnCfg, defaultMachineKubeConfPath, string(kc))
	if err != nil {
		t.Fatalf("unexpected error while appending file to ignition: %v", err)
	}
	anno, err := getNodeAnnotation(mp.Status.Configuration.Name)
	if err != nil {
		t.Fatalf("unexpected error while creating annotations err: %v", err)
	}
	err = appendFileToIgnition(&mcIgnCfg, daemonconsts.InitialNodeAnnotationsFilePath, anno)
	if err != nil {
		t.Fatalf("unexpected error while appending file to ignition: %v", err)
	}

	// initialize bootstrap server and get config.
	bs := &bootstrapServer{
		serverBaseDir:  testDir,
		kubeconfigFunc: func() ([]byte, []byte, error) { return getKubeConfigContent(t) },
	}
	if err != nil {
		t.Fatal(err)
	}
	res, err := bs.GetConfig(poolRequest{
		machineConfigPool: "master",
	})
	if err != nil {
		t.Fatalf("expected err to be nil, received: %v", err)
	}

	// assert on the output.
	ignCfg, err := ctrlcommon.ParseAndConvertConfig(mc.Spec.Config.Raw)
	if err != nil {
		t.Fatal(err)
	}
	resCfg, err := ctrlcommon.ParseAndConvertConfig(res.Raw)
	if err != nil {
		t.Fatal(err)
	}
	validateIgnitionFiles(t, ignCfg.Storage.Files, resCfg.Storage.Files)
	validateIgnitionSystemd(t, ignCfg.Systemd.Units, resCfg.Systemd.Units)

	// verify bootstrap cannot serve ignition to other pool than master
	res, err = bs.GetConfig(poolRequest{
		machineConfigPool: testPool,
	})
	if err == nil {
		t.Fatalf("expected bootstrap server to not serve ignition to non-master pools")
	}
}

// TestClusterServer tests the behavior of the machine config server
// when it's running within the cluster.
// The test does the following:
//
// 1. Fetch the MachineConfigPool from the testdata.
// 2. Fetch the MachineConfig from the testdata, call this origMC.
// 3. Manually update the ignition config from Step 2 by adding
//    the NodeAnnotations file, the kubeconfig file (which is read
//    from the testdata). This Ignition config is then
//    labeled as expected Ignition config (mc).
// 4. Use the Kubernetes fake client to Create the MachineConfigPool
//    and the MachineConfig objects from Step 1, 2 inside the cluster.
// 5. Call the Cluster GetConfig method.
// 6. Compare the Ignition configs from Step 3 and Step 5.
func TestClusterServer(t *testing.T) {
	mp, err := getTestMachineConfigPool()
	if err != nil {
		t.Fatal(err)
	}

	mcPath := filepath.Join(testDir, "machine-configs", testConfig+".yaml")
	mcData, err := ioutil.ReadFile(mcPath)
	if err != nil {
		t.Fatalf("unexpected error while reading machine-config: %s, err: %v", mcPath, err)
	}
	origMC := new(mcfgv1.MachineConfig)
	err = yaml.Unmarshal([]byte(mcData), origMC)
	if err != nil {
		t.Fatalf("unexpected error while unmarshaling machine-config: %s, err: %v", mcPath, err)
	}

	cs := fake.NewSimpleClientset()
	_, err = cs.MachineconfigurationV1().MachineConfigPools().Create(context.TODO(), mp, metav1.CreateOptions{})
	if err != nil {
		t.Logf("err: %v", err)
	}
	_, err = cs.MachineconfigurationV1().MachineConfigs().Create(context.TODO(), origMC, metav1.CreateOptions{})
	if err != nil {
		t.Logf("err: %v", err)
	}

	csc := &clusterServer{
		machineClient:  cs.MachineconfigurationV1(),
		kubeconfigFunc: func() ([]byte, []byte, error) { return getKubeConfigContent(t) },
	}

	mc := new(mcfgv1.MachineConfig)
	err = yaml.Unmarshal([]byte(mcData), mc)
	if err != nil {
		t.Fatalf("unexpected error while unmarshaling machine-config: %s, err: %v", mcPath, err)
	}
	mcIgnCfg, err := ctrlcommon.ParseAndConvertConfig(mc.Spec.Config.Raw)
	if err != nil {
		t.Errorf("decoding Ignition Config failed: %s", err)
	}

	kc, _, err := getKubeConfigContent(t)
	if err != nil {
		t.Fatal(err)
	}
	err = appendFileToIgnition(&mcIgnCfg, defaultMachineKubeConfPath, string(kc))
	if err != nil {
		t.Fatalf("unexpected error while appending file to ignition: %v", err)
	}
	anno, err := getNodeAnnotation(mp.Status.Configuration.Name)
	if err != nil {
		t.Fatalf("unexpected error while creating annotations err: %v", err)
	}
	err = appendFileToIgnition(&mcIgnCfg, daemonconsts.InitialNodeAnnotationsFilePath, anno)
	if err != nil {
		t.Fatalf("unexpected error while appending file to ignition: %v", err)
	}

	res, err := csc.GetConfig(poolRequest{
		machineConfigPool: testPool,
	})
	if err != nil {
		t.Fatalf("expected err to be nil, received: %v", err)
	}

	ignCfg, err := ctrlcommon.ParseAndConvertConfig(mc.Spec.Config.Raw)
	if err != nil {
		t.Fatal(err)
	}
	resCfg, err := ctrlcommon.ParseAndConvertConfig(res.Raw)
	if err != nil {
		t.Fatal(err)
	}
	validateIgnitionFiles(t, ignCfg.Storage.Files, resCfg.Storage.Files)
	validateIgnitionSystemd(t, ignCfg.Systemd.Units, resCfg.Systemd.Units)

	foundEncapsulated := false
	for _, f := range resCfg.Storage.Files {
		if f.Path != daemonconsts.MachineConfigEncapsulatedPath {
			continue
		}
		foundEncapsulated = true
		encapMc := new(mcfgv1.MachineConfig)
		contents, err := getDecodedContent(*f.Contents.Source)
		assert.Nil(t, err)
		err = yaml.Unmarshal([]byte(contents), encapMc)
		assert.Nil(t, err)
		assert.Equal(t, encapMc.Spec.KernelArguments, mc.Spec.KernelArguments)
		assert.Equal(t, encapMc.Spec.OSImageURL, mc.Spec.OSImageURL)
	}
	if !foundEncapsulated {
		t.Errorf("missing %s", daemonconsts.MachineConfigEncapsulatedPath)
	}
}

func getKubeConfigContent(t *testing.T) ([]byte, []byte, error) {
	return []byte("dummy-kubeconfig"), []byte("dummy-root-ca"), nil
}

func validateIgnitionFiles(t *testing.T, exp, got []ign3types.File) {
	expMap := createFileMap(exp)
	gotMap := createFileMap(got)

	encapsulatedKey := "root" + daemonconsts.MachineConfigEncapsulatedPath
	for k, v := range expMap {
		// This special value is injected
		if k == encapsulatedKey {
			continue
		}
		f, ok := gotMap[k]
		if !ok {
			t.Errorf("could not find file: %s", k)
			continue
		}
		assert.Equal(t, v, f)
	}

}

func validateIgnitionSystemd(t *testing.T, exp, got []ign3types.Unit) {
	expMap := createUnitMap(exp)
	gotMap := createUnitMap(got)

	for k, v := range expMap {
		f, ok := gotMap[k]
		if !ok {
			t.Errorf("could not find unit: %s", k)
			continue
		}
		assert.Equal(t, v, f)
	}
}

func createUnitMap(units []ign3types.Unit) map[string]ign3types.Unit {
	m := make(map[string]ign3types.Unit)
	for i := range units {
		m[units[i].Name] = units[i]
	}
	return m
}

func createFileMap(files []ign3types.File) map[string]ign3types.File {
	m := make(map[string]ign3types.File)
	for i := range files {
		file := files[i].Path
		m[file] = files[i]
	}
	return m
}

func getTestMachineConfigPool() (*mcfgv1.MachineConfigPool, error) {
	mpPath := path.Join(testDir, "machine-pools", testPool+".yaml")
	mpData, err := ioutil.ReadFile(mpPath)
	if err != nil {
		return nil, fmt.Errorf("unexpected error while reading machine-pool: %s, err: %v", mpPath, err)
	}
	mp := new(mcfgv1.MachineConfigPool)
	err = yaml.Unmarshal(mpData, mp)
	if err != nil {
		return nil, fmt.Errorf("unexpected error while unmarshaling machine-pool: %s, err: %v", mpPath, err)
	}
	return mp, nil
}

func TestKubeconfigFromSecret(t *testing.T) {
	tests := []struct {
		SecretPath string
		TestURL    string
		Error      bool
	}{{
		SecretPath: testDir,
		TestURL:    "api.tt.testing",
	}, {
		SecretPath: "BADPATH",
		TestURL:    "api.tt.testing",
		Error:      true,
	}, {
		SecretPath: testDir,
		Error:      true,
	}}
	for idx, test := range tests {
		t.Run(fmt.Sprintf("case#%d", idx), func(t *testing.T) {
			desc := fmt.Sprintf("SecretPath(%#v), TestAPIUrl(%#v)", test.SecretPath, test.TestURL)
			kc, ca, err := kubeconfigFromSecret(test.SecretPath, test.TestURL)
			if err != nil {
				if !test.Error {
					t.Fatalf("%s failed %s", desc, err.Error())
				} else {
					return
				}
			}

			if kc == nil {
				t.Fatalf("Generated kubeconfig is nil")
			}

			if ca == nil {
				t.Fatalf("Certificate data is nil")
			}

			str := fmt.Sprintf("%s", kc)
			if str == "" || len(str) == 0 {
				t.Fatalf("Error converting kubeconfig yaml to string")
			}

			mockCAData, err := ioutil.ReadFile(test.SecretPath + "/" + corev1.ServiceAccountRootCAKey)
			if err != nil {
				t.Fatalf("Error reading test certificate file: %s", err.Error())
			}

			if !strings.Contains(str, base64.StdEncoding.EncodeToString(mockCAData)) {
				t.Fatalf("Kubeconfig does not contain b64 encoded ca data")
			}

			if !strings.Contains(str, test.TestURL) {
				t.Fatalf("Kubeconfig does not contain server API URL")
			}
		})
	}
}

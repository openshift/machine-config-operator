package server

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"

	"github.com/coreos/go-semver/semver"
	ign2 "github.com/coreos/ignition/config/v2_2"
	ign3 "github.com/coreos/ignition/v2/config/v3_5"
	ign3types "github.com/coreos/ignition/v2/config/v3_5/types"
	yaml "github.com/ghodss/yaml"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	daemonconsts "github.com/openshift/machine-config-operator/pkg/daemon/constants"
)

const (
	testPool   = "test-pool"
	testConfig = "test-config"
	testDir    = "./testdata"
)

var (
	testKubeConfig = fmt.Sprintf("%s/kubeconfig", testDir)
)

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
	mcData, err := os.ReadFile(mcPath)
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

	vers := []*semver.Version{semver.New("3.4.0"), semver.New("3.3.0"), semver.New("3.2.0"), semver.New("3.1.0"), semver.New("2.2.0")}
	t.Logf("vers: %v\n", vers)
	for _, v := range vers {
		major := v.Slice()[0]
		mcIgnCfg, err = ctrlcommon.ParseAndConvertConfig(mc.Spec.Config.Raw)
		assert.Nil(t, err)
		err = appendEncapsulated(&mcIgnCfg, mc, v)
		if err != nil {
			t.Errorf("converting MachineConfig to raw Ignition config failed: %s", err)
		}
		assert.Equal(t, 1, len(origIgnCfg.Storage.Files))
		assert.Equal(t, 2, len(mcIgnCfg.Storage.Files))
		assert.Equal(t, mcIgnCfg.Storage.Files[0].Path, "/etc/coreos/update.conf")
		encapF := &mcIgnCfg.Storage.Files[1]
		assert.Equal(t, encapF.Path, daemonconsts.MachineConfigEncapsulatedPath)
		encapSource := *encapF.Contents.Source
		dataPrefix := "data:,"
		assert.True(t, strings.HasPrefix(encapSource, dataPrefix))
		encapEncoded := encapSource[len(dataPrefix):]
		encapDataStr, err := url.PathUnescape(encapEncoded)
		assert.Nil(t, err)
		t.Logf("encapData: %s", encapDataStr)
		encapData := []byte(encapDataStr)
		var mc mcfgv1.MachineConfig
		err = json.Unmarshal(encapData, &mc)
		assert.Nil(t, err)
		// TODO(jkyros): the encap only supplies what the current internal version is, and 3.1 was able to parse a 3.2 config
		// because it's weird so we should probably revisit whether it's okay if the encap is now supplying 3.4
		if major == 3 {
			_, _, err := ign3.Parse(mc.Spec.Config.Raw)
			assert.Nil(t, err)
		} else {
			_, _, err := ign2.Parse(mc.Spec.Config.Raw)
			assert.Nil(t, err)
		}
	}
}

// TestBootstrapServer tests the behavior of the machine config server
// when it's running in bootstrap mode.
// The test does the following:
//
//  1. Fetch the MachineConfigPool from the testdata.
//  2. Fetch the MachineConfig from the testdata.
//  3. Manually update the ignition config from Step 2 by adding
//     the NodeAnnotations file, the kubeconfig file (which is read
//     from the testdata). This Ignition config is then
//     labeled as expected Ignition config.
//  4. Call the Bootstrap GetConfig method by passing the reference to the
//     MachineConfigPool present in the testdata folder.
//  5. Compare the Ignition configs from Step 3 and Step 4.
func TestBootstrapServer(t *testing.T) {
	mp, err := getTestMachineConfigPool()
	if err != nil {
		t.Fatal(err)
	}

	mcPath := filepath.Join(testDir, "machine-configs", testConfig+".yaml")
	mcData, err := os.ReadFile(mcPath)
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

	bytes := []byte("testing")
	err = os.WriteFile(filepath.Join(testDir, "bar.crt"), bytes, 0o664)
	require.Nil(t, err)
	// initialize bootstrap server and get config.
	bs := &bootstrapServer{
		serverBaseDir:  testDir,
		kubeconfigFunc: func() ([]byte, []byte, error) { return getKubeConfigContent(t) },
		certs:          []string{"foo=bar.crt"},
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
	foundCertFiles := false
	for _, file := range resCfg.Storage.Files {
		if strings.Contains(file.Path, filepath.Join("/etc/docker/certs.d", "foo")) {
			foundCertFiles = true
		}
	}
	require.True(t, foundCertFiles)
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

type mockMCPLister struct {
	pools []*mcfgv1.MachineConfigPool
}

func (mcpl *mockMCPLister) List(selector labels.Selector) (ret []*mcfgv1.MachineConfigPool, err error) {
	return mcpl.pools, nil
}
func (mcpl *mockMCPLister) Get(name string) (ret *mcfgv1.MachineConfigPool, err error) {
	if mcpl.pools == nil {
		return nil, nil
	}
	for _, pool := range mcpl.pools {
		if pool.Name == name {
			return pool, nil
		}

	}
	return nil, nil
}

type mockMCLister struct {
	configs []*mcfgv1.MachineConfig
}

func (mcpl *mockMCLister) List(selector labels.Selector) (ret []*mcfgv1.MachineConfig, err error) {
	return mcpl.configs, nil
}
func (mcpl *mockMCLister) Get(name string) (ret *mcfgv1.MachineConfig, err error) {
	if mcpl.configs == nil {
		return nil, nil
	}
	for _, pool := range mcpl.configs {
		if pool.Name == name {
			return pool, nil
		}

	}
	return nil, nil
}

type mockCCLister struct {
	configs []*mcfgv1.ControllerConfig
}

func (mcpl *mockCCLister) List(selector labels.Selector) (ret []*mcfgv1.ControllerConfig, err error) {
	return mcpl.configs, nil
}
func (mcpl *mockCCLister) Get(name string) (ret *mcfgv1.ControllerConfig, err error) {
	if mcpl.configs == nil {
		return nil, nil
	}
	for _, config := range mcpl.configs {
		if config.Name == name {
			return config, nil
		}

	}
	return nil, nil
}

// TestClusterServer tests the behavior of the machine config server
// when it's running within the cluster.
// The test does the following:
//
//  1. Fetch the MachineConfigPool from the testdata.
//  2. Fetch the MachineConfig from the testdata, call this origMC.
//  3. Manually update the ignition config from Step 2 by adding
//     the NodeAnnotations file, the kubeconfig file (which is read
//     from the testdata). This Ignition config is then
//     labeled as expected Ignition config (mc).
//  4. Use the Kubernetes fake client to Create the MachineConfigPool
//     and the MachineConfig objects from Step 1, 2 inside the cluster.
//  5. Call the Cluster GetConfig method.
//  6. Compare the Ignition configs from Step 3 and Step 5.
func TestClusterServer(t *testing.T) {
	mp, err := getTestMachineConfigPool()
	if err != nil {
		t.Fatal(err)
	}

	mcPath := filepath.Join(testDir, "machine-configs", testConfig+".yaml")
	mcData, err := os.ReadFile(mcPath)
	if err != nil {
		t.Fatalf("unexpected error while reading machine-config: %s, err: %v", mcPath, err)
	}
	origMC := new(mcfgv1.MachineConfig)
	err = yaml.Unmarshal([]byte(mcData), origMC)
	if err != nil {
		t.Fatalf("unexpected error while unmarshaling machine-config: %s, err: %v", mcPath, err)
	}

	controllerConfig := getTestControllerConfig()

	mcpLister := &mockMCPLister{
		pools: []*mcfgv1.MachineConfigPool{
			mp,
		},
	}

	mcLister := &mockMCLister{
		configs: []*mcfgv1.MachineConfig{
			origMC,
		},
	}

	ccLister := &mockCCLister{
		configs: []*mcfgv1.ControllerConfig{
			controllerConfig,
		},
	}

	// machineClient:  cs.MachineconfigurationV1(),
	csc := &clusterServer{
		machineConfigPoolLister: mcpLister,
		machineConfigLister:     mcLister,
		controllerConfigLister:  ccLister,
		kubeconfigFunc: func() ([]byte, []byte, error) {
			return getKubeConfigContent(t)
		},
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
		contents, err := ctrlcommon.DecodeIgnitionFileContents(f.Contents.Source, f.Contents.Compression)
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
	mpData, err := os.ReadFile(mpPath)
	if err != nil {
		return nil, fmt.Errorf("unexpected error while reading machine-pool: %s, err: %w", mpPath, err)
	}
	mp := new(mcfgv1.MachineConfigPool)
	err = yaml.Unmarshal(mpData, mp)
	if err != nil {
		return nil, fmt.Errorf("unexpected error while unmarshaling machine-pool: %s, err: %w", mpPath, err)
	}
	return mp, nil
}

func getTestControllerConfig() *mcfgv1.ControllerConfig {
	return &mcfgv1.ControllerConfig{
		ObjectMeta: metav1.ObjectMeta{Generation: 1, Name: "machine-config-controller"},
		Spec: mcfgv1.ControllerConfigSpec{
			KubeAPIServerServingCAData: []byte("Testdata"),
		},
	}
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
			kc, ca, err := kubeconfigFromSecret(test.SecretPath, test.TestURL, nil)
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

			mockCAData, err := os.ReadFile(test.SecretPath + "/" + corev1.ServiceAccountRootCAKey)
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

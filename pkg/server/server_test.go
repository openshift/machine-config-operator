package server

import (
	"fmt"
	"io/ioutil"
	"path"
	"path/filepath"
	"reflect"
	"testing"

	ignv2_2types "github.com/coreos/ignition/config/v2_2/types"
	yaml "github.com/ghodss/yaml"
	"github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	"github.com/openshift/machine-config-operator/pkg/daemon"
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

// TestBootstrapServer tests the behavior of the machine config server
// when it's running in bootstrap mode.
// The test does the following:
//
// 1. Fetch the machine-pool from the testdata.
// 2. Fetch the machine-config from the testdata.
// 3. Manually update the ignition config from Step 2 by adding
//    the node-annotations file, the kubeconfig file(which is read
//    from the testdata). This ignition config is then
//    labeled as expected Ignition config.
// 4. Call the Bootstrap getConfig method by passing the reference to the
//    machine pool present in the testdata folder.
// 5. Compare the Ignition configs from Step 3 and Step 4.
func TestBootstrapServer(t *testing.T) {
	mp, err := getTestMachinePool()
	if err != nil {
		t.Fatal(err)
	}

	mcPath := filepath.Join(testDir, "machine-configs", testConfig+".yaml")
	mcData, err := ioutil.ReadFile(mcPath)
	if err != nil {
		t.Fatalf("unexpected error while reading machine-config: %s, err: %v", mcPath, err)
	}

	mc := new(v1.MachineConfig)
	err = yaml.Unmarshal([]byte(mcData), mc)
	if err != nil {
		t.Fatalf("unexpected error while unmarshaling machine-config: %s, err: %v", mcPath, err)
	}

	// add new files, units in the expected Ignition.
	kc, _, err := getKubeConfigContent(t)
	if err != nil {
		t.Fatal(err)
	}
	appendFileToIgnition(&mc.Spec.Config, defaultMachineKubeConfPath, string(kc))
	anno, err := getNodeAnnotation(mp.Status.CurrentMachineConfig)
	if err != nil {
		t.Fatalf("unexpected error while creating annotations err: %v", err)
	}
	appendFileToIgnition(&mc.Spec.Config, daemon.InitialNodeAnnotationsFilePath, anno)

	// initialize bootstrap server and get config.
	bs := &bootstrapConfig{
		configBaseDir:  testDir,
		kubeconfigFunc: func() ([]byte, []byte, error) { return getKubeConfigContent(t) },
	}
	if err != nil {
		t.Fatal(err)
	}
	res, err := bs.getConfig(poolRequest{
		machinePool: testPool,
	})
	if err != nil {
		t.Fatalf("expected err to be nil, received: %v", err)
	}

	// assert on the output.
	validateIgnitionFiles(t, res.Storage.Files, mc.Spec.Config.Storage.Files)
	validateIgnitionSystemd(t, res.Systemd.Units, mc.Spec.Config.Systemd.Units)
}

// TestClusterServer tests the behavior of the machine config server
// when it's running within the cluster.
// The test does the following:
//
// 1. Fetch the machine-pool from the testdata.
// 2. Fetch the machine-config from the testdata, call this origMC.
// 3. Manually update the ignition config from Step 2 by adding
//    the node-annotations file, the kubeconfig file(which is read
//    from the testdata). This ignition config is then
//    labeled as expected Ignition config (mc).
// 4. Use the Kubernetes fake client to Create the machine pool and the config
//    objects from Step 1, 2 inside the cluster.
// 5. Call the Cluster getConfig method.
// 6. Compare the Ignition configs from Step 3 and Step 5.
func TestClusterServer(t *testing.T) {
	mp, err := getTestMachinePool()
	if err != nil {
		t.Fatal(err)
	}

	mcPath := filepath.Join(testDir, "machine-configs", testConfig+".yaml")
	mcData, err := ioutil.ReadFile(mcPath)
	if err != nil {
		t.Fatalf("unexpected error while reading machine-config: %s, err: %v", mcPath, err)
	}
	origMC := new(v1.MachineConfig)
	err = yaml.Unmarshal([]byte(mcData), origMC)
	if err != nil {
		t.Fatalf("unexpected error while unmarshaling machine-config: %s, err: %v", mcPath, err)
	}

	cs := fake.NewSimpleClientset()
	_, err = cs.MachineconfigurationV1().MachineConfigPools().Create(mp)
	if err != nil {
		t.Logf("err: %v", err)
	}
	_, err = cs.MachineconfigurationV1().MachineConfigs().Create(origMC)
	if err != nil {
		t.Logf("err: %v", err)
	}

	csc := &clusterConfig{
		machineClient:  cs.MachineconfigurationV1(),
		kubeconfigFunc: func() ([]byte, []byte, error) { return getKubeConfigContent(t) },
	}

	mc := new(v1.MachineConfig)
	err = yaml.Unmarshal([]byte(mcData), mc)
	if err != nil {
		t.Fatalf("unexpected error while unmarshaling machine-config: %s, err: %v", mcPath, err)
	}

	kc, _, err := getKubeConfigContent(t)
	if err != nil {
		t.Fatal(err)
	}
	appendFileToIgnition(&mc.Spec.Config, defaultMachineKubeConfPath, string(kc))
	anno, err := getNodeAnnotation(mp.Status.CurrentMachineConfig)
	if err != nil {
		t.Fatalf("unexpected error while creating annotations err: %v", err)
	}
	appendFileToIgnition(&mc.Spec.Config, daemon.InitialNodeAnnotationsFilePath, anno)

	res, err := csc.getConfig(poolRequest{
		machinePool: testPool,
	})
	if err != nil {
		t.Fatalf("expected err to be nil, received: %v", err)
	}

	validateIgnitionFiles(t, res.Storage.Files, mc.Spec.Config.Storage.Files)
	validateIgnitionSystemd(t, res.Systemd.Units, mc.Spec.Config.Systemd.Units)
}

func getKubeConfigContent(t *testing.T) ([]byte, []byte, error) {
	return []byte("dummy-kubeconfig"), []byte("dummy-root-ca"), nil
}

func validateIgnitionFiles(t *testing.T, exp, got []ignv2_2types.File) {
	expMap := createFileMap(exp)
	gotMap := createFileMap(got)

	for k, v := range expMap {
		f, ok := gotMap[k]
		if !ok {
			t.Errorf("could not find file: %s", k)
		}
		if !reflect.DeepEqual(v, f) {
			t.Errorf("file validation failed for: %s, exp: %v, got: %v", k, v, f)
		}
	}
}

func validateIgnitionSystemd(t *testing.T, exp, got []ignv2_2types.Unit) {
	expMap := createUnitMap(exp)
	gotMap := createUnitMap(got)

	for k, v := range expMap {
		f, ok := gotMap[k]
		if !ok {
			t.Errorf("could not find file: %s", k)
		}
		if !reflect.DeepEqual(v, f) {
			t.Errorf("file validation failed for: %s, exp: %v, got: %v", k, v, f)
		}
	}
}

func createUnitMap(units []ignv2_2types.Unit) map[string]ignv2_2types.Unit {
	m := make(map[string]ignv2_2types.Unit)
	for i := range units {
		m[units[i].Name] = units[i]
	}
	return m
}

func createFileMap(files []ignv2_2types.File) map[string]ignv2_2types.File {
	m := make(map[string]ignv2_2types.File)
	for i := range files {
		file := path.Join(files[i].Filesystem, files[i].Path)
		m[file] = files[i]
	}
	return m
}

func getTestMachinePool() (*v1.MachineConfigPool, error) {
	mpPath := path.Join(testDir, "machine-pools", testPool+".yaml")
	mpData, err := ioutil.ReadFile(mpPath)
	if err != nil {
		return nil, fmt.Errorf("unexpected error while reading machine-pool: %s, err: %v", mpPath, err)
	}
	mp := new(v1.MachineConfigPool)
	err = yaml.Unmarshal(mpData, mp)
	if err != nil {
		return nil, fmt.Errorf("unexpected error while unmarshaling machine-pool: %s, err: %v", mpPath, err)
	}
	return mp, nil
}

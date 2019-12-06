package main

import (
	"fmt"
	"io/ioutil"
	"os"
	"reflect"
	"testing"

	"github.com/joho/godotenv"
)

type testConfig struct {
	t            *testing.T
	etcdName     string
	discoverySRV string
	dr           bool
	inCluster    bool
	bootstrapSRV bool

	Want map[string]string
}

func TestSetupEnvSRVWithDR(t *testing.T) {
	config := &testConfig{
		t:            t,
		etcdName:     "etcd-test",
		discoverySRV: "testcluster.openshift.com",
		dr:           true,
		inCluster:    false,
		bootstrapSRV: true,
	}
	config.Want = map[string]string{
		"ETCD_IPV4_ADDRESS":      testIPAddress,
		"ETCD_DNS_NAME":          fmt.Sprintf("%s.%s", config.etcdName, config.discoverySRV),
		"ETCD_INITIAL_CLUSTER":   fmt.Sprintf("%s=%s.%s", config.etcdName, config.etcdName, config.discoverySRV),
		"ETCD_WILDCARD_DNS_NAME": fmt.Sprintf("*.%s", config.discoverySRV),
	}
	testSetupEnv(config)
}

func TestSetupEnvSRV(t *testing.T) {
	config := &testConfig{
		t:            t,
		etcdName:     "etcd-test",
		discoverySRV: "testcluster.openshift.com",
		dr:           false,
		inCluster:    false,
		bootstrapSRV: true,
	}
	config.Want = map[string]string{
		"ETCD_DISCOVERY_SRV":     config.discoverySRV,
		"ETCD_IPV4_ADDRESS":      testIPAddress,
		"ETCD_DNS_NAME":          fmt.Sprintf("%s.%s", config.etcdName, config.discoverySRV),
		"ETCD_WILDCARD_DNS_NAME": fmt.Sprintf("*.%s", config.discoverySRV),
	}
	testSetupEnv(config)
}

func TestSetupEnvSRVInCluster(t *testing.T) {
	config := &testConfig{
		t:            t,
		etcdName:     "etcd-test",
		discoverySRV: "testcluster.openshift.com",
		dr:           false,
		inCluster:    true,
		bootstrapSRV: true,
	}
	config.Want = map[string]string{
		"ETCD_DISCOVERY_SRV":     config.discoverySRV,
		"ETCD_IPV4_ADDRESS":      testIPAddress,
		"ETCD_DNS_NAME":          fmt.Sprintf("%s.%s", config.etcdName, config.discoverySRV),
		"ETCD_WILDCARD_DNS_NAME": fmt.Sprintf("*.%s", config.discoverySRV),
	}
	testSetupEnv(config)
}

func testSetupEnv(tc *testConfig) {
	previousFile, err := ioutil.TempFile("/tmp", "run-etcd-environment.*")
	if err != nil {
		tc.t.Fatal(err)
	}

	// set ENV which emulates inCuster
	if tc.inCluster {
		os.Setenv("KUBERNETES_SERVICE_HOST", "kubernetes.default")
		os.Setenv("KUBERNETES_SERVICE_PORT", "2379")
		defer os.Unsetenv("KUBERNETES_SERVICE_HOST")
		defer os.Unsetenv("KUBERNETES_SERVICE_PORT")
	}

	runOpts := &opts{
		discoverySRV: tc.discoverySRV,
		ifName:       "",
		outputFile:   previousFile.Name(),
		bootstrapSRV: tc.bootstrapSRV,
	}

	setupEnv, err := newSetupEnv(runOpts, tc.etcdName, "./tmp", []string{testIPAddress})
	if err != nil {
		tc.t.Fatalf("testSetupEnv() failed: %s", err)
	}

	if tc.dr {
		// set up a populated Env emulating an DR senario
		existingExportEnv := map[string]string{
			"INITIAL_CLUSTER": fmt.Sprintf("%s=%s", tc.etcdName, setupEnv.etcdDNS),
			"DISCOVERY_SRV":   setupEnv.opts.discoverySRV,
		}
		if err := writeEnvironmentFile(existingExportEnv, previousFile, true); err != nil {
			tc.t.Fatal(err)
		}

		existingUnexportedEnv := map[string]string{
			"IPV4_ADDRESS":      setupEnv.etcdIP,
			"DNS_NAME":          setupEnv.etcdDNS,
			"WILDCARD_DNS_NAME": fmt.Sprintf("*.%s", setupEnv.opts.discoverySRV),
		}
		if err := writeEnvironmentFile(existingUnexportedEnv, previousFile, true); err != nil {
			tc.t.Fatalf("testSetupEnv() failed: %s", err)
		}
	}

	// Close existingFile now that it is populated/parsed
	if err := previousFile.Close(); err != nil {
		tc.t.Fatal(err)
	}

	// runs against previousFile
	exportEnv, err := setupEnv.getExportEnv()
	if err != nil {
		tc.t.Fatalf("testSetupEnv() failed: %s", err)
	}

	currentFile, err := os.Create(setupEnv.opts.outputFile)
	if err != nil {
		tc.t.Fatalf("testSetupEnv() failed: %s", err)
	}

	// cleanup
	defer currentFile.Close()
	defer os.Remove(setupEnv.opts.outputFile)

	if err := writeEnvironmentFile(exportEnv, currentFile, true); err != nil {
		tc.t.Fatalf("testSetupEnv() failed: %s", err)
	}

	nonExportEnv := map[string]string{
		"IPV4_ADDRESS":      setupEnv.etcdIP,
		"DNS_NAME":          setupEnv.etcdDNS,
		"WILDCARD_DNS_NAME": fmt.Sprintf("*.%s", setupEnv.opts.discoverySRV),
	}

	if err := writeEnvironmentFile(nonExportEnv, currentFile, false); err != nil {
		tc.t.Fatalf("testSetupEnv() failed: %s", err)
	}

	if _, err := os.Stat(setupEnv.opts.outputFile); !os.IsNotExist(err) {
		if got, _ := godotenv.Read(setupEnv.opts.outputFile); !reflect.DeepEqual(got, tc.Want) {
			tc.t.Errorf("testSetupEnv() got %q, want %q", got, tc.Want)
		}
	}
}

func TestPreferredIP(t *testing.T) {
	var preferredIP string
	testIPs, err := ipAddrs(preferredIP)
	if err != nil {
		t.Errorf("TestOverrideIP() failed: %v", err)
	}

	preferredIP = testIPs[0]
	ip, err := ipAddrs(preferredIP)
	if err != nil {
		t.Errorf("TestOverrideIP() failed: %v", err)
	}
	if testIPs[0] != ip[0] {
		t.Errorf("TestOverrideIP() failed: got %q, want %q", ip[0], testIPs[0])
	}
}

// These test cases test setBootstrapEnv() function for 4 scenarios. If INITIAL_CLUSTER and INITIAL_CLUSTER_STATE are defined,
// then, setBookstrapEnv() should not include the conflicting DISCOVERY_SRV environment variable and instead copy those variables
// as is. These variables are defined in the runtime environment file while dealing with the disaster recovery (DR).
// 1. Test no file
// 2. Test with INITIAL_CLUSTER and INITIAL_CLUSTER_STATE defined.
// 3. Test with DISCOVER_SRV in the file
// 4. Test with none of the variables defined.

func TestNonExtantFile(t *testing.T) {
	etcdName := "etcdname.devcluster.openshift.com"
	fileName := fmt.Sprintf("/tmp/etcdDoesNotExist_%d", os.Getpid())

	runOpts := &opts{
		discoverySRV: "devcluster.openshift.com",
		ifName:       "",
		outputFile:   fileName,
		bootstrapSRV: true,
	}

	setupEnv, err := newSetupEnv(runOpts, etcdName, "./tmp", []string{testIPAddress})
	if err != nil {
		t.Fatalf("setupEnv() failed: %s", err)
	}

	bootEnv, err := setupEnv.getBootstrapEnv()
	if err != nil {
		t.Fatalf("setBootstrapEnv() failed: %s", err)
	}

	if bootEnv["DISCOVERY_SRV"] != setupEnv.opts.discoverySRV {
		t.Errorf("bootEnv[\"DISCOVERY_SRV\"] = %q; want %q", bootEnv["DISCOVERY_SRV"], setupEnv.opts.discoverySRV)
	}
	if bootEnv["INITIAL_CLUSTER"] != "" {
		t.Errorf("bootEnv[\"INITIAL_CLUSTER\"] = %q; want %q", bootEnv["INITIAL_CLUSTER"], "")
	}
	if bootEnv["INITIAL_CLUSTER_STATE"] != "" {
		t.Errorf("bootEnv[\"INITIAL_CLUSTER_STATE\"] = %q; want %q", bootEnv["INITIAL_CLUSTER_STATE"], "")
	}
}

func TestFileWithInitialCluster(t *testing.T) {
	etcdName := "etcdname.devcluster.openshift.com"
	etcdIntialClusterState := "existing"
	file, err := ioutil.TempFile("/tmp", "run-etcd-environment.*")
	if err != nil {
		t.Fatalf("Creating a tempfile failed: %s", err)
	}
	defer os.Remove(file.Name())

	runOpts := &opts{
		discoverySRV: "devcluster.openshift.com",
		ifName:       "",
		outputFile:   file.Name(),
		bootstrapSRV: true,
	}

	initialClusterStr := "etcd-member-ip-10-0-147-172.us-east-2.compute.internal=https://etcd-1.etcdname.devcluster.openshift.com:2380"
	m := make(map[string]string)
	m["INITIAL_CLUSTER"] = initialClusterStr
	m["INITIAL_CLUSTER_STATE"] = etcdIntialClusterState

	writeEnvironmentFile(m, file, false)

	setupEnv, err := newSetupEnv(runOpts, etcdName, "./tmp", []string{testIPAddress})
	if err != nil {
		t.Fatalf("setupEnv() failed: %s", err)
	}

	bootEnv, err := setupEnv.getBootstrapEnv()
	if err != nil {
		t.Fatalf("setBootstrapEnv() failed: %s", err)
	}

	if bootEnv["DISCOVERY_SRV"] != "" {
		t.Errorf("bootEnv[\"DISCOVERY_SRV\"] = %q; want %q", bootEnv["DISCOVERY_SRV"], "")
	}
	if bootEnv["INITIAL_CLUSTER"] != initialClusterStr {
		t.Errorf("bootEnv[\"INITIAL_CLUSTER\"] = %q; want %q", bootEnv["INITIAL_CLUSTER"], initialClusterStr)
	}
	if bootEnv["INITIAL_CLUSTER_STATE"] != etcdIntialClusterState {
		t.Errorf("bootEnv[\"INITIAL_CLUSTER_STATE\"] = %q; want %q", bootEnv["INITIAL_CLUSTER_STATE"], etcdIntialClusterState)
	}
}

func TestFileWithDiscoverySrv(t *testing.T) {
	etcdName := "etcdname.devcluster.openshift.com"
	file, err := ioutil.TempFile("/tmp", "run-etcd-environment.*")
	if err != nil {
		t.Fatalf("Creating a tempfile failed: %s", err)
	}
	defer os.Remove(file.Name())

	runOpts := &opts{
		discoverySRV: "devcluster.openshift.com",
		ifName:       "",
		outputFile:   file.Name(),
		bootstrapSRV: true,
	}

	m := make(map[string]string)
	m["DISCOVERY_SRV"] = "OLDNAME.devcluster.openshift.com"

	writeEnvironmentFile(m, file, false)

	setupEnv, err := newSetupEnv(runOpts, etcdName, "./tmp", []string{testIPAddress})
	if err != nil {
		t.Fatalf("setupEnv() failed: %s", err)
	}

	bootEnv, err := setupEnv.getBootstrapEnv()
	if err != nil {
		t.Fatalf("setBootstrapEnv() failed: %s", err)
	}

	if bootEnv["DISCOVERY_SRV"] != setupEnv.opts.discoverySRV {
		t.Errorf("bootEnv[\"DISCOVERY_SRV\"] = %q; want %q", bootEnv["DISCOVERY_SRV"], setupEnv.opts.discoverySRV)
	}
	if bootEnv["INITIAL_CLUSTER"] != "" {
		t.Errorf("bootEnv[\"INITIAL_CLUSTER\"] = %q; want %q", bootEnv["INITIAL_CLUSTER"], "")
	}
	if bootEnv["INITIAL_CLUSTER_STATE"] != "" {
		t.Errorf("bootEnv[\"INITIAL_CLUSTER_STATE\"] = %q; want %q", bootEnv["INITIAL_CLUSTER_STATE"], "")
	}
}

func TestFileWithDiscoverySrvWithFalseFlag(t *testing.T) {
	etcdName := "etcdname.devcluster.openshift.com"
	file, err := ioutil.TempFile("/tmp", "run-etcd-environment.*")
	if err != nil {
		t.Fatalf("Creating a tempfile failed: %s", err)
	}
	defer os.Remove(file.Name())

	runOpts := &opts{
		discoverySRV: "devcluster.openshift.com",
		ifName:       "",
		outputFile:   file.Name(),
		bootstrapSRV: false,
	}

	m := make(map[string]string)
	m["DISCOVERY_SRV"] = "OLDNAME.devcluster.openshift.com"

	writeEnvironmentFile(m, file, false)

	setupEnv, err := newSetupEnv(runOpts, etcdName, "./tmp", []string{testIPAddress})
	if err != nil {
		t.Fatalf("setupEnv() failed: %s", err)
	}

	bootEnv, err := setupEnv.getBootstrapEnv()
	if err != nil {
		t.Fatalf("setBootstrapEnv() failed: %s", err)
	}

	if bootEnv["DISCOVERY_SRV"] != "" {
		t.Errorf("bootEnv[\"DISCOVERY_SRV\"] = %q; want %q", bootEnv["DISCOVERY_SRV"], "")
	}
	if bootEnv["INITIAL_CLUSTER"] != "" {
		t.Errorf("bootEnv[\"INITIAL_CLUSTER\"] = %q; want %q", bootEnv["INITIAL_CLUSTER"], "")
	}
	if bootEnv["INITIAL_CLUSTER_STATE"] != "" {
		t.Errorf("bootEnv[\"INITIAL_CLUSTER_STATE\"] = %q; want %q", bootEnv["INITIAL_CLUSTER_STATE"], "")
	}
}

func TestFileWithNoVariables(t *testing.T) {
	etcdName := "etcdname.devcluster.openshift.com"
	file, err := ioutil.TempFile("/tmp", "run-etcd-environment.*")
	if err != nil {
		t.Fatalf("Creating a tempfile failed: %s", err)
	}
	defer os.Remove(file.Name())

	runOpts := &opts{
		discoverySRV: "devcluster.openshift.com",
		ifName:       "",
		outputFile:   file.Name(),
		bootstrapSRV: true,
	}

	setupEnv, err := newSetupEnv(runOpts, etcdName, "./tmp", []string{testIPAddress})
	if err != nil {
		t.Fatalf("setupEnv() failed: %s", err)
	}

	bootEnv, err := setupEnv.getBootstrapEnv()
	if err != nil {
		t.Fatalf("setBootstrapEnv() failed: %s", err)
	}

	if bootEnv["DISCOVERY_SRV"] != setupEnv.opts.discoverySRV {
		t.Errorf("bootEnv[\"DISCOVERY_SRV\"] = %q; want %q", bootEnv["DISCOVERY_SRV"], setupEnv.opts.discoverySRV)
	}
	if bootEnv["INITIAL_CLUSTER"] != "" {
		t.Errorf("bootEnv[\"INITIAL_CLUSTER\"] = %q; want %q", bootEnv["INITIAL_CLUSTER"], "")
	}
	if bootEnv["INITIAL_CLUSTER_STATE"] != "" {
		t.Errorf("bootEnv[\"INITIAL_CLUSTER_STATE\"] = %q; want %q", bootEnv["INITIAL_CLUSTER_STATE"], "")
	}
}

func TestFileWithNoVariablesFalseFlag(t *testing.T) {
	etcdName := "etcdname.devcluster.openshift.com"
	file, err := ioutil.TempFile("/tmp", "run-etcd-environment.*")
	if err != nil {
		t.Fatalf("Creating a tempfile failed: %s", err)
	}
	defer os.Remove(file.Name())

	runOpts := &opts{
		discoverySRV: "devcluster.openshift.com",
		ifName:       "",
		outputFile:   file.Name(),
		bootstrapSRV: false,
	}

	setupEnv, err := newSetupEnv(runOpts, etcdName, "./tmp", []string{testIPAddress})
	if err != nil {
		t.Fatalf("setupEnv() failed: %s", err)
	}

	bootEnv, err := setupEnv.getBootstrapEnv()
	if err != nil {
		t.Fatalf("setBootstrapEnv() failed: %s", err)
	}

	if bootEnv["DISCOVERY_SRV"] != "" {
		t.Errorf("bootEnv[\"DISCOVERY_SRV\"] = %q; want %q", bootEnv["DISCOVERY_SRV"], "")
	}
	if bootEnv["INITIAL_CLUSTER"] != "" {
		t.Errorf("bootEnv[\"INITIAL_CLUSTER\"] = %q; want %q ", bootEnv["INITIAL_CLUSTER"], "")
	}
	if bootEnv["INITIAL_CLUSTER_STATE"] != "" {
		t.Errorf("bootEnv[\"INITIAL_CLUSTER_STATE\"] = %q; want %q", bootEnv["INITIAL_CLUSTER_STATE"], "")
	}
}

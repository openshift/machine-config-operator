package main

import (
	"fmt"
	"io/ioutil"
	"os"
	"testing"
)

// These test cases test setBootstrapEnv() function for 4 scenarios. If INITIAL_CLUSTER and INITIAL_CLUSTER_STATE are defined,
// then, setBookstrapEnv() should not include the conflicting DISCOVERY_SRV environment variable and instead copy those variables
// as is. These variables are defined in the runtime environment file while dealing with the disaster recovery (DR).
// 1. Test no file
// 2. Test with INITIAL_CLUSTER and INITIAL_CLUSTER_STATE defined.
// 3. Test with DISCOVER_SRV in the file
// 4. Test with none of the variables defined.

func TestNonExtantFile(t *testing.T) {

	fileName := fmt.Sprintf("/tmp/etcdDoesNotExist_%d", os.Getpid())

	bootEnv, err := setBootstrapEnv(fileName, "etcdname.devcluster.openshift.com", true)

	if err != nil {
		t.Fatalf("setBootstrapEnv() failed: %s", err)
	}

	if bootEnv["DISCOVERY_SRV"] != "etcdname.devcluster.openshift.com" {
		t.Errorf("bootEnv[\"DISCOVERY_SRV\"] = \"%s\"; want \"etcdname.devcluster.openshift.com\" ", bootEnv["DISCOVERY_SRV"])
	}

	if bootEnv["INITIAL_CLUSTER"] != "" {
		t.Errorf("bootEnv[\"INITIAL_CLUSTER\"] = \"%s\"; want \"\" ", bootEnv["INITIAL_CLUSTER"])
	}
	if bootEnv["INITIAL_CLUSTER_STATE"] != "" {
		t.Errorf("bootEnv[\"INITIAL_CLUSTER_STATE\"] = \"%s\"; want \"\" ", bootEnv["INITIAL_CLUSTER_STATE"])
	}
}

func TestFileWithInitialCluster(t *testing.T) {

	file, err := ioutil.TempFile("/tmp/", "etcd_")
	if err != nil {
		t.Fatalf("Creating a tempfile failed: %s", err)
	}
	defer os.Remove(file.Name())

	initialClusterStr := "etcd-member-ip-10-0-147-172.us-east-2.compute.internal=https://etcd-1.etcdname.devcluster.openshift.com:2380,etcd-member-ip-10-0-171-108.us-east-2.compute.internal=https://etcd-2.etcdname.devcluster.openshift.com:2380,etcd-member-ip-10-0-128-73.us-east-2.compute.internal=https://etcd-0.etcdname.devcluster.openshift.com:2380"
	m := make(map[string]string)
	m["INITIAL_CLUSTER"] = initialClusterStr
	m["INITIAL_CLUSTER_STATE"] = "existing"

	writeEnvironmentFile(m, file, false)

	bootEnv, err := setBootstrapEnv(file.Name(), "etcdname.devcluster.openshift.com", true)

	if err != nil {
		t.Fatalf("setBootstrapEnv() failed: %s", err)
	}

	if bootEnv["DISCOVERY_SRV"] != "" {
		t.Errorf("bootEnv[\"DISCOVERY_SRV\"] = \"%s\"; want \"\" ", bootEnv["DISCOVERY_SRV"])
	}

	if bootEnv["INITIAL_CLUSTER"] != initialClusterStr {
		t.Errorf("bootEnv[\"INITIAL_CLUSTER\"] = \"%s\"; want \"%s\" ", bootEnv["INITIAL_CLUSTER"], initialClusterStr)
	}
	if bootEnv["INITIAL_CLUSTER_STATE"] != "existing" {
		t.Errorf("bootEnv[\"INITIAL_CLUSTER_STATE\"] = \"%s\"; want \"existing\" ", bootEnv["INITIAL_CLUSTER_STATE"])
	}
}

func TestFileWithDiscoverySrv(t *testing.T) {
	file, err := ioutil.TempFile("/tmp/", "etcd_")
	if err != nil {
		t.Fatalf("Creating a tempfile failed: %s", err)
	}
	defer os.Remove(file.Name())

	m := make(map[string]string)
	m["DISCOVERY_SRV"] = "OLDNAME.devcluster.openshift.com"

	writeEnvironmentFile(m, file, false)

	bootEnv, err := setBootstrapEnv(file.Name(), "etcdname.devcluster.openshift.com", true)

	if err != nil {
		t.Fatalf("setBootstrapEnv() failed: %s", err)
	}

	if bootEnv["DISCOVERY_SRV"] != "etcdname.devcluster.openshift.com" {
		t.Errorf("bootEnv[\"DISCOVERY_SRV\"] = \"%s\"; want \"etcdname.devcluster.openshift.com\" ", bootEnv["DISCOVERY_SRV"])
	}

	if bootEnv["INITIAL_CLUSTER"] != "" {
		t.Errorf("bootEnv[\"INITIAL_CLUSTER\"] = \"%s\"; want \"\" ", bootEnv["INITIAL_CLUSTER"])
	}
	if bootEnv["INITIAL_CLUSTER_STATE"] != "" {
		t.Errorf("bootEnv[\"INITIAL_CLUSTER_STATE\"] = \"%s\"; want \"\" ", bootEnv["INITIAL_CLUSTER_STATE"])
	}
}

func TestFileWithDiscoverySrvWithFalseFlag(t *testing.T) {
	file, err := ioutil.TempFile("/tmp/", "etcd_")
	if err != nil {
		t.Fatalf("Creating a tempfile failed: %s", err)
	}
	defer os.Remove(file.Name())

	m := make(map[string]string)
	m["DISCOVERY_SRV"] = "OLDNAME.devcluster.openshift.com"

	writeEnvironmentFile(m, file, false)

	bootEnv, err := setBootstrapEnv(file.Name(), "etcdname.devcluster.openshift.com", false)

	if err != nil {
		t.Fatalf("setBootstrapEnv() failed: %s", err)
	}

	if bootEnv["DISCOVERY_SRV"] != "" {
		t.Errorf("bootEnv[\"DISCOVERY_SRV\"] = \"%s\"; want \"\" ", bootEnv["DISCOVERY_SRV"])
	}

	if bootEnv["INITIAL_CLUSTER"] != "" {
		t.Errorf("bootEnv[\"INITIAL_CLUSTER\"] = \"%s\"; want \"\" ", bootEnv["INITIAL_CLUSTER"])
	}
	if bootEnv["INITIAL_CLUSTER_STATE"] != "" {
		t.Errorf("bootEnv[\"INITIAL_CLUSTER_STATE\"] = \"%s\"; want \"\" ", bootEnv["INITIAL_CLUSTER_STATE"])
	}
}

func TestFileWithNoVariables(t *testing.T) {
	file, err := ioutil.TempFile("/tmp/", "etcd_")
	if err != nil {
		t.Fatalf("Creating a tempfile failed: %s", err)
	}
	defer os.Remove(file.Name())

	bootEnv, err := setBootstrapEnv(file.Name(), "etcdname.devcluster.openshift.com", true)

	if err != nil {
		t.Fatalf("setBootstrapEnv() failed: %s", err)
	}

	if bootEnv["DISCOVERY_SRV"] != "etcdname.devcluster.openshift.com" {
		t.Errorf("bootEnv[\"DISCOVERY_SRV\"] = \"%s\"; want \"etcdname.devcluster.openshift.com\" ", bootEnv["DISCOVERY_SRV"])
	}
	if bootEnv["INITIAL_CLUSTER"] != "" {
		t.Errorf("bootEnv[\"INITIAL_CLUSTER\"] = \"%s\"; want \"\" ", bootEnv["INITIAL_CLUSTER"])
	}
	if bootEnv["INITIAL_CLUSTER_STATE"] != "" {
		t.Errorf("bootEnv[\"INITIAL_CLUSTER_STATE\"] = \"%s\"; want \"\" ", bootEnv["INITIAL_CLUSTER_STATE"])
	}
}

func TestFileWithNoVariablesFalseFlag(t *testing.T) {
	file, err := ioutil.TempFile("/tmp/", "etcd_")
	if err != nil {
		t.Fatalf("Creating a tempfile failed: %s", err)
	}
	defer os.Remove(file.Name())

	bootEnv, err := setBootstrapEnv(file.Name(), "etcdname.devcluster.openshift.com", false)

	if err != nil {
		t.Fatalf("setBootstrapEnv() failed: %s", err)
	}

	if bootEnv["DISCOVERY_SRV"] != "" {
		t.Errorf("bootEnv[\"DISCOVERY_SRV\"] = \"%s\"; want \"\" ", bootEnv["DISCOVERY_SRV"])
	}
	if bootEnv["INITIAL_CLUSTER"] != "" {
		t.Errorf("bootEnv[\"INITIAL_CLUSTER\"] = \"%s\"; want \"\" ", bootEnv["INITIAL_CLUSTER"])
	}
	if bootEnv["INITIAL_CLUSTER_STATE"] != "" {
		t.Errorf("bootEnv[\"INITIAL_CLUSTER_STATE\"] = \"%s\"; want \"\" ", bootEnv["INITIAL_CLUSTER_STATE"])
	}
}

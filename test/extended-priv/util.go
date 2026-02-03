package extended

import (
	"context"
	"crypto/x509"
	b64 "encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	exutil "github.com/openshift/machine-config-operator/test/extended-priv/util"
	logger "github.com/openshift/machine-config-operator/test/extended-priv/util/logext"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/yaml"

	g "github.com/onsi/ginkgo/v2"
	o "github.com/onsi/gomega"
	e2e "k8s.io/kubernetes/test/e2e/framework"
)

// fixturesPath stores a local copy of the embedded resources
var (
	fixturesPath    = ""
	fixtureInitLock sync.Once
)

// PodDisruptionBudget struct is used to handle PodDisruptionBudget resources in OCP
type PodDisruptionBudget struct {
	name      string
	namespace string
	template  string
}

// ImageContentSourcePolicy struct is used to handle ImageContentSourcePolicy resources in OCP
type ImageContentSourcePolicy struct {
	name     string
	template string
}

// ImageDigestMirrorSet struct is used to handle ImageDigestMirrorSet resources in OCP
type ImageDigestMirrorSet struct {
	Resource
	Template
}

// NewImageDigestMirrorSet create a new ImageDigestMirrorSet struct
func NewImageDigestMirrorSet(oc *exutil.CLI, name string, t Template) *ImageDigestMirrorSet {
	return &ImageDigestMirrorSet{Resource: *NewResource(oc, "ImageDigestMirrorSet", name), Template: t}
}

// ImageTagMirrorSet struct is used to handle ImageTagMirrorSet resources in OCP
type ImageTagMirrorSet struct {
	Resource
	Template
}

// TextToVerify is a helper struct to verify configurations using the `createMcAndVerifyMCValue` function
type TextToVerify struct {
	textToVerifyForMC   string
	textToVerifyForNode string
	needBash            bool
	needChroot          bool
}

func (pdb *PodDisruptionBudget) create(oc *exutil.CLI) {
	logger.Infof("Creating pod disruption budget: %s", pdb.name)
	exutil.CreateNsResourceFromTemplate(oc, pdb.namespace, "--ignore-unknown-parameters=true", "-f", pdb.template, "-p", "NAME="+pdb.name)
}

func (pdb *PodDisruptionBudget) delete(oc *exutil.CLI) {
	logger.Infof("Deleting pod disruption budget: %s", pdb.name)
	err := oc.AsAdmin().WithoutNamespace().Run("delete").Args("pdb", pdb.name, "-n", pdb.namespace, "--ignore-not-found=true").Execute()
	o.Expect(err).NotTo(o.HaveOccurred())
}

func (icsp *ImageContentSourcePolicy) create(oc *exutil.CLI) {
	exutil.CreateClusterResourceFromTemplate(oc, "--ignore-unknown-parameters=true", "-f", icsp.template, "-p", "NAME="+icsp.name)
	mcp := NewMachineConfigPool(oc.AsAdmin(), "worker")
	mcp.waitForComplete()
	mcp.name = "master"
	mcp.waitForComplete()
}

func (icsp *ImageContentSourcePolicy) delete(oc *exutil.CLI) {
	logger.Infof("deleting icsp config: %s", icsp.name)
	err := oc.AsAdmin().WithoutNamespace().Run("delete").Args("imagecontentsourcepolicy", icsp.name, "--ignore-not-found=true").Execute()
	o.Expect(err).NotTo(o.HaveOccurred())
	mcp := NewMachineConfigPool(oc.AsAdmin(), "worker")
	mcp.waitForComplete()
	mcp.name = "master"
	mcp.waitForComplete()
}

func getPullSecret(oc *exutil.CLI) (string, error) {
	return oc.AsAdmin().WithoutNamespace().Run("get").Args("secret/pull-secret", "-n", "openshift-config", `--template={{index .data ".dockerconfigjson" | base64decode}}`).OutputToFile("auth.dockerconfigjson")
}

func setDataForPullSecret(oc *exutil.CLI, configFile string) (string, error) {
	return oc.AsAdmin().WithoutNamespace().Run("set").Args("data", "secret/pull-secret", "-n", "openshift-config", "--from-file=.dockerconfigjson="+configFile).Output()
}

// generateTemplateAbsolutePath manipulates absolute path of test file by
// cached fixture test data dir and file name
// because exutil.FixturePath will copy all test files to fixture path (tmp dir with prefix fixture-testdata-dir)
// this operation is very expensive, we don't want to call it for every case
func generateTemplateAbsolutePath(fileName string) string {
	fixtureInitLock.Do(func() {
		logger.Infof("mco fixture dir is not initialized, start to create")
		fixturesPath = exutil.FixturePath(".")
		logger.Infof("mco fixture dir is initialized: %s", fixturesPath)
	})
	return filepath.Join(fixturesPath, fileName)
}

func sortNodeList(nodes []*Node) []*Node {
	sort.Slice(nodes, func(l, r int) bool {
		lMetadata := JSON(nodes[l].GetOrFail("{.metadata}"))
		rMetadata := JSON(nodes[r].GetOrFail("{.metadata}"))

		lLabels := &JSONData{nil}
		if lMetadata.Get("labels").Exists() {
			lLabels = lMetadata.Get("labels")
		}
		rLabels := &JSONData{nil}
		if rMetadata.Get("labels").Exists() {
			rLabels = rMetadata.Get("labels")
		}

		lZone := lLabels.Get("topology.kubernetes.io/zone")
		rZone := rLabels.Get("topology.kubernetes.io/zone")
		// if both nodes have zone label, sort by zone, push nodes without label to end of list
		if lZone.Exists() && rZone.Exists() { //nolint:all
			if lZone.ToString() != rZone.ToString() {
				return lZone.ToString() < rZone.ToString()
			}
		} else if rZone.Exists() {
			return false
		} else if lZone.Exists() {
			return true
		}

		// if nodes are in the same zone or they have no labels sortby creationTime oldest to newest
		dateLayout := "2006-01-02T15:04:05Z"
		lDate, err := time.Parse(dateLayout, lMetadata.Get("creationTimestamp").ToString())
		if err != nil {
			e2e.Failf("Cannot parse creationTimestamp %s in node %s", lMetadata.Get("creationTimestamp").ToString(), nodes[l].GetName())

		}
		rDate, err := time.Parse(dateLayout, rMetadata.Get("creationTimestamp").ToString())
		if err != nil {
			e2e.Failf("Cannot parse creationTimestamp %s in node %s", rMetadata.Get("creationTimestamp").ToString(), nodes[r].GetName())

		}
		return lDate.Before(rDate)

	})
	return nodes
}

// sortMasterNodeList returns the list of nodes sorted by the order used to updated them in MCO master pool.
//
//	Master pool will use the same order as the rest of the pools, but the node running the operator pod will be the last one to be updated.
func sortMasterNodeList(oc *exutil.CLI, nodes []*Node) ([]*Node, error) {
	masterSortedNodes := []*Node{}
	operatorNode, err := GetOperatorNode(oc)
	if err != nil {
		return nil, err
	}

	logger.Infof("MCO operator pod running in node: %s", operatorNode)

	var latestNode *Node
	for _, item := range sortNodeList(nodes) {
		node := item
		if node.GetName() == operatorNode.GetName() {
			latestNode = node
			continue
		}
		masterSortedNodes = append(masterSortedNodes, node)
	}

	masterSortedNodes = append(masterSortedNodes, latestNode)

	logger.Infof("Sorted master nodes: %s", masterSortedNodes)

	return masterSortedNodes, nil
}

// PreChecks executes some basic checks to make sure the the cluster is healthy enough to run MCO test cases
func PreChecks(oc *exutil.CLI) {
	exutil.By("MCO Preconditions Checks")

	allMCPs, err := NewMachineConfigPoolList(oc.AsAdmin()).GetAll()
	o.Expect(err).NotTo(o.HaveOccurred(), "Cannot get the list of MachineConfigPools")

	for _, pool := range allMCPs {
		logger.Infof("Check that %s pool is ready for testing", pool.GetName())
		if err := pool.SanityCheck(); err != nil { // Check that it is not degraded nor updating
			logger.Errorf("MCP is not ready for testing (it is updating or degraded):\n%s", pool.PrettyString())
			g.Skip(fmt.Sprintf("%s pool is not ready for testing. %s", pool.GetName(), err))
		}

	}

	logger.Infof("Wait for MCC to get the leader lease")
	o.Eventually(NewController(oc.AsAdmin()).HasAcquiredLease, "6m", "20s").Should(o.BeTrue(),
		"The controller pod didn't acquire the lease properly. Cannot execute any test case if the controller is not active")

	logger.Infof("End of MCO Preconditions\n")
}

// create a temp dir with e2e namespace for every test in JustBeforeEach
// fail the test, if dir creation is failed
func createTmpDir() string {
	// according to review comment, dir name should not depend on temp ns
	// it is empty when initialized by `NewCLIWithoutNamespace`
	tmpdir := filepath.Join(e2e.TestContext.OutputDir, fmt.Sprintf("mco-test-%s", exutil.GetRandomString()))
	err := os.MkdirAll(tmpdir, 0o755)
	o.Expect(err).NotTo(o.HaveOccurred())
	logger.Infof("create test dir %s", tmpdir)
	return tmpdir
}

func IsTrue(s string) bool {
	return strings.EqualFold(s, TrueString)
}

// IsSNOSafe returns true if the cluster is a SNO cluster. Instead of failing, it returns an error if we can't know if the cluster is SNO or not
func IsSNOSafe(oc *exutil.CLI) (bool, error) {
	allNodes, err := NewNodeList(oc.AsAdmin()).GetAll()
	if err != nil {
		return false, err
	}
	return len(allNodes) == 1, nil
}

// Retry function that retries a given function f, with a specified number of attempts and a delay between attempts
func Retry(attempts int, delay time.Duration, f func() error) error {
	var err error
	for i := 0; i < attempts; i++ {
		err = f()
		if err == nil {
			return nil
		}
		logger.Errorf("Attempt %d failed: %v\n", i+1, err)

		if i < attempts-1 {
			time.Sleep(delay)
		}
	}
	return err
}

// GetLastNLines returns the last N lines from a string
func GetLastNLines(s string, n int) string {
	lines := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	lenLines := len(lines)
	if lenLines > n {
		return strings.Join(lines[lenLines-n:], "\n")
	}
	return strings.Join(lines, "\n")
}

// GetClonedResourceJSONString takes the json data of a given resource and clone it using a new name and namespace, removing unnecessary fields
// Sometimes we need to apply extra changes, in order to do so we can provide an function using the extraModifications parameter
func GetClonedResourceJSONString(res ResourceInterface, newName, newNamespace string, extraModifications func(string) (string, error)) (string, error) {
	jsonRes, err := res.GetCleanJSON()
	if err != nil {
		return "", err
	}

	jsonRes, err = sjson.Delete(jsonRes, "status")
	if err != nil {
		return "", err
	}

	jsonRes, err = sjson.Delete(jsonRes, "metadata.creationTimestamp")
	if err != nil {
		return "", err
	}
	jsonRes, err = sjson.Delete(jsonRes, "metadata.resourceVersion")
	if err != nil {
		return "", err
	}
	jsonRes, err = sjson.Delete(jsonRes, "metadata.uid")
	if err != nil {
		return "", err
	}

	jsonRes, err = sjson.Delete(jsonRes, "metadata.generation")
	if err != nil {
		return "", err
	}

	jsonRes, err = sjson.Set(jsonRes, "metadata.name", newName)
	if err != nil {
		return "", err
	}

	if newNamespace != "" {
		jsonRes, err = sjson.Set(jsonRes, "metadata.namespace", newNamespace)
		if err != nil {
			return "", err
		}
	}

	if extraModifications != nil {
		logger.Infof("Executing extra modifications")
		return extraModifications(jsonRes)
	}

	return jsonRes, nil

}

// CloneResource will clone the given resource with the new name and the new namespace. If new namespace is an empty strng, it is ignored and the namespace will not be changed.
// Sometimes we need to apply extra changes to the cloned resource before it is created, in order to do so we can provide an function using the extraModifications parameter
func CloneResource(res ResourceInterface, newName, newNamespace string, extraModifications func(string) (string, error)) (*Resource, error) {
	logger.Infof("Cloning resource %s with name %s and namespace %s", res, newName, newNamespace)

	jsonRes, err := GetClonedResourceJSONString(res, newName, newNamespace, extraModifications)
	if err != nil {
		return nil, err
	}

	if newNamespace == "" {
		newNamespace = res.GetNamespace()
	}

	filename := "cloned-" + res.GetKind() + "-" + newName + "-" + uuid.NewString()
	if newNamespace != "" {
		filename += "-namespace"
	}
	filename += ".json"

	tmpFile := generateTmpFile(res.GetOC(), filename)

	wErr := os.WriteFile(tmpFile, []byte(jsonRes), 0o644)
	if wErr != nil {
		return nil, wErr
	}

	logger.Infof("New resource created using definition file %s", tmpFile)

	_, cErr := res.GetOC().AsAdmin().WithoutNamespace().Run("create").Args("-f", tmpFile).Output()

	if cErr != nil {
		return nil, cErr
	}

	return NewNamespacedResource(res.GetOC(), res.GetKind(), newNamespace, newName), nil
}

// DEPRECATED, use generateTempFilePath instead
func generateTmpFile(oc *exutil.CLI, fileName string) string {
	return filepath.Join(e2e.TestContext.OutputDir, oc.Namespace()+"-"+fileName)
}

// MergeDockerConfigs accepts two docker configs strings as parameter and merge them
func MergeDockerConfigs(dockerConfig1, dockerConfig2 string) (string, error) {

	type DockerConfig struct {
		Auths map[string]interface{} `json:"auths"`
	}

	var config1, config2 DockerConfig
	err := json.Unmarshal([]byte(dockerConfig1), &config1)
	if err != nil {
		logger.Errorf("Error unmarshalling dockerConfig1")
		return "", err
	}
	err = json.Unmarshal([]byte(dockerConfig2), &config2)
	if err != nil {
		logger.Errorf("Error unmarshalling dockerConfig2")
		return "", err
	}

	for k, v := range config2.Auths {
		config1.Auths[k] = v
	}

	mergedConfig, err := json.Marshal(config1)
	if err != nil {
		logger.Errorf("Cannot marshal the merged docker config")
		return "", err
	}

	return string(mergedConfig), err
}

// getEnabledCapabilities get enabled capability list, the invoker can check expected capability is enabled or not
func getEnabledCapabilities(oc *exutil.CLI) []interface{} {
	jsonStr := NewResource(oc.AsAdmin(), "clusterversion", "version").GetOrFail(`{.status.capabilities.enabledCapabilities}`)
	logger.Infof("enabled capabilities: %s", jsonStr)
	enabledCapabilities := make([]interface{}, 0)
	jsonData := JSON(jsonStr)
	if jsonData.Exists() {
		enabledCapabilities = jsonData.ToList()
	}

	return enabledCapabilities
}

// IsCapabilityEnabled check whether capability is in enabledCapabilities
func IsCapabilityEnabled(oc *exutil.CLI, capability string) bool {
	enabledCapabilities := getEnabledCapabilities(oc)
	enabled := false
	for _, ec := range enabledCapabilities {
		if ec == capability {
			enabled = true
			break
		}
	}
	logger.Infof("Capability [%s] is enabled: %v", capability, enabled)

	return enabled
}

// GetCurrentTestPolarionIDNumber inspects the name of the test case and return the number of the polarion ID linked to this automated test case. It returns an empty string if no ID found.
func GetCurrentTestPolarionIDNumber() string {
	name := g.CurrentSpecReport().FullText()

	r := regexp.MustCompile(`\[PolarionID:(?P<id>\d+)\]`)

	matches := r.FindStringSubmatch(name)
	number := r.SubexpIndex("id")
	if len(matches) < number+1 {
		logger.Errorf("Could not get the test case ID")
		return ""
	}

	return matches[number]
}

// IsCompactOrSNOCluster returns true if the current cluster is a Compact cluster or a SNO cluster
func IsCompactOrSNOCluster(oc *exutil.CLI) bool {
	var (
		wMcp    = NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolWorker)
		mcpList = NewMachineConfigPoolList(oc.AsAdmin())
	)

	return wMcp.IsEmpty() && len(mcpList.GetAllOrFail()) == 2
}

// MarshalOrFail marshals the input to JSON and fails the test if there is an error
func MarshalOrFail(input interface{}) []byte {
	inputJSON, err := json.Marshal(input)
	o.Expect(err).NotTo(o.HaveOccurred(), "Error marshaling input to JSON")
	return inputJSON
}

// OCCreate creates a resource from a file using oc create -f
func OCCreate(oc *exutil.CLI, fileName string) error {
	return oc.Run("create").Args("-f", fileName).Execute()
}

// OrFail function will process another function's return values and fail if any of those returned values is an error != nil and returns the first value
// example: if we have: func getValue() (string, error)
//
//	we can do:  value := OrFail[string](getValue())
func OrFail[T any](vals ...any) T {
	for _, val := range vals {
		err, ok := val.(error)
		if ok {
			o.ExpectWithOffset(1, err).NotTo(o.HaveOccurred())
		}
	}

	return vals[0].(T)
}

// QuoteIfNotJSON quotes a string if it's not valid JSON
func QuoteIfNotJSON(s string) string {
	var js interface{}
	if json.Unmarshal([]byte(s), &js) == nil {
		// It's valid JSON → return as is
		return s
	}
	// Not valid JSON → return quoted JSON string
	b, err := json.Marshal(s)
	if err != nil {
		e2e.Failf("The provided string cannot be JSON encoded: %s", s)
	}
	return string(b)
}

// WorkersCanBeScaled returns true if worker nodes can be scaled using machinesets
func WorkersCanBeScaled(oc *exutil.CLI) (bool, error) {
	platform := exutil.CheckPlatform(oc)
	logger.Infof("Checking if in this cluster workers can be scaled using machinesets")

	// Baremetal and None platforms cannot scale workers
	if platform == "baremetal" || platform == "none" || platform == "" {
		logger.Infof("Baremetal/None platform. Can't scale up nodes in Baremetal test environments. Nodes cannot be scaled")
		return false, nil
	}

	// Check if MachineAPI capability is enabled
	if !IsCapabilityEnabled(oc.AsAdmin(), "MachineAPI") {
		logger.Infof("MachineAPI capability is disabled. Nodes cannot be scaled")
		return false, nil
	}

	// Get all machinesets
	msl, err := NewMachineSetList(oc.AsAdmin(), MachineAPINamespace).GetAll()
	if err != nil {
		logger.Errorf("Error getting a list of MachineSet resources")
		return false, err
	}

	// If there is no machineset then clearly we can't use them to scale the workers
	if len(msl) == 0 {
		logger.Infof("No machineset configured. Nodes cannot be scaled")
		return false, nil
	}

	totalworkers := 0
	for _, ms := range msl {
		replicas, err := ms.Get(`{.spec.replicas}`)
		if err != nil {
			logger.Errorf("Error getting the number of replicas in %s", ms)
			return false, err
		}
		if replicas != "" {
			intReplicas, err := strconv.Atoi(replicas)
			if err == nil {
				totalworkers += intReplicas
			}
		}
	}

	// In some UPI/SNO/Compact clusters machineset resources exist, but they are all configured with 0 replicas
	// If all machinesets have 0 replicas, then it means that we need to skip the test case
	if totalworkers == 0 {
		logger.Infof("All machinesets have 0 worker nodes. Nodes cannot be scaled")
		return false, nil
	}

	return true, nil
}

// SkipTestIfWorkersCannotBeScaled skips the current test if the worker pool cannot be scaled via machineset
func SkipTestIfWorkersCannotBeScaled(oc *exutil.CLI) {
	canBeScaled, err := WorkersCanBeScaled(oc)
	o.ExpectWithOffset(1, err).NotTo(o.HaveOccurred(), "Error deciding if worker nodes can be scaled using machinesets")

	if !canBeScaled {
		g.Skip("Worker nodes cannot be scaled using machinesets. This test cannot be execute if workers cannot be scaled via machineset, IPI clusters.")
	}
}

// getEnabledFeatureGates returns the list of enabled feature gates
func getEnabledFeatureGates(oc *exutil.CLI) ([]string, error) {
	enabledFeatureGates, err := NewResource(oc.AsAdmin(), "featuregate", "cluster").Get(`{.status.featureGates[0].enabled[*].name}`)
	if err != nil {
		return nil, err
	}

	if strings.TrimSpace(enabledFeatureGates) == "" {
		return []string{}, nil
	}

	return strings.Split(enabledFeatureGates, " "), nil
}

// IsFeaturegateEnabled checks whether a featuregate is enabled or not
func IsFeaturegateEnabled(oc *exutil.CLI, featuregate string) (bool, error) {
	enabledFeatureGates, err := getEnabledFeatureGates(oc)
	if err != nil {
		return false, err
	}
	for _, f := range enabledFeatureGates {
		if f == featuregate {
			return true, nil
		}
	}
	return false, nil
}

// SkipIfNoFeatureGate skips the test if the specified feature gate is not enabled
func SkipIfNoFeatureGate(oc *exutil.CLI, featuregate string) {
	enabled, err := IsFeaturegateEnabled(oc, featuregate)
	o.Expect(err).NotTo(o.HaveOccurred(), "Error getting enabled featuregates")

	if !enabled {
		g.Skip(fmt.Sprintf("Featuregate %s is not enabled in this cluster", featuregate))
	}
}

// skipTestIfSupportedPlatformNotMatched skips the test if the current platform is not in the supported list
func skipTestIfSupportedPlatformNotMatched(oc *exutil.CLI, supported ...string) {
	var match bool
	p := exutil.CheckPlatform(oc)
	for _, sp := range supported {
		if strings.EqualFold(sp, p) {
			match = true
			break
		}
	}

	if !match {
		g.Skip(fmt.Sprintf("skip test because current platform %s is not in supported list %v", p, supported))
	}
}

// RemoveDuplicates removes duplicate items from a list
func RemoveDuplicates[T comparable](list []T) []T {
	allKeys := make(map[T]bool)
	fileterdList := []T{}
	for _, item := range list {
		if !allKeys[item] {
			allKeys[item] = true
			fileterdList = append(fileterdList, item)
		}
	}
	return fileterdList
}

// isFIPSEnabledInClusterConfig checks if FIPS is enabled in the cluster configuration
func isFIPSEnabledInClusterConfig(oc *exutil.CLI) bool {
	cc := NewNamespacedResource(oc.AsAdmin(), "cm", "kube-system", "cluster-config-v1")
	ic := cc.GetOrFail("{.data.install-config}")
	return strings.Contains(ic, "fips: true")
}

// skipTestIfFIPSIsNotEnabled skip the test if fips is not enabled
func skipTestIfFIPSIsNotEnabled(oc *exutil.CLI) {
	if !isFIPSEnabledInClusterConfig(oc) {
		g.Skip("fips is not enabled, skip this test")
	}
}

// skipTestIfFIPSIstEnabled skip the test if fips is not enabled
func skipTestIfFIPSIsEnabled(oc *exutil.CLI) {
	if isFIPSEnabledInClusterConfig(oc) {
		g.Skip("fips is enabled, skip this test")
	}
}

// getURLEncodedFileConfig returns a file configuration with URL-encoded content
//
//nolint:unparam
func getURLEncodedFileConfig(destinationPath, content, mode string) string {
	encodedContent := url.PathEscape(content)

	return getFileConfig(destinationPath, "data:,"+encodedContent, mode)
}

// getFileConfig returns a file configuration JSON string
func getFileConfig(destinationPath, source, mode string) string {
	decimalMode := mode
	// if octal number we convert it to decimal. Json templates do not accept numbers with a leading zero (octal).
	// if we don't do this conversion the 'oc process' command will not be able to render the template because {"mode": 0666}
	//   is not a valid json. Numbers in json cannot start with a leading 0
	if mode != "" && mode[0] == '0' {
		// parse the octal string and conver to unsigned integer(file modes are always positive)
		iMode, err := strconv.ParseUint(mode, 8, 64)
		// get a string with the decimal numeric representation of the mode
		decimalMode = fmt.Sprintf("%d", iMode)
		if err != nil {
			e2e.Failf("Filer permissions %s cannot be converted to integer", mode)
		}
	}

	var fileConfig string
	if mode == "" {
		fileConfig = fmt.Sprintf(`{"contents": {"source": "%s"}, "path": "%s"}`, source, destinationPath)
	} else {
		fileConfig = fmt.Sprintf(`{"contents": {"source": "%s"}, "path": "%s", "mode": %s}`, source, destinationPath, decimalMode)
	}

	return fileConfig
}

// GetMCSPodNames returns the names of machine-config-server pods
func GetMCSPodNames(oc *exutil.CLI) ([]string, error) {
	output, err := oc.AsAdmin().WithoutNamespace().Run("get").Args("pods", "-n", MachineConfigNamespace,
		"-l", "k8s-app=machine-config-server", "-o", "jsonpath={.items[*].metadata.name }").Output()
	if err != nil {
		return nil, err
	}

	if strings.Trim(output, " \n") == "" {
		return []string{}, nil
	}

	return strings.Split(output, " "), nil
}

// RotateMCSCertificates it executes the "oc adm ocp-certificates regenerate-machine-config-server-serving-cert" command in a master node
// When we execute the command in the master node we make sure that in FIPS clusters we are running the command from a FIPS enabled machine.
func RotateMCSCertificates(oc *exutil.CLI) error {
	wMcp := NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolWorker)
	master := wMcp.GetNodesOrFail()[0]

	remoteAdminKubeConfig := fmt.Sprintf("/root/remoteKubeConfig-%s", uuid.New().String())
	adminKubeConfig := exutil.KubeConfigPath()

	defer master.RemoveFile(remoteAdminKubeConfig)
	err := master.CopyFromLocal(adminKubeConfig, remoteAdminKubeConfig)

	if err != nil {
		return err
	}

	command := fmt.Sprintf("oc --kubeconfig=%s --insecure-skip-tls-verify adm ocp-certificates regenerate-machine-config-server-serving-cert",
		remoteAdminKubeConfig)

	logger.Infof("RUN: %s", command)
	stdout, err := master.DebugNodeWithChroot(strings.Split(command, " ")...)

	logger.Infof(stdout)

	return err
}

// skipIfNoTechPreview skips the test if TechPreviewNoUpgrade is not enabled
func skipIfNoTechPreview(oc *exutil.CLI) {
	if !exutil.IsTechPreviewNoUpgrade(oc) {
		g.Skip("featureSet: TechPreviewNoUpgrade is required for this test")
	}
}

func jsonEncode(s string) string {
	e, err := json.Marshal(s)
	if err != nil {
		e2e.Failf("Error json encoding the string: %s", s)
	}
	return string(e)
}

func generateTempFilePath(dir, pattern string) string {
	return filepath.Join(dir, strings.ReplaceAll(pattern, "*", exutil.GetRandomString()))
}

func getSingleUnitConfig(unitName string, unitEnabled bool, unitContents string) string {
	// Escape not valid characters in json from the file content
	escapedContent := jsonEncode(unitContents)
	return fmt.Sprintf(`{"name": "%s", "enabled": %t, "contents": %s}`, unitName, unitEnabled, escapedContent)
}

// GetCertificatesInfoFromPemBundle extracts certificate information from a PEM bundle
func GetCertificatesInfoFromPemBundle(bundleName string, pemBundle []byte) ([]CertificateInfo, error) {
	var certificatesInfo []CertificateInfo

	if pemBundle == nil {
		return nil, fmt.Errorf("Provided pem bundle is nil")
	}

	if len(pemBundle) == 0 {
		logger.Infof("Empty pem bundle")
		return certificatesInfo, nil
	}

	for {
		block, rest := pem.Decode(pemBundle)
		if block == nil {
			return nil, fmt.Errorf("failed to parse certificate PEM:\n%s", string(pemBundle))
		}

		logger.Infof("FOUND: %s", block.Type)
		if block.Type != "CERTIFICATE" {
			return nil, fmt.Errorf("Only CERTIFICATES are expected in the bundle, but a type %s was found in it", block.Type)
		}

		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, err
		}

		certificatesInfo = append(certificatesInfo,
			CertificateInfo{
				BundleFile: bundleName,
				NotAfter:   cert.NotAfter.Format(time.RFC3339),
				NotBefore:  cert.NotBefore.Format(time.RFC3339),
				Signer:     cert.Issuer.String(),
				Subject:    cert.Subject.String(),
			},
		)

		pemBundle = rest
		if len(rest) == 0 {
			break
		}

	}
	return certificatesInfo, nil
}

// createCA creates a new CA certificate and private key
//
//nolint:unparam
func createCA(tmpDir, caFileName string) (keyPath, caPath string, err error) {
	var (
		keyFileName = "privateKey.pem"
	)
	caPath = filepath.Join(tmpDir, caFileName)
	keyPath = filepath.Join(tmpDir, keyFileName)

	logger.Infof("Creating CA in directory %s", tmpDir)
	logger.Infof("Create key")
	keyArgs := []string{"genrsa", "-out", keyFileName, "4096"}
	cmd := exec.Command("openssl", keyArgs...)
	cmd.Dir = tmpDir

	output, err := cmd.CombinedOutput()
	if err != nil {
		logger.Errorf(string(output))
		return "", "", err
	}

	logger.Infof("Create CA")

	caArgs := []string{"req", "-new", "-x509", "-nodes", "-days", "3600", "-key", "privateKey.pem", "-out", caFileName, "-subj", "/OU=MCO QE/CN=example.com"}

	cmd = exec.Command("openssl", caArgs...)
	cmd.Dir = tmpDir

	output, err = cmd.CombinedOutput()
	if err != nil {
		logger.Errorf(string(output))
		return "", "", err
	}

	return keyPath, caPath, nil
}

// ToJSON converts the given string in to JSON format
func ToJSON(content string) (string, error) {
	var js json.RawMessage
	if json.Unmarshal([]byte(content), &js) == nil {
		// the string is already JSON, no need to manipulate it
		return content, nil
	}

	bytes, err := yaml.YAMLToJSON([]byte(content))
	return string(bytes), err
}

// getCertsFromKubeconfig extracts the CA certificate from a kubeconfig file
func getCertsFromKubeconfig(kubeconfig string) (string, error) {
	// We don't know if the kubeconfig file will be in YAML or in JSON format
	// We will transform it into JSON
	JSONstring, err := ToJSON(kubeconfig)
	if err != nil {
		return "", err
	}

	currentCtx := gjson.Get(JSONstring, "current-context")
	logger.Debugf("Context: %s\n", currentCtx)
	if !currentCtx.Exists() || currentCtx.String() == "" {
		return "", fmt.Errorf("No current-contenxt in the provided kubeconfig")
	}

	logger.Debugf("Current context: %s", currentCtx.String())

	cluster := gjson.Get(JSONstring, `contexts.#(name=="`+currentCtx.String()+`").context.cluster`)
	if !cluster.Exists() || cluster.String() == "" {
		return "", fmt.Errorf("No current cluster information for context %s in the provided kubeconfig", currentCtx.String())
	}

	logger.Debugf("Cluster: %s\n", cluster.String())

	cert64 := gjson.Get(JSONstring, `clusters.#(name=="`+cluster.String()+`").cluster.certificate-authority-data`)
	if !cert64.Exists() || cert64.String() == "" {
		return "", fmt.Errorf("No current certificate-authority-data information for context %s and cluster %s in the provided kubeconfig", currentCtx.String(), cluster.String())
	}

	cert, err := b64.StdEncoding.DecodeString(cert64.String())
	if err != nil {
		logger.Errorf("The certiifcate provided in the kubeconfig is not base64 encoded")
		return "", err
	}

	logger.Infof("Certificate successfully extracted from kubeconfig data")
	logger.Debugf("Cert: %s\n", string(cert))
	return string(cert), nil
}

// GetBase64EncodedFileSourceContent encodes file content as a base64 data URI
func GetBase64EncodedFileSourceContent(fileContent string) string {

	encodedContent := b64.StdEncoding.EncodeToString([]byte(fileContent))

	return "data:text/plain;charset=utf-8;base64," + encodedContent
}

// PtrTo returns a pointer to the given value
func PtrTo[T any](v T) *T {
	return &v
}

// RemoveAllMCDPods removes all MCD pods in openshift-machine-config-operator namespace
func RemoveAllMCDPods(oc *exutil.CLI) error {
	return removeMCOPods(oc, "-l", "k8s-app=machine-config-daemon")
}

// removeAllMCOPods removes all MCO pods in openshift-machine-config-operator namespace matching the given selector args
func removeMCOPods(oc *exutil.CLI, argsSelector ...string) error {
	args := append([]string{"pods", "-n", MachineConfigNamespace}, argsSelector...)
	err := oc.AsAdmin().WithoutNamespace().Run("delete").Args(args...).Execute()

	if err != nil {
		logger.Errorf("Cannot delete the pods in %s namespace", MachineConfigNamespace)
		return err
	}

	return nil
}

// waitForAllMCOPodsReady waits for all MCO pods to be running and ready
func waitForAllMCOPodsReady(oc *exutil.CLI, timeout time.Duration) error {
	logger.Infof("Waiting for MCO pods to be runnging and ready in namespace %s", MachineConfigNamespace)
	mcoPodsList := NewNamespacedResourceList(oc.AsAdmin(), "pod", MachineConfigNamespace)
	mcoPodsList.PrintDebugCommand()
	immediate := false
	// Returns the "ready" status value unless the pod is in "Completed" status, which is ignored
	template := `'{{- range .items -}}{{- range .status.conditions -}}{{- if ne .reason "PodCompleted" -}}{{- if eq .type "Ready" -}}{{- .status}} {{" "}}{{- end -}}{{- end -}}{{- end -}}{{- end -}}'`
	waitErr := wait.PollUntilContextTimeout(context.TODO(), 10*time.Second, timeout, immediate,
		func(_ context.Context) (bool, error) {
			status, err := mcoPodsList.GetTemplate(template)

			if err != nil {
				logger.Errorf("Problems getting pods info. Trying again")
				return false, nil
			}

			if strings.Contains(status, FalseString) {
				return false, nil
			}

			return strings.Contains(status, TrueString), nil
		})

	if waitErr != nil {
		_ = oc.AsAdmin().WithoutNamespace().Run("get").Args("pods", "-n", MachineConfigNamespace).Execute()
		return fmt.Errorf("MCO pods were deleted in namespace %s, but they did not become ready", MachineConfigNamespace)
	}

	return nil
}

// checkAllOperatorsHealthy fails the test if any ClusterOperator resource is degraded
func checkAllOperatorsHealthy(oc *exutil.CLI, timeout, poll string) {
	o.Eventually(func(gm o.Gomega) { // Passing o.Gomega as parameter we can use assertions inside the Eventually function without breaking the retries.
		ops, err := NewResourceList(oc.AsAdmin(), "co").GetAll()
		gm.Expect(err).NotTo(o.HaveOccurred(), "Could not get a list with all the clusteroperator resources")

		for _, op := range ops {
			gm.Expect(&op).NotTo(BeDegraded(), "%s is Degraded!. \n%s", op.PrettyString())
		}
	}, timeout, poll).
		Should(o.Succeed(),
			"There are degraded ClusterOperators!")
}

// WaitForStableCluster waits for the cluster to be stable for a minimum period
func WaitForStableCluster(oc *exutil.CLI, minimumStable, timeout string) error {
	err := oc.AsAdmin().Run("adm").Args("wait-for-stable-cluster", "--minimum-stable-period", minimumStable, "--timeout", timeout).Execute()
	if err != nil {
		oc.Run("get").Args("co").Execute()
	}
	return err
}

// GetAPIServerInternalURI returns the API server internal URI
func GetAPIServerInternalURI(oc *exutil.CLI) (string, error) {
	infra := NewResource(oc, "infrastructure", "cluster")
	apiServerInternalURI, err := infra.Get(`{.status.apiServerInternalURI}`)
	if err != nil {
		return "", err
	}

	return regexp.MustCompile(`^https*:\/\/(.*):\d+$`).ReplaceAllString(strings.TrimSpace(apiServerInternalURI), `$1`), nil
}

// splitCommandString splits a string taking into account double quotes and single quotes, unscaping the quotes if necessary
func splitCommandString(strCommand string) []string {
	command := []string{}
	insideDoubleQuote := false
	insideSingleQuote := false

	isSingleQuote := func(b byte) bool {
		return b == '\'' && !insideDoubleQuote
	}

	isDoubleQuote := func(b byte) bool {
		return b == '"' && !insideSingleQuote
	}

	arg := []byte{}
	for _, char := range []byte(strings.TrimSpace(strCommand)) {
		if isDoubleQuote(char) {

			// skip the first character of the quote
			if !insideDoubleQuote {
				insideDoubleQuote = true
				continue
			}
			// we are inside a quote
			// if the new double quote is scaped we unscape it and continue inside a quote
			if arg[len(arg)-1] == '\\' {
				arg[len(arg)-1] = '"'
				continue
			}

			// If there is no scaped char the we get out of the quote state ignoring the last character of the quote
			insideDoubleQuote = false
			continue

		}

		if isSingleQuote(char) {
			// skip the first character of the quote
			if !insideSingleQuote {
				insideSingleQuote = true
				continue
			}
			// we are inside a quote
			// if the new single quote is scaped we unscape it and continue inside a quote
			if arg[len(arg)-1] == '\\' {
				arg[len(arg)-1] = '\''
				continue
			}

			// If there is no scaped char the we get out of the quote state ignoring the last character of the quote
			insideSingleQuote = false
			continue

		}

		if char == ' ' && !insideDoubleQuote && !insideSingleQuote {
			command = append(command, string(arg))
			arg = []byte{}
			continue
		}

		arg = append(arg, char)
	}
	if len(arg) > 0 {
		command = append(command, string(arg))
	}

	return command

}

func getMachineConfigControllerPod(oc *exutil.CLI) (string, error) {
	pod, err := oc.AsAdmin().WithoutNamespace().Run("get").Args("pod", "-n", MachineConfigNamespace, "-l", ControllerLabel+"="+ControllerLabelValue, "-o", `jsonpath={.items[?(@.status.phase=="Running")].metadata.name}`).Output()
	logger.Infof("machine-config-controller pod name is %s", pod)
	return pod, err
}

// skipTestIfWorkersCannotBeScaled skips the current test if the worker pool cannot be scaled via machineset
func skipTestIfWorkersCannotBeScaled(oc *exutil.CLI) {
	canBeScaled, err := WorkersCanBeScaled(oc)
	o.ExpectWithOffset(1, err).NotTo(o.HaveOccurred(), "Error decidign if worker nodes can be scaled using machinesets")

	if !canBeScaled {
		g.Skip("Worker nodes cannot be scaled using machinesets. This test cannot be execute if workers cannot be scaled via machineset, IPI clusters.")
	}
}

// getAllKubeProxyPod returns the kube-rbac-proxy- pod for given namespace
func getAllKubeProxyPod(oc *exutil.CLI, namespace string) ([]string, error) {
	var kubeRabcProxyPodList []string
	getKubeProxyPod, err := exutil.GetAllPods(oc.AsAdmin(), namespace)
	for i := range getKubeProxyPod {
		if strings.Contains(getKubeProxyPod[i], "kube-rbac-proxy-crio-") {
			kubeRabcProxyPodList = append(kubeRabcProxyPodList, getKubeProxyPod[i])
		}
	}
	if len(kubeRabcProxyPodList) == 0 {
		logger.Infof("Empty kube-rbac-proxy-crio- pod list")
		return kubeRabcProxyPodList, err
	}
	logger.Infof("Get all Kube proxy pod list: %s", kubeRabcProxyPodList)
	return kubeRabcProxyPodList, err
}

// validate that the machine config 'mc' degrades machineconfigpool 'mcp', due to RenderDegraded error matching expectedNDMessage, expectedNDReason
func validateMcpRenderDegraded(mc *MachineConfig, mcp *MachineConfigPool, expectedRDMessage, expectedRDReason string) {
	defer o.Eventually(mcp, "5m", "20s").ShouldNot(BeDegraded(), "The MCP was not recovered from Degraded status after deleting the offending MC")
	defer o.Eventually(mc.Delete).Should(o.Succeed(), "Could not delete the offending MC")
	mc.create()
	logger.Infof("OK!\n")

	checkDegraded(mcp, expectedRDMessage, expectedRDReason, "RenderDegraded", false, 2)
}

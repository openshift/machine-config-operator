package extended

import (
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	exutil "github.com/openshift/machine-config-operator/test/extended/util"
	logger "github.com/openshift/machine-config-operator/test/extended/util/logext"
	"github.com/tidwall/sjson"

	g "github.com/onsi/ginkgo/v2"
	o "github.com/onsi/gomega"
	e2e "k8s.io/kubernetes/test/e2e/framework"
)

// fixturePathCache to store fixture path mapping, key: dir name under testdata, value: fixture path
var fixturePathCache = make(map[string]string)

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
	mcoDirName := "mco"
	mcoBaseDir := ""
	if mcoBaseDir = fixturePathCache[mcoDirName]; mcoBaseDir == "" {
		logger.Infof("mco fixture dir is not initialized, start to create")
		mcoBaseDir = exutil.FixturePath("testdata", mcoDirName)
		fixturePathCache[mcoDirName] = mcoBaseDir
		logger.Infof("mco fixture dir is initialized: %s", mcoBaseDir)
	} else {
		mcoBaseDir = fixturePathCache[mcoDirName]
		logger.Debugf("mco fixture dir found in cache: %s", mcoBaseDir)
	}
	return filepath.Join(mcoBaseDir, fileName)
}

func sortNodeList(nodes []Node) []Node {
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
func sortMasterNodeList(oc *exutil.CLI, nodes []Node) ([]Node, error) {
	masterSortedNodes := []Node{}
	operatorNode, err := GetOperatorNode(oc)
	if err != nil {
		return nil, err
	}

	logger.Infof("MCO operator pod running in node: %s", operatorNode)

	var latestNode Node
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

// preChecks executes some basic checks to make sure the the cluster is healthy enough to run MCO test cases
func preChecks(oc *exutil.CLI) {
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

// getEnabledFeatureGates get enabled featuregates list
func getEnabledFeatureGates(oc *exutil.CLI) ([]string, error) {
	enabledFeatureGates, err := NewResource(oc.AsAdmin(), "featuregate", "cluster").Get(`{.status.featureGates[0].enabled[*].name}`)
	if err != nil {
		return nil, err
	}

	return strings.Split(enabledFeatureGates, " "), nil
}

// IsFeaturegateEnabled check whether a featuregate is in enabled or not
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

// GetInternalIgnitionConfigURL return the API server internal uri without any port and without the protocol
func GetAPIServerInternalURI(oc *exutil.CLI) (string, error) {
	infra := NewResource(oc, "infrastructure", "cluster")
	apiServerInternalURI, err := infra.Get(`{.status.apiServerInternalURI}`)
	if err != nil {
		return "", err
	}

	return regexp.MustCompile(`^https*:\/\/(.*):\d+$`).ReplaceAllString(strings.TrimSpace(apiServerInternalURI), `$1`), nil
}

// IsCompactOrSNOCluster returns true if the current cluster is a Compact cluster or a SNO cluster
func IsCompactOrSNOCluster(oc *exutil.CLI) bool {
	var (
		wMcp    = NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolWorker)
		mcpList = NewMachineConfigPoolList(oc.AsAdmin())
	)

	return wMcp.IsEmpty() && len(mcpList.GetAllOrFail()) == 2
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

func extractJournalLogs(oc *exutil.CLI, outDir string) (totalErr error) {
	var (
		nl = NewNodeList(oc)
	)

	logger.Infof("Collecting journal logs")
	allNodes, err := nl.GetAll()
	if err != nil {
		return err
	}
	for _, node := range allNodes {
		if node.HasTaintEffectOrFail("NoExecute") {
			logger.Infof("Node %s is tainted with 'NoExecute'. Validation skipped.", node.GetName())
			continue
		}
		if node.GetConditionStatusByType("DiskPressure") != FalseString {
			logger.Infof("Node %s is under disk pressure. The node cannot be debugged. We skip the validation for this node", node.GetName())
			continue
		}

		logger.Infof("Collecting journal logs from node: %s", node.GetName())

		fileName := path.Join(outDir, node.GetName()+"-journal.log")
		tmpFilePath := path.Join("/tmp/journal.log")

		_, err := node.DebugNodeWithChroot("bash", "-c", "journalctl -o with-unit >  "+tmpFilePath)
		if err != nil {
			totalErr = err
			logger.Infof("Error getting journal logs from node %s: %s", node.GetName(), err)
			continue
		}
		err = node.CopyToLocal(tmpFilePath, fileName)
		if err != nil {
			totalErr = err
			logger.Infof("Error copying the file with the journal logs from node %s: %s", node.GetName(), err)
			continue
		}
	}

	return totalErr
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

// splitCommandString splits a string taking into account double quotes and single quotes, unscaping the quotes if necessary
// Example. Split this:
//
//	command "with \"escaped\" double quotes" and 'with \'escaped\' single quotes' and simple params
//
// Result:
// - command
// - with "escaped" double quotes
// - and
// - with 'escaped' single quotes
// - and
// - simple
// - params
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

	r := regexp.MustCompile(`-(?P<id>\d+)-`)

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

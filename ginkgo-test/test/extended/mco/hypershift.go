package mco

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"

	o "github.com/onsi/gomega"
	exutil "github.com/openshift/machine-config-operator/ginkgo-test/test/extended/util"
	logger "github.com/openshift/machine-config-operator/ginkgo-test/test/extended/util/logext"
)

// HypershiftCli will be used to execute hypershift command e.g. hypershift install etc.
type HypershiftCli struct{}

// CliOptions interface to describe cli option test requirement
type CliOptions interface {
	ToArgs() []string
}

// AbstractCliOptions base struct to impl CliOptions
type AbstractCliOptions struct {
	options []OptionField
}

// OptionField describe data structure of cli option
type OptionField struct {
	name, value string
}

// AwsInstallOptions install option impl for aws
type AwsInstallOptions struct {
	AbstractCliOptions
}

// AwsCreateClusterOptions create cluster option impl for aws
type AwsCreateClusterOptions struct {
	AbstractCliOptions
}

// AwsDestroyClusterOptions destroy cluster option impl for aws
type AwsDestroyClusterOptions struct {
	AbstractCliOptions
}

// AwsCreateNodePoolOptions create node pool option impl for aws
type AwsCreateNodePoolOptions struct {
	AbstractCliOptions
}

// ToArgs base class to impl interface of CliOptions
func (bc *AbstractCliOptions) ToArgs() []string {
	return bc.transform(bc.options)
}

// appendOption append option with k,v
func (bc *AbstractCliOptions) appendOption(k, v string) {
	bc.options = append(
		bc.options,
		OptionField{
			name:  k,
			value: v,
		})
}

// transform the option fields to cli options
func (bc *AbstractCliOptions) transform(options []OptionField) []string {

	args := []string{}

	for _, field := range options {
		if field.value != "" {
			args = append(args, field.name, field.value)
		} else {
			args = append(args, field.name)
		}
	}

	return args
}

// NewAwsInstallOptions constructor of aws install options
func NewAwsInstallOptions() *AwsInstallOptions {
	return &AwsInstallOptions{
		AbstractCliOptions{
			options: []OptionField{},
		},
	}
}

// WithBucket builder func to append option s3 bucket name
func (opt *AwsInstallOptions) WithBucket(bucket string) *AwsInstallOptions {
	opt.appendOption("--oidc-storage-provider-s3-bucket-name", bucket)
	return opt
}

// WithCredential builder func to append option s3 cred
func (opt *AwsInstallOptions) WithCredential(cred string) *AwsInstallOptions {
	opt.appendOption("--oidc-storage-provider-s3-credentials", cred)
	return opt
}

// WithRegion builder func to append option s3 region
func (opt *AwsInstallOptions) WithRegion(region string) *AwsInstallOptions {
	opt.appendOption("--oidc-storage-provider-s3-region", region)
	return opt
}

// WithNamespace builder func to append option s3 region
func (opt *AwsInstallOptions) WithNamespace(ns string) *AwsInstallOptions {
	opt.appendOption("--namespace", ns)
	return opt
}

// WithEnableDefaultingWebhook builder func to append option enable-defaulting-webhook
func (opt *AwsInstallOptions) WithEnableDefaultingWebhook() *AwsInstallOptions {
	opt.appendOption("--enable-defaulting-webhook", "true")
	return opt
}

// WithHypershiftImage builder func to append option enable-defaulting-webhook
func (opt *AwsInstallOptions) WithHypershiftImage(image string) *AwsInstallOptions {
	opt.appendOption("--hypershift-image", image)
	return opt
}

// NewAwsCreateClusterOptions constructor of create cluster on aws options
func NewAwsCreateClusterOptions() *AwsCreateClusterOptions {
	opts := &AwsCreateClusterOptions{
		AbstractCliOptions{
			options: []OptionField{},
		},
	}
	opts.appendOption("aws", "")

	return opts
}

// WithName builder func to append option name
func (opt *AwsCreateClusterOptions) WithName(name string) *AwsCreateClusterOptions {
	opt.appendOption("--name", name)
	return opt
}

// WithPullSecret builder func to append option pull-secret
func (opt *AwsCreateClusterOptions) WithPullSecret(ps string) *AwsCreateClusterOptions {
	opt.appendOption("--pull-secret", ps)
	return opt
}

// WithAwsCredential builder func to append option aws-cred
func (opt *AwsCreateClusterOptions) WithAwsCredential(cred string) *AwsCreateClusterOptions {
	opt.appendOption("--aws-creds", cred)
	return opt
}

// WithBaseDomain builder func to append option base domain
func (opt *AwsCreateClusterOptions) WithBaseDomain(domain string) *AwsCreateClusterOptions {
	opt.appendOption("--base-domain", domain)
	return opt
}

// WithNodePoolReplica builder func to append option node pool replica
func (opt *AwsCreateClusterOptions) WithNodePoolReplica(replica string) *AwsCreateClusterOptions {
	opt.appendOption("--node-pool-replicas", replica)
	return opt
}

// WithRegion builder func to append option region
func (opt *AwsCreateClusterOptions) WithRegion(region string) *AwsCreateClusterOptions {
	opt.appendOption("--region", region)
	return opt
}

// WithReleaseImage builder func to append option release-image
func (opt *AwsCreateClusterOptions) WithReleaseImage(releaseImage string) *AwsCreateClusterOptions {
	opt.appendOption("--release-image", releaseImage)
	return opt
}

// NewAwsDestroyClusterOptions constructor of destroy cluster on aws options
func NewAwsDestroyClusterOptions() *AwsDestroyClusterOptions {
	opts := &AwsDestroyClusterOptions{
		AbstractCliOptions{
			options: []OptionField{},
		},
	}
	opts.appendOption("aws", "")

	return opts
}

// WithName builder func to append option name
func (opt *AwsDestroyClusterOptions) WithName(name string) *AwsDestroyClusterOptions {
	opt.appendOption("--name", name)
	return opt
}

// WithAwsCredential builder func to append option aws-cred
func (opt *AwsDestroyClusterOptions) WithAwsCredential(cred string) *AwsDestroyClusterOptions {
	opt.appendOption("--aws-creds", cred)
	return opt
}

// WithDestroyCloudResource builder func to append option destroy cloud resource
func (opt *AwsDestroyClusterOptions) WithDestroyCloudResource() *AwsDestroyClusterOptions {
	opt.appendOption("--destroy-cloud-resources", "")
	return opt
}

// WithNamespace builder func to append option namespace
func (opt *AwsDestroyClusterOptions) WithNamespace(ns string) *AwsDestroyClusterOptions {
	opt.appendOption("--namespace", ns)
	return opt
}

// NewAwsCreateNodePoolOptions constructor of create node pool options
func NewAwsCreateNodePoolOptions() *AwsCreateNodePoolOptions {
	opts := &AwsCreateNodePoolOptions{
		AbstractCliOptions{
			options: []OptionField{},
		},
	}
	opts.appendOption("aws", "")

	return opts
}

// WithClusterName builder func to append option cluster
func (opt *AwsCreateNodePoolOptions) WithClusterName(cluster string) *AwsCreateNodePoolOptions {
	opt.appendOption("--cluster-name", cluster)
	return opt
}

// WithName builder func to append option name
func (opt *AwsCreateNodePoolOptions) WithName(name string) *AwsCreateNodePoolOptions {
	opt.appendOption("--name", name)
	return opt
}

// WithNodeCount builder func to append option node count
func (opt *AwsCreateNodePoolOptions) WithNodeCount(count string) *AwsCreateNodePoolOptions {
	opt.appendOption("--node-count", count)
	return opt
}

// WithRender builder func to append option render
func (opt *AwsCreateNodePoolOptions) WithRender() *AwsCreateNodePoolOptions {
	opt.appendOption("--render", "")
	return opt
}

// WithNamespace builder func to append option namespace
func (opt *AwsCreateNodePoolOptions) WithNamespace(ns string) *AwsCreateNodePoolOptions {
	opt.appendOption("--namespace", ns)
	return opt
}

// Install install hypershift operator with cli operations
func (hc *HypershiftCli) Install(options CliOptions) (string, error) {
	return execHypershift(
		append([]string{"install"},
			options.ToArgs()...))
}

// Uninstall uninstall hypershift operator from OCP
func (hc *HypershiftCli) Uninstall() (string, error) {
	return execBash([]string{
		"hypershift install render --format=yaml | oc delete -f -"})
}

// CreateCluster create hosted cluster on different platforms e.g. aws or auzre
func (hc *HypershiftCli) CreateCluster(options CliOptions) (string, error) {
	return execHypershift(
		append([]string{"create", "cluster"},
			options.ToArgs()...))
}

// DestroyCluster destroy hosted cluster with cli options
func (hc *HypershiftCli) DestroyCluster(options CliOptions) (string, error) {
	return execHypershift(
		append([]string{"destroy", "cluster"},
			options.ToArgs()...))
}

// CreateKubeConfig create kubeconfig for hosted cluster
func (hc *HypershiftCli) CreateKubeConfig(clusterName, namespace, filePath string) (string, error) {
	return execBash([]string{
		fmt.Sprintf("hypershift create kubeconfig --name %s --namespace %s > %s", clusterName, namespace, filePath)})
}

// CreateNodePool create node pool with cli options
func (hc *HypershiftCli) CreateNodePool(options CliOptions) (string, error) {
	return execHypershift(
		append([]string{"create", "nodepool"},
			options.ToArgs()...))
}

// wrapper func to execute hypershift cli
func execHypershift(args []string) (string, error) {
	return execCmd("hypershift", args)
}

// wrapper func to execute bash command
func execBash(args []string) (string, error) {
	return execCmd(
		"bash",
		append([]string{"-c"}, args...))
}

// execute cmd, handle std,stderr separately, support debug logging
// if error occurred, log stderr, otherwise return stdout
// tip: hypershift cli redirects some logs to stderr
func execCmd(name string, args []string) (string, error) {
	cmd := exec.Command(name, args...)
	// handle errbuffer separately
	var errbuffer bytes.Buffer
	cmd.Stderr = &errbuffer
	outbytes, err := cmd.Output()
	stdout := string(outbytes)
	stderr := string(errbuffer.Bytes())
	// print cmdline
	logger.Infof("running cmd: %s %s", name, strings.Join(args, " "))
	// print debug log for stdout
	logger.Debugf("cmd stdout: %s", stdout)
	logger.Debugf("cmd stderr: %s", stderr)
	if err != nil {
		logger.Errorf("cmd exec failed: %v", err)
		if cmd.Stderr != nil {
			logger.Errorf("cmd stderr: %s", stderr)
		}
	}
	return stdout, err
}

// HypershiftHostedCluster hostedcluster struct, extends resource
type HypershiftHostedCluster struct {
	Resource
}

// NewHypershiftHostedCluster constructor of hostedcluster struct
func NewHypershiftHostedCluster(oc *exutil.CLI, clusterNs, name string) *HypershiftHostedCluster {
	return &HypershiftHostedCluster{
		Resource: *NewNamespacedResource(oc, HypershiftCrHostedCluster, clusterNs, name),
	}
}

// GetVersion will return hosted cluster version
func (hc *HypershiftHostedCluster) GetVersion() string {
	return hc.GetOrFail(`{.status.version.desired.version}`)
}

// HypershiftNodePool node pool struct, extends resource
type HypershiftNodePool struct {
	Resource
}

// NewHypershiftNodePool constructor of node pool struct
func NewHypershiftNodePool(oc *exutil.CLI, clusterNs, name string) *HypershiftNodePool {
	return &HypershiftNodePool{
		Resource: *NewNamespacedResource(oc, HypershiftCrNodePool, clusterNs, name),
	}
}

// IsReady check node pool is ready or not. expected is desired nodes = current nodes
func (np *HypershiftNodePool) IsReady() bool {
	logger.Infof("checking nodepool %s is ready", np.name)
	// we consider if desired nodes == current nodes, nodepool is ready
	desiredNodes := np.GetOrFail("{.spec.replicas}")
	currentNodes := np.GetOrFail("{.status.replicas}")

	logger.Infof("desired nodes # is %s", desiredNodes)
	logger.Infof("current nodes # is %s", currentNodes)

	return desiredNodes == currentNodes
}

// WaitUntilReady wait for node pool is ready
func (np *HypershiftNodePool) WaitUntilReady() {
	logger.Infof("wait nodepool %s to be ready", np.GetName())
	replicas := np.GetOrFail(`{.spec.replicas}`)
	o.Eventually(
		np.Poll(`{.status.replicas}`),
		np.EstimateTimeoutInMins(), "30s").
		Should(o.Equal(replicas), "current nodes should equal to desired nodes %s. Nodepool:\n%s", replicas, np.PrettyString())
	logger.Infof("nodepool %s is ready", np.GetName())
}

// WaitUntilConfigIsUpdating wait for config update to start
func (np *HypershiftNodePool) WaitUntilConfigIsUpdating() {
	logger.Infof("wait condition UpdatingConfig to be true")
	o.Eventually(func() map[string]interface{} {
		updatingConfig := JSON(np.GetConditionByType("UpdatingConfig"))
		if updatingConfig.Exists() {
			return updatingConfig.ToMap()
		}
		logger.Infof("condition UpdatingConfig not found")
		return nil
	}, "5m", "2s").Should(o.HaveKeyWithValue("status", "True"), "UpdatingConfig condition status should be 'True'. Nodepool:\n%s", np.PrettyString())
	logger.Infof("status of condition UpdatingConfig is True")
}

// WaitUntilVersionIsUpdating wait for version update to start
func (np *HypershiftNodePool) WaitUntilVersionIsUpdating() {
	logger.Infof("wait condition UpdatingVersion to be true")
	o.Eventually(func() map[string]interface{} {
		updatingConfig := JSON(np.GetConditionByType("UpdatingVersion"))
		if updatingConfig.Exists() {
			return updatingConfig.ToMap()
		}
		logger.Infof("condition UpdatingVersion not found")
		return nil
	}, "5m", "2s").Should(o.HaveKeyWithValue("status", "True"), "UpdatingVersion condition status should be 'True'. Nodepool:\n%s", np.PrettyString())
	logger.Infof("status of condition UpdatingVersion is True")
}

// WaitUntilConfigUpdateIsCompleted poll condition UpdatingConfig until it is disappeared
func (np *HypershiftNodePool) WaitUntilConfigUpdateIsCompleted() {
	logger.Infof("wait nodepool %s config update to complete", np.GetName())
	o.Eventually(np.GetConditionStatusByType, np.EstimateTimeoutInMins(), "30s").WithArguments("UpdatingConfig").Should(o.Equal("False"),
		"config update is not completed. Nodepool:\n%s", np.PrettyString())

	logger.Infof("nodepool %s config update is completed", np.GetName())
}

// WaitUntilVersionUpdateIsCompleted poll condition UpdatingVersion until it is disappeared
func (np *HypershiftNodePool) WaitUntilVersionUpdateIsCompleted() {
	logger.Infof("wait nodepool %s version update to complete", np.GetName())
	o.Eventually(np.GetConditionStatusByType, np.EstimateTimeoutInMins(), "2s").WithArguments("UpdatingVersion").Should(o.Equal("False"),
		"version update is not completed. Nodepool:\n%s", np.PrettyString())

	logger.Infof("nodepool %s version update is completed", np.GetName())
}

// EstimateTimeoutInMins caculate wait timeout based on replica number
func (np *HypershiftNodePool) EstimateTimeoutInMins() string {
	desiredNodes, err := strconv.Atoi(np.GetOrFail(`{.spec.replicas}`))
	if err != nil {
		desiredNodes = 2 // use 2 replicas as default
	}
	return fmt.Sprintf("%dm", desiredNodes*15)
}

// GetAllLinuxNodes get all linux nodes in this nodepool
func (np *HypershiftNodePool) GetAllLinuxNodesOrFail() []Node {
	workerList := NewNodeList(np.oc.AsAdmin().AsGuestKubeconf())
	workerList.ByLabel("kubernetes.io/os=linux,hypershift.openshift.io/nodePool=" + np.GetName())
	nodes, getNodesErr := workerList.GetAll()
	o.Expect(getNodesErr).NotTo(o.HaveOccurred(), "list all linux nodes in new nodepool error")
	o.Expect(nodes).NotTo(o.BeEmpty(), "no linux node found for new nodepool")

	return nodes
}

// GetVersion get version of nodepool
func (np *HypershiftNodePool) GetVersion() string {
	return np.GetOrFail(`{.status.version}`)
}

// CloudCredential interface to describe test requirement for credential config of cloud platforms
type CloudCredential interface {
	OutputToFile(path string) error
}

// AwsCredential AWS impl of CloudCredential
type AwsCredential struct {
	accessKey,
	secretKey,
	region,
	profile,
	file string
}

// NewAwsCredential constructor of AwsCredential
func NewAwsCredential(oc *exutil.CLI, pf string) (*AwsCredential, error) {
	// get aws credential and region from cluster
	format := `template={{index .data "%s"|base64decode}}`
	// get access key
	ak, ake := oc.AsAdmin().WithoutNamespace().Run("get").Args("secrets/aws-creds", "-n", "kube-system", "-o", fmt.Sprintf(format, "aws_access_key_id")).Output()
	if ake != nil {
		logger.Errorf("get aws access key failed: %v", ake)
		return nil, ake
	}
	// get secret key
	sk, ske := oc.AsAdmin().WithoutNamespace().Run("get").Args("secrets/aws-creds", "-n", "kube-system", "-o", fmt.Sprintf(format, "aws_secret_access_key")).Output()
	if ske != nil {
		logger.Errorf("get aws secret key failed: %v", ske)
		return nil, ske
	}
	// get region
	r, re := NewResource(oc.AsAdmin(), "infrastructure", "cluster").Get("{.status.platformStatus.aws.region}")
	if re != nil {
		logger.Errorf("get aws region failed: %v", re)
		return nil, re
	}

	return &AwsCredential{
		accessKey: ak,
		secretKey: sk,
		region:    r,
		profile:   pf,
	}, nil
}

// OutputToFile imple interface CliOptions, save credential info to local config file
func (ac *AwsCredential) OutputToFile(path string) error {

	logger.Infof("save aws-cred info to %s", path)
	if ac.accessKey == "" || ac.secretKey == "" {
		return fmt.Errorf("cannot create aws credential file, accessKey or SecretKey is empty")
	}
	// if no profile found, [default] profile will be used
	if ac.profile == "" {
		ac.profile = "default"
	}
	content := fmt.Sprintf("[%s]\naws_access_key_id = %s\naws_secret_access_key = %s",
		ac.profile,
		ac.accessKey,
		ac.secretKey)
	// if the file exists, it will be truncated and permission will not be changed
	err := os.WriteFile(path, []byte(content), 0o600)
	ac.file = path
	return err
}

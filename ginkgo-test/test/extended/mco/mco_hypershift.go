package mco

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/openshift/machine-config-operator/ginkgo-test/test/extended/util/architecture"
	logger "github.com/openshift/machine-config-operator/ginkgo-test/test/extended/util/logext"

	g "github.com/onsi/ginkgo/v2"
	o "github.com/onsi/gomega"
	exutil "github.com/openshift/machine-config-operator/ginkgo-test/test/extended/util"
)

var _ = g.Describe("[sig-mco] MCO hypershift", func() {

	defer g.GinkgoRecover()

	var (
		// init cli object, temp namespace contains prefix mco.
		// tip: don't put this in BeforeEach/JustBeforeEach, you will get error
		// "You may only call AfterEach from within a Describe, Context or When"
		oc = exutil.NewCLIForKubeOpenShift("mco-hypershift")
		// temp dir to store all test files, and it will be recycled when test is finished
		tmpdir string
		// whether hypershift is enabled
		hypershiftEnabled bool
		// declare hypershift test driver
		ht *HypershiftTest
	)

	g.JustBeforeEach(func() {
		// check support platform for this test. only aws is support
		skipTestIfSupportedPlatformNotMatched(oc, "aws")
		preChecks(oc)

		tmpdir = createTmpDir()
		hypershiftEnabled = isHypershiftEnabled(oc)

		ht = &HypershiftTest{
			NewSharedContext(),
			oc,
			HypershiftCli{},
			GetCloudCredential(oc),
			tmpdir,
			exutil.GetHyperShiftHostedClusterNameSpace(oc),
		}

		// in hypershift enabled env, like prow or cluster installed with hypershift template
		// operator and hosted cluster are available by default
		// skip operator and hosted cluster install steps
		if !hypershiftEnabled {
			// create/recycle aws s3 bucket
			ht.CreateBucket()
			// install hypershift
			ht.InstallOnAws()
			// create hosted cluster w/o node pool
			ht.CreateClusterOnAws()
		} else {
			hostedClusterName := getFirstHostedCluster(oc)
			hostedClusterNs := exutil.GetHyperShiftHostedClusterNameSpace(oc)
			// OCPQE-16036 check hosted cluster platform type, we only support create nodepool on aws based hostedcluster.
			hostedClusterPlatform, err := exutil.GetHostedClusterPlatformType(oc, hostedClusterName, hostedClusterNs)
			o.Expect(err).NotTo(o.HaveOccurred(), "Get hostedcluster platform type failed")
			logger.Debugf("hostedcluster platform type is %s", hostedClusterPlatform)
			if hostedClusterPlatform != exutil.AWSPlatform {
				g.Skip(fmt.Sprintf("hostedcluster platform type [%s] is not aws, skip this test", hostedClusterPlatform))
			}
			ht.Put(TestCtxKeyCluster, hostedClusterName)
		}

	})

	g.JustAfterEach(func() {
		// only do clean up (destroy hostedcluster/uninstall hypershift/delete bucket) for env that hypershift is not pre-installed
		if !hypershiftEnabled {
			ht.DestroyClusterOnAws()
			ht.Uninstall()
			ht.DeleteBucket()
		}
		os.RemoveAll(tmpdir)
		logger.Infof("test dir %s is cleaned up", tmpdir)
	})

	g.It("HyperShiftMGMT-Author:rioliu-Longduration-NonPreRelease-High-54328-hypershift Add new file on hosted cluster node via config map [Disruptive]", func() {

		// create node pool with replica=2
		// destroy node pool then delete config map
		defer ht.DeleteMcConfigMap()
		defer ht.DestroyNodePoolOnAws()
		ht.CreateNodePoolOnAws("2")

		// create config map which contains machine config
		ht.CreateMcConfigMap()

		// patch node pool to update config name with new config map
		ht.PatchNodePoolToTriggerUpdate()

		// create kubeconfig for hosted cluster
		ht.CreateKubeConfigForCluster()

		// check machine config annotations on nodes to make sure update is done
		ht.CheckMcAnnotationsOnNode()

		// check file content on hosted cluster nodes
		ht.VerifyFileContent()

	})

	g.It("HyperShiftMGMT-Author:rioliu-Longduration-NonPreRelease-High-54366-hypershift Update release image of node pool [Disruptive]", func() {
		// check arch, only support amd64
		architecture.SkipNonAmd64SingleArch(oc)
		// check latest accepted build, if it is same as hostedcluster version, skip this case
		ht.skipTestIfLatestAcceptedBuildIsSameAsHostedClusterVersion()

		// create a nodepool with 2 replicas and enable in place upgrade
		defer ht.DestroyNodePoolOnAws()
		ht.CreateNodePoolOnAws("2")

		// patch nodepool with latest nightly build and wait until version update to complete
		// compare nodepool version and build version. they should be same
		ht.PatchNodePoolToUpdateReleaseImage()

		// create kubeconfig for hosted cluster
		ht.CreateKubeConfigForCluster()

		// check machine config annotations on nodes to make sure update is done
		ht.CheckMcAnnotationsOnNode()

	})

	g.It("HyperShiftMGMT-Author:rioliu-Longduration-NonPreRelease-High-55356-hypershift Honor MaxUnavailable for inplace upgrades [Disruptive]", func() {

		// create node pool with replica=3
		// destroy node pool then delete config map
		defer ht.DeleteMcConfigMap()
		defer ht.DestroyNodePoolOnAws()
		ht.CreateNodePoolOnAws("3") // TODO: change the replica to 5 when bug https://issues.redhat.com/browse/OCPBUGS-2870 is fixed

		// create config map which contains machine config
		ht.CreateMcConfigMap()

		// patch node pool to update config name with new config map
		ht.PatchNodePoolToUpdateMaxUnavailable("2")

		// create kubeconfig for hosted cluster
		ht.CreateKubeConfigForCluster()

		// check whether nodes are updating in parallel
		ht.CheckNodesAreUpdatingInParallel(2)

	})
})

// GetCloudCredential get cloud credential impl by platform name
func GetCloudCredential(oc *exutil.CLI) CloudCredential {
	var (
		cc CloudCredential
		ce error
	)
	platform := exutil.CheckPlatform(oc)
	switch platform {
	case "aws":
		cc, ce = NewAwsCredential(oc, "default")
		o.Expect(ce).NotTo(o.HaveOccurred(), "extract aws cred from cluster failed")
	default:
		logger.Infof("no impl of CloudCredential for platform %s right now", platform)
	}
	return cc
}

// HypershiftTest tester for hypershift, contains required tool e.g client, cli, cred, shared context etc.
type HypershiftTest struct {
	*SharedContext
	oc             *exutil.CLI
	cli            HypershiftCli
	cred           CloudCredential
	dir, clusterNS string
}

// InstallOnAws install hypershift on aws
func (ht *HypershiftTest) InstallOnAws() {

	exutil.By("install hypershift operator")

	awscred := ht.cred.(*AwsCredential)
	_, installErr := ht.cli.Install(
		NewAwsInstallOptions().
			WithBucket(ht.StrValue(TestCtxKeyBucket)).
			WithCredential(awscred.file).
			WithRegion(awscred.region).
			WithEnableDefaultingWebhook().
			WithHypershiftImage(ht.getHypershiftImage()))
	o.Expect(installErr).NotTo(o.HaveOccurred(), "install hypershift operator via cli failed")

	// check whether pod under ns hypershift is running
	exutil.AssertAllPodsToBeReady(ht.oc, "hypershift")

	logger.Infof("hypershift is installed on AWS successfully")
}

// Uninstall uninstall hypershift
func (ht *HypershiftTest) Uninstall() {
	ht.cli.Uninstall()
}

// CreateBucket create s3 bucket
func (ht *HypershiftTest) CreateBucket() {

	exutil.By("configure aws-cred file with default profile")

	const (
		bucketPolicyTemplate = `{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect": "Allow",
      "Principal": "*",
      "Action": "s3:GetObject",
      "Resource": "arn:aws:s3:::%s/*"
    }
  ]
}`
	)

	// create a temp file to store aws credential in shared temp dir
	credfile := generateTempFilePath(ht.dir, "aws-cred-*.conf")
	// call CloudCredential#OutputToFile to write cred info to temp file
	o.Expect(ht.cred.OutputToFile(credfile)).NotTo(o.HaveOccurred(), "write aws cred to file failed")
	exutil.By("create s3 bucket for installer")
	// get aws cred
	awscred := ht.cred.(*AwsCredential)
	// get infra name as part of bucket name
	infraName := NewResource(ht.oc.AsAdmin(), "infrastructure", "cluster").GetOrFail("{.status.infrastructureName}")
	// bucket name pattern: $infraName-$component-$region-$randstr e.g. rioliu-092301-mvw2f-hypershift-us-east-2-glnjmsex
	bucket := fmt.Sprintf("%s-hypershift-%s-%s", infraName, awscred.region, exutil.GetRandomString())
	ht.Put(TestCtxKeyBucket, bucket)
	// init s3 client
	s3 := exutil.NewS3ClientFromCredFile(awscred.file, "default", awscred.region)
	// create bucket if it does not exists
	o.Expect(s3.CreateBucket(bucket)).NotTo(o.HaveOccurred(), "create aws s3 bucket %s failed", bucket)
	policy := fmt.Sprintf(bucketPolicyTemplate, bucket)
	o.Expect(s3.PutBucketPolicy(bucket, policy)).To(o.Succeed(), "an error happened while adding a policy to the bucket")
}

// DeleteBucket delete s3 bucket
func (ht *HypershiftTest) DeleteBucket() {

	exutil.By("delete s3 bucket to recycle cloud resource")

	// get aws cred
	awscred := ht.cred.(*AwsCredential)
	// init s3 client
	s3 := exutil.NewS3ClientFromCredFile(awscred.file, "default", awscred.region)
	// delete bucket, ignore not found
	bucketName := ht.StrValue(TestCtxKeyBucket)
	o.Expect(s3.DeleteBucket(bucketName)).NotTo(o.HaveOccurred(), "delete aws s3 bucket %s failed", bucketName)
}

// CreateClusterOnAws create hosted cluster on aws
func (ht *HypershiftTest) CreateClusterOnAws() {

	exutil.By("extract pull-secret from namespace openshift-config")

	// extract pull secret and save it to temp dir
	secret := NewSecret(ht.oc.AsAdmin(), "openshift-config", "pull-secret")
	o.Expect(
		secret.ExtractToDir(ht.dir)).
		NotTo(o.HaveOccurred(),
			fmt.Sprintf("extract pull-secret from openshift-config to %s failed", ht.dir))
	secretFile := filepath.Join(ht.dir, ".dockerconfigjson")
	logger.Infof("pull-secret info is saved to %s", secretFile)

	exutil.By("get base domain from resource dns/cluster")

	baseDomain := getBaseDomain(ht.oc)
	logger.Infof("based domain is: %s", baseDomain)

	exutil.By("create hosted cluster on AWS")
	name := fmt.Sprintf("mco-cluster-%s", exutil.GetRandomString())
	ht.Put(TestCtxKeyCluster, name)
	awscred := ht.cred.(*AwsCredential)
	createClusterOpts := NewAwsCreateClusterOptions().
		WithAwsCredential(awscred.file).
		WithBaseDomain(baseDomain).
		WithPullSecret(secretFile).
		WithRegion(awscred.region).
		WithName(name)

	_, createClusterErr := ht.cli.CreateCluster(createClusterOpts)
	o.Expect(createClusterErr).NotTo(o.HaveOccurred(), "create hosted cluster on aws failed")

	ht.clusterNS = exutil.GetHyperShiftHostedClusterNameSpace(ht.oc)
	logger.Infof("the hosted cluster namespace is: %s", ht.clusterNS)

	// wait for hosted control plane is available
	defer ht.oc.AsAdmin().Run("get").Args("-n", fmt.Sprintf("%s-%s", ht.clusterNS, name), "pods").Execute() // for debugging purpose
	exutil.AssertAllPodsToBeReadyWithPollerParams(ht.oc, fmt.Sprintf("%s-%s", ht.clusterNS, name), 30*time.Second, 10*time.Minute)

	logger.Infof("hosted cluster %s is created successfully on AWS", name)
}

// DestroyClusterOnAws destroy hosted cluster on aws
func (ht *HypershiftTest) DestroyClusterOnAws() {

	clusterName := ht.StrValue(TestCtxKeyCluster)

	exutil.By(fmt.Sprintf("destroy hosted cluster %s", clusterName))

	awscred := ht.cred.(*AwsCredential)
	destroyClusterOpts := NewAwsDestroyClusterOptions().
		WithName(clusterName).
		WithAwsCredential(awscred.file).
		WithDestroyCloudResource()

	_, destroyClusterErr := ht.cli.DestroyCluster(destroyClusterOpts)
	o.Expect(destroyClusterErr).NotTo(o.HaveOccurred(), fmt.Sprintf("destroy hosted cluster %s failed", clusterName))

	logger.Infof(fmt.Sprintf("hosted cluster %s is destroyed successfully", clusterName))
}

// CreateNodePoolOnAws create node pool on aws
// param: replica nodes # in node pool
func (ht *HypershiftTest) CreateNodePoolOnAws(replica string) {

	exutil.By("create rendered node pool")

	clusterName := ht.StrValue(TestCtxKeyCluster)
	name := fmt.Sprintf("%s-np-%s", clusterName, exutil.GetRandomString())
	renderNodePoolOpts := NewAwsCreateNodePoolOptions().
		WithName(name).
		WithClusterName(clusterName).
		WithNodeCount(replica).
		WithNamespace(ht.clusterNS).
		WithRender()

	renderedNp, renderNpErr := ht.cli.CreateNodePool(renderNodePoolOpts)
	o.Expect(renderNpErr).NotTo(o.HaveOccurred(), fmt.Sprintf("create node pool %s failed", name))
	o.Expect(renderedNp).NotTo(o.BeEmpty(), "rendered nodepool is empty")
	// replace upgradeType to InPlace
	renderedNp = strings.ReplaceAll(renderedNp, "Replace", "InPlace")
	logger.Infof("change upgrade type from Replace to InPlace in rendered node pool")
	// write rendered node pool to temp file
	renderedFile := filepath.Join(ht.dir, fmt.Sprintf("%s-%s.yaml", name, exutil.GetRandomString()))
	o.Expect(
		os.WriteFile(renderedFile, []byte(renderedNp), 0o600)).
		NotTo(o.HaveOccurred(), fmt.Sprintf("write rendered node pool to %s failed", renderedFile))
	logger.Infof("rendered node pool is saved to file %s", renderedFile)

	// apply updated node pool file
	o.Expect(
		ht.oc.AsAdmin().WithoutNamespace().Run("apply").Args("-f", renderedFile).Execute()).
		NotTo(o.HaveOccurred(), "create rendered node pool failed")

	logger.Infof("poll node pool status, expected is desired nodes == current nodes")
	np := NewHypershiftNodePool(ht.oc.AsAdmin(), ht.clusterNS, name)
	logger.Debugf(np.PrettyString())

	// poll node pool state, expected is desired nodes == current nodes
	np.WaitUntilReady()

	ht.Put(TestCtxKeyNodePool, name)

}

// DestroyNodePoolOnAws delete node pool related awsmachine first, then delete node pool
func (ht *HypershiftTest) DestroyNodePoolOnAws() {

	exutil.By("destroy nodepool related resources")

	logger.Infof("delete node pool related machines")

	npName := ht.StrValue(TestCtxKeyNodePool)
	clusterName := ht.StrValue(TestCtxKeyCluster)
	awsMachines, getAwsMachineErr := NewNamespacedResourceList(ht.oc.AsAdmin(), HypershiftAwsMachine, fmt.Sprintf("%s-%s", ht.clusterNS, clusterName)).GetAll()
	o.Expect(getAwsMachineErr).NotTo(o.HaveOccurred(), "get awsmachines failed for hosted cluster %s", clusterName)
	o.Expect(awsMachines).ShouldNot(o.BeEmpty())
	for _, machine := range awsMachines {
		clonedFromName := machine.GetAnnotationOrFail(`cluster.x-k8s.io/cloned-from-name`)
		if clonedFromName == npName {
			logger.Infof("deleting awsmachine %s", machine.GetName())
			deleteMachineErr := machine.Delete()
			if deleteMachineErr != nil {
				// here we just log the error, will not terminate the clean up process.
				// if any of the deletion is failed, it will be recycled by hypershift
				logger.Errorf("delete awsmachine %s failed\n %v", machine.GetName(), deleteMachineErr)
			} else {
				logger.Infof("awsmachine %s is deleted successfully", machine.GetName())
			}
		}
	}

	logger.Infof("all the awsmachines of nodepool %s are deleted", npName)

	NewNamespacedResource(ht.oc.AsAdmin(), HypershiftCrNodePool, ht.clusterNS, npName).DeleteOrFail()

	logger.Infof("nodepool %s is deleted successfully", npName)

}

// CreateMcConfigMap create config map contains machine config
func (ht *HypershiftTest) CreateMcConfigMap() {

	exutil.By("create machine config in config map")

	template := generateTemplateAbsolutePath(TmplHypershiftMcConfigMap)
	cmName := fmt.Sprintf("mc-cm-%s", exutil.GetRandomString())
	mcName := fmt.Sprintf("99-mc-test-%s", exutil.GetRandomString())
	mcpName := MachineConfigPoolWorker
	filePath := fmt.Sprintf("/home/core/test-%s", exutil.GetRandomString())
	exutil.ApplyNsResourceFromTemplate(
		ht.oc.AsAdmin(),
		ht.clusterNS,
		"--ignore-unknown-parameters=true",
		"-f", template,
		"-p",
		"CMNAME="+cmName,
		"MCNAME="+mcName,
		"POOL="+mcpName,
		"FILEPATH="+filePath,
	)

	// get config map to check it exists or not
	cm := NewNamespacedResource(ht.oc.AsAdmin(), "cm", ht.clusterNS, cmName)
	o.Expect(cm.Exists()).Should(o.BeTrue(), "mc config map does not exist")
	logger.Debugf(cm.PrettyString())
	logger.Infof("config map %s is created successfully", cmName)

	ht.Put(TestCtxKeyConfigMap, cmName)
	ht.Put(TestCtxKeyFilePath, filePath)

}

// DeleteMcConfigMap when node pool is destroyed, delete config map
func (ht *HypershiftTest) DeleteMcConfigMap() {

	exutil.By("delete config map")

	cmName := ht.StrValue(TestCtxKeyConfigMap)
	NewNamespacedResource(ht.oc.AsAdmin(), "cm", ht.clusterNS, cmName).DeleteOrFail()

	logger.Infof("config map %s is deleted successfully", cmName)
}

// PatchNodePoolToTriggerUpdate patch node pool to update config map
// this operation will trigger in-place update
func (ht *HypershiftTest) PatchNodePoolToTriggerUpdate() {

	exutil.By("patch node pool to add config setting")

	npName := ht.StrValue(HypershiftCrNodePool)
	cmName := ht.StrValue(TestCtxKeyConfigMap)
	np := NewHypershiftNodePool(ht.oc.AsAdmin(), ht.clusterNS, npName)
	o.Expect(np.Patch("merge", fmt.Sprintf(`{"spec":{"config":[{"name": "%s"}]}}`, cmName))).NotTo(o.HaveOccurred(), "patch node pool with cm setting failed")
	o.Expect(np.GetOrFail(`{.spec.config}`)).Should(o.ContainSubstring(cmName), "node pool does not have cm config")
	logger.Debugf(np.PrettyString())

	exutil.By("wait node pool update to complete")

	np.WaitUntilConfigIsUpdating()
	np.WaitUntilConfigUpdateIsCompleted()

}

// PatchNodePoolToUpdateReleaseImage patch node pool to update spec.release.image
// this operation will update os image on hosted cluster nodes
func (ht *HypershiftTest) PatchNodePoolToUpdateReleaseImage() {

	exutil.By("patch node pool to update release image")
	npName := ht.StrValue(HypershiftCrNodePool)
	np := NewHypershiftNodePool(ht.oc.AsAdmin(), ht.clusterNS, npName)
	versionSlice := strings.Split(np.GetVersion(), ".")
	imageURL, version := getLatestImageURL(ht.oc, fmt.Sprintf("%s.%s", versionSlice[0], versionSlice[1])) // get latest nightly build based on release version
	o.Expect(np.Patch("merge", fmt.Sprintf(`{"spec":{"release":{"image": "%s"}}}`, imageURL))).NotTo(o.HaveOccurred(), "patch node pool with release image failed")
	o.Expect(np.GetOrFail(`{.spec.release.image}`)).Should(o.ContainSubstring(imageURL), "node pool does not have update release image config")
	logger.Debugf(np.PrettyString())

	exutil.By("wait node pool update to complete")

	np.WaitUntilVersionIsUpdating()
	np.WaitUntilVersionUpdateIsCompleted()
	o.Expect(np.GetVersion()).Should(o.Equal(version), "version of node pool is not updated correctly")

}

// PatchNodePoolToUpdateMaxUnavailable update node pool to enable maxUnavailable support
func (ht *HypershiftTest) PatchNodePoolToUpdateMaxUnavailable(maxUnavailable string) {

	exutil.By("patch node pool to update property spec.management.inPlace.maxUnavailable and spec.config")

	npName := ht.StrValue(HypershiftCrNodePool)
	cmName := ht.StrValue(TestCtxKeyConfigMap)
	// update maxUnavailable
	np := NewHypershiftNodePool(ht.oc.AsAdmin(), ht.clusterNS, npName)
	o.Expect(np.Patch("merge", fmt.Sprintf(`{"spec":{"management":{"inPlace":{"maxUnavailable":%s}}}}`, maxUnavailable))).NotTo(o.HaveOccurred(), "patch node pool with maxUnavailable setting failed")
	o.Expect(np.GetOrFail(`{.spec.management.inPlace}`)).Should(o.ContainSubstring("maxUnavailable"), "node pool does not have maxUnavailable config")
	// update config
	o.Expect(np.Patch("merge", fmt.Sprintf(`{"spec":{"config":[{"name": "%s"}]}}`, cmName))).NotTo(o.HaveOccurred(), "patch node pool with cm setting failed")
	o.Expect(np.GetOrFail(`{.spec.config}`)).Should(o.ContainSubstring(cmName), "node pool does not have cm config")

	logger.Debugf(np.PrettyString())

	exutil.By("check node pool update is started")
	np.WaitUntilConfigIsUpdating()

}

// CheckNodesAreUpdatingInParallel check whether nodes are updating in parallel, nodeNum should be equal to maxUnavailable setting
func (ht *HypershiftTest) CheckNodesAreUpdatingInParallel(nodeNum int) {

	npName := ht.StrValue(HypershiftCrNodePool)
	np := NewHypershiftNodePool(ht.oc.AsAdmin(), ht.clusterNS, npName)
	defer np.WaitUntilConfigUpdateIsCompleted()

	exutil.By(fmt.Sprintf("checking whether nodes are updating in parallel, expected node num is %v", nodeNum))

	kubeconf := ht.StrValue(TestCtxKeyKubeConfig)
	ht.oc.SetGuestKubeconf(kubeconf)

	nodesInfo := ""
	mcStatePoller := func() int {
		nodeStates := NewNodeList(ht.oc.AsAdmin().AsGuestKubeconf()).McStateSnapshot()
		logger.Infof("machine-config state of all hosted cluster nodes: %s", nodeStates)
		nodesInfo, _ = ht.oc.AsAdmin().AsGuestKubeconf().Run("get").Args("node").Output()
		return strings.Count(nodeStates, "Working")
	}

	o.Eventually(mcStatePoller, "3m", "10s").Should(o.BeNumerically("==", nodeNum), "updating node num not equal to maxUnavailable value.\n%s", nodesInfo)
	o.Consistently(mcStatePoller, "8m", "10s").Should(o.BeNumerically("<=", nodeNum), "updating node num is greater than maxUnavailable value.\n%s", nodesInfo)

}

// CreateKubeConfigForCluster create kubeconfig for hosted cluster
func (ht *HypershiftTest) CreateKubeConfigForCluster() {

	exutil.By("create kubeconfig for hosted cluster")

	clusterName := ht.StrValue(TestCtxKeyCluster)
	file := filepath.Join(ht.dir, fmt.Sprintf("%s-kubeconfig", clusterName))
	_, err := ht.cli.CreateKubeConfig(clusterName, ht.clusterNS, file)
	o.Expect(err).NotTo(o.HaveOccurred(), fmt.Sprintf("create kubeconfig for cluster %s failed", clusterName))

	logger.Infof("kubeconfig of cluster %s is saved to %s", clusterName, file)

	ht.Put(TestCtxKeyKubeConfig, file)
}

// CheckMcAnnotationsOnNode check machine config is updated successfully
func (ht *HypershiftTest) CheckMcAnnotationsOnNode() {

	exutil.By("check machine config annotation to verify update is done")
	clusterName := ht.StrValue(TestCtxKeyCluster)
	kubeconf := ht.StrValue(TestCtxKeyKubeConfig)
	npName := ht.StrValue(HypershiftCrNodePool)
	ht.oc.SetGuestKubeconf(kubeconf)
	np := NewHypershiftNodePool(ht.oc.AsAdmin().AsGuestKubeconf(), ht.clusterNS, npName)
	workerNode := np.GetAllLinuxNodesOrFail()[0]

	// get machine config name
	secrets := NewNamespacedResourceList(ht.oc.AsAdmin(), "secrets", fmt.Sprintf("%s-%s", ht.clusterNS, clusterName))
	secrets.SortByTimestamp()
	secrets.ByFieldSelector("type=Opaque")
	secrets.SetItemsFilter("-1:")
	filterdSecrets, getSecretErr := secrets.GetAll()
	o.Expect(getSecretErr).NotTo(o.HaveOccurred(), "Get latest secret failed")
	userDataSecretName := filterdSecrets[0].GetName()
	logger.Infof("get latest user-data secret name %s", userDataSecretName)

	// mc name is suffix of the secret name e.g. user-data-inplace-upgrade-fe5d465e
	tempSlice := strings.Split(userDataSecretName, "-")
	mcName := tempSlice[len(tempSlice)-1]
	logger.Infof("machine config name is %s", mcName)

	logger.Debugf(workerNode.PrettyString())

	desiredConfig := workerNode.GetAnnotationOrFail(NodeAnnotationDesiredConfig)
	currentConfig := workerNode.GetAnnotationOrFail(NodeAnnotationCurrentConfig)
	desiredDrain := workerNode.GetAnnotationOrFail(NodeAnnotationDesiredDrain)
	lastAppliedDrain := workerNode.GetAnnotationOrFail(NodeAnnotationLastAppliedDrain)
	reason := workerNode.GetAnnotationOrFail(NodeAnnotationReason)
	state := workerNode.GetAnnotationOrFail(NodeAnnotationState)
	drainReqID := fmt.Sprintf("uncordon-%s", mcName)

	// do assertion for annotations, expected result is like below
	// desiredConfig == currentConfig
	o.Expect(currentConfig).Should(o.Equal(desiredConfig), "current config not equal to desired config")
	// desiredConfig = $mcName
	o.Expect(desiredConfig).Should(o.Equal(mcName))
	// currentConfig = $mcName
	o.Expect(currentConfig).Should(o.Equal(mcName))
	// desiredDrain == lastAppliedDrain
	o.Expect(desiredDrain).Should(o.Equal(lastAppliedDrain), "desired drain not equal to last applied drain")
	// desiredDrain = uncordon-$mcName
	o.Expect(desiredDrain).Should(o.Equal(drainReqID), "desired drain id is not expected")
	// lastAppliedDrain = uncordon-$mcName
	o.Expect(lastAppliedDrain).Should(o.Equal(drainReqID), "last applied drain id is not expected")
	// reason is empty
	o.Expect(reason).Should(o.BeEmpty(), "reason is not empty")
	// state is 'Done'
	o.Expect(state).Should(o.Equal("Done"))

}

// VerifyFileContent verify whether config file on node is expected
func (ht *HypershiftTest) VerifyFileContent() {

	exutil.By("check whether the test file content is matched ")
	filePath := ht.StrValue(TestCtxKeyFilePath)
	kubeconf := ht.StrValue(TestCtxKeyKubeConfig)
	npName := ht.StrValue(HypershiftCrNodePool)
	ht.oc.SetGuestKubeconf(kubeconf)
	np := NewHypershiftNodePool(ht.oc.AsAdmin().AsGuestKubeconf(), ht.clusterNS, npName)
	workerNode := np.GetAllLinuxNodesOrFail()[0]
	// when we call oc debug with guest kubeconfig, temp namespace oc.Namespace()
	// cannot be found in hosted cluster.
	// copy node object to change namespace to default
	clonedNode := workerNode
	clonedNode.oc.SetNamespace("default")
	rf := NewRemoteFile(clonedNode, filePath)
	o.Expect(rf.Fetch()).NotTo(o.HaveOccurred(), "fetch remote file failed")
	o.Expect(rf.GetTextContent()).Should(o.ContainSubstring("hello world"), "file content does not match machine config setting")

}

// skipTestIfLatestAcceptedBuildIsSameAsHostedClusterVersion, skip the test if latest accepted nightly build is same as hostedcluster version
func (ht *HypershiftTest) skipTestIfLatestAcceptedBuildIsSameAsHostedClusterVersion() {

	// OCPQE-17034, if latest accepted build is same as hosted cluster version. i.e. it is nodepool version as well
	// release image update will not happen, skip this case.

	// Get hosted cluster version
	hostedclusterName := ht.StrValue(TestCtxKeyCluster)
	hostedcluster := NewHypershiftHostedCluster(ht.oc.AsAdmin(), ht.clusterNS, hostedclusterName)
	hostedclusterVersion := hostedcluster.GetVersion()
	// Get latest accepted build
	_, latestAcceptedBuild := getLatestImageURL(ht.oc, strings.Join(strings.Split(hostedclusterVersion, ".")[:2], "."))

	if hostedclusterVersion == latestAcceptedBuild {
		g.Skip(fmt.Sprintf("latest accepted build [%s] is same as hosted cluster version [%s], cannot update release image, skip this case", latestAcceptedBuild, hostedclusterVersion))
	}

}

func (ht *HypershiftTest) getHypershiftImage() string {
	// get minor release version as image tag
	// imageTag, _, cvErr := exutil.GetClusterVersion(ht.oc)
	// o.Expect(cvErr).NotTo(o.HaveOccurred(), "Get minor release version error")
	// Becaseu of https://issues.redhat.com/browse/OCPQE-26256 we will always use the "latest" image
	imageTag := "latest"
	arch := architecture.GetControlPlaneArch(ht.oc)
	if arch == architecture.ARM64 {
		imageTag = fmt.Sprintf("%s-%s", imageTag, architecture.ARM64.String())
	}

	image := fmt.Sprintf("quay.io/hypershift/hypershift-operator:%s", imageTag)
	logger.Infof("Hypershift image is: %s", image)

	return image
}

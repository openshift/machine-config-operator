package extended

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	g "github.com/onsi/ginkgo/v2"
	o "github.com/onsi/gomega"
	exutil "github.com/openshift/machine-config-operator/test/extended-priv/util"
	logger "github.com/openshift/machine-config-operator/test/extended-priv/util/logext"
	"github.com/tidwall/gjson"
)

// marketplaceProduct pairs a stable product ID with a human-readable name.
type marketplaceProduct struct {
	id   string
	name string
}

// allMarketplaceProducts lists all known marketplace product IDs across all architectures.
// Each product ID encodes the architecture — no MachineSet arch lookup is needed.
var allMarketplaceProducts = []marketplaceProduct{
	// x86_64
	{"59ead7de-2540-4653-a8b0-fa7926d5c845", "OCP x86_64"},
	{"963b36c3-de6f-48ed-b802-2b38b2a2cdeb", "OKE x86_64"},
	{"f5da01a6-d046-487c-9072-42fe53b1cad4", "OPP x86_64"},
	{"962791c7-3ae5-46d1-ba62-c7a5ebac54fd", "OCP EMEA x86_64"},
	{"7026c8d7-392c-4010-b93c-f93f7bc5495f", "OKE EMEA x86_64"},
	{"628c9df3-0254-4f91-bc1f-8619d1b8eaa8", "OPP EMEA x86_64"},
	// arm64
	{"abc249f8-7440-45f7-a4b1-c026baff64c1", "OCP arm64"},
	{"d2d3ebcd-c1ca-43d8-bf0a-530433200f35", "OKE arm64"},
	{"be6d3e94-c8dc-4a3e-9218-4b449b11f06f", "OPP arm64"},
	// ROSA (arch-neutral product line)
	{"34850061-abaf-402d-92df-94325c9e947f", "ROSA"},
}

var _ = g.Describe("[sig-mco][Suite:openshift/machine-config-operator/disruptive][Serial][Disruptive] MCO AWS Marketplace Boot Image Updates", func() {
	defer g.GinkgoRecover()

	var (
		oc                   = exutil.NewCLI("mco-marketplace-bootimages", exutil.KubeConfigPath())
		machineConfiguration *MachineConfiguration
	)

	g.JustBeforeEach(func() {
		machineConfiguration = GetMachineConfiguration(oc.AsAdmin())
		PreChecks(oc)

		exutil.By("Disabling skew to avoid collisions")
		initialSpec := machineConfiguration.GetSpecOrFail()
		DisableSkew(machineConfiguration)
		g.DeferCleanup(func() {
			exutil.By("Restoring initial MachineConfiguration spec")
			o.Expect(machineConfiguration.SetSpec(initialSpec)).To(o.Succeed())
			logger.Infof("OK!\n")
		})
		logger.Infof("OK!\n")
	})

	g.It("[PolarionID:89828][OTP] MCO updates marketplace boot images to the correct product line", g.Label("Platform:aws"), func() {
		var (
			testMS = NewMachineSetList(oc.AsAdmin(), MachineAPINamespace).GetAllOrFail()[0]
			region = getCurrentRegionOrFail(oc.AsAdmin())
		)

		exutil.By("Saving original MachineSet state before label change")
		originalAMI := testMS.GetCoreOsBootImageOrFail()
		originalStream, getLabelErr := testMS.GetOSStreamLabel()

		defer func() {
			// Controller ignores rhel-10 so it won't overwrite the restored AMI.
			// Restore label first to stop any in-progress rhel-9 reconcile, then restore AMI.
			exutil.By("Restoring MachineSet stream label to rhel-10")
			if getLabelErr != nil {
				o.Expect(testMS.RemoveOSStreamLabel()).To(o.Succeed())
			} else {
				o.Expect(testMS.AddOSStreamLabel(originalStream)).To(o.Succeed())
			}
			logger.Infof("OK!\n")

			exutil.By("Restoring original MachineSet AMI")
			o.Expect(testMS.SetCoreOsBootImage(originalAMI)).To(o.Succeed())
			logger.Infof("OK!\n")
		}()
		logger.Infof("OK!\n")

		exutil.By("Setting MachineSet stream label to rhel-9 to activate marketplace AMI testing")
		o.Expect(testMS.AddOSStreamLabel(OSImageStreamRHEL9)).To(o.Succeed(),
			"error setting osstream label on MachineSet %s", testMS)
		logger.Infof("OK!\n")

		exutil.By("Reading stream version token from coreos-bootimages ConfigMap")
		versionToken, err := getMarketplaceVersionToken(oc.AsAdmin())
		o.Expect(err).NotTo(o.HaveOccurred(), "error reading marketplace version token")
		logger.Infof("Target version token: %s\n", versionToken)

		exutil.By("Building AWS EC2 client from cluster credentials")
		awsSecret := NewSecret(oc.AsAdmin(), MachineAPINamespace, "aws-cloud-credentials")
		awsCredentialsData, err := awsSecret.GetDecodedDataMap()
		o.Expect(err).NotTo(o.HaveOccurred(), "error reading aws-cloud-credentials secret")
		ec2Client, err := exutil.NewEC2Client(region, awsCredentialsData)
		o.Expect(err).NotTo(o.HaveOccurred(), "error creating AWS EC2 client")

		for _, product := range allMarketplaceProducts {
			exutil.By(fmt.Sprintf("Testing product line: %s (%s)", product.name, product.id))

			testAMI, skipReason, err := exutil.SelectMarketplaceTestAMI(context.Background(), ec2Client, product.id, versionToken)
			o.Expect(err).NotTo(o.HaveOccurred(), "error querying EC2 for product %s", product.name)
			if skipReason != "" {
				logger.Infof("Skipping product %s: %s\n", product.name, skipReason)
				continue
			}

			patchedAMIID := aws.ToString(testAMI.ImageId)
			logger.Infof("Patching MachineSet to second-newest AMI %s for product %s\n", patchedAMIID, product.name)

			exutil.By(fmt.Sprintf("Patching MachineSet to AMI %s", patchedAMIID))
			o.Expect(testMS.SetCoreOsBootImage(patchedAMIID)).To(o.Succeed(),
				"error patching MachineSet %s to AMI %s", testMS, patchedAMIID)
			logger.Infof("OK!\n")

			exutil.By("Waiting for MCO to reconcile to a newer marketplace AMI")
			o.Eventually(testMS.GetCoreOsBootImage, "3m", "20s").ShouldNot(o.Equal(patchedAMIID),
				"MCO did not update MachineSet %s away from patched AMI %s", testMS, patchedAMIID)
			logger.Infof("OK!\n")

			exutil.By("Asserting the updated AMI is marketplace-owned with the same product ID")
			newAMIID, getErr := testMS.GetCoreOsBootImage()
			o.Expect(getErr).NotTo(o.HaveOccurred(), "error getting updated AMI from MachineSet %s", testMS)

			updatedImg, descErr := exutil.DescribeMarketplaceImage(context.Background(), ec2Client, newAMIID)
			o.Expect(descErr).NotTo(o.HaveOccurred(), "error describing updated AMI %s", newAMIID)

			o.Expect(aws.ToString(updatedImg.OwnerId)).To(o.Equal(exutil.AWSMarketplaceOwnerAccountID),
				"updated AMI %s should be owned by aws-marketplace (%s)", newAMIID, exutil.AWSMarketplaceOwnerAccountID)
			o.Expect(aws.ToString(updatedImg.Name)).To(o.ContainSubstring(product.id),
				"updated AMI %s name should contain product ID %s", newAMIID, product.id)

			logger.Infof("Product %s OK: MachineSet updated to %s (name: %s)\n",
				product.name, newAMIID, aws.ToString(updatedImg.Name))
		}
	})
})

// getMarketplaceVersionToken reads the RHCOS release string from the coreos-bootimages
// ConfigMap and returns the N.M version token (e.g. "9.6"). The release string is the same
// across architectures for a given OCP release, so x86_64 is used as the reference.
func getMarketplaceVersionToken(oc *exutil.CLI) (string, error) {
	coreosBootimagesCM := NewConfigMap(oc.AsAdmin(), MachineConfigNamespace, "coreos-bootimages")
	streamJSON, err := coreosBootimagesCM.GetDataValue("stream")
	if err != nil {
		return "", fmt.Errorf("error reading coreos-bootimages ConfigMap: %w", err)
	}

	parsed := gjson.Parse(streamJSON)
	releaseString := parsed.Get("architectures.x86_64.artifacts.aws.release").String()
	if releaseString == "" {
		return "", fmt.Errorf("RHCOS release string not found in coreos-bootimages ConfigMap")
	}

	return exutil.ParseMarketplaceVersionToken(releaseString)
}

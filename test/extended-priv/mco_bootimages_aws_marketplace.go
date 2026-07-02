package extended

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	g "github.com/onsi/ginkgo/v2"
	o "github.com/onsi/gomega"
	exutil "github.com/openshift/machine-config-operator/test/extended-priv/util"
	logger "github.com/openshift/machine-config-operator/test/extended-priv/util/logext"
	"github.com/tidwall/gjson"
)

const (
	// awsMarketplaceOwnerAccountID is the AWS account that owns all marketplace AMIs.
	awsMarketplaceOwnerAccountID = "679593333241"
	// awsMarketplaceOwnerAlias is the owner alias used in DescribeImages filters for marketplace AMIs.
	awsMarketplaceOwnerAliasE2E = "aws-marketplace"
)

// descVersionRe matches a full RHCOS release string embedded in an AMI description.
// Handles all three formats: "9.6.20260210-0", "9.6.20250701-0", "418.94.202511191518-0".
var descVersionRe = regexp.MustCompile(`\d+\.\d+\.(?:\d{8}|\d{12})-\d+`)

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

	g.It("[OTP] MCO updates marketplace boot images to the correct product line", g.Label("Platform:aws"), func() {
		var (
			testMS = NewMachineSetList(oc.AsAdmin(), MachineAPINamespace).GetAllOrFail()[0]
			region = getCurrentRegionOrFail(oc.AsAdmin())
		)

		const osStreamLabel = "machineconfiguration.openshift.io/osstream"

		exutil.By("Saving original MachineSet state before label change")
		originalAMI := testMS.GetCoreOsBootImageOrFail()
		originalStream, getLabelErr := testMS.GetLabel(osStreamLabel)
		setStreamLabel := func(value string) error {
			return testMS.Patch("merge", fmt.Sprintf(`{"metadata":{"labels":{%q:%q}}}`, osStreamLabel, value))
		}
		removeStreamLabel := func() error {
			return testMS.Patch("merge", fmt.Sprintf(`{"metadata":{"labels":{%q:null}}}`, osStreamLabel))
		}

		defer func() {
			// Controller ignores rhel-10 so it won't overwrite the restored AMI.
			// Restore label first to stop any in-progress rhel-9 reconcile, then restore AMI.
			exutil.By("Restoring MachineSet stream label to rhel-10")
			if getLabelErr != nil {
				o.Expect(removeStreamLabel()).To(o.Succeed())
			} else {
				o.Expect(setStreamLabel(originalStream)).To(o.Succeed())
			}
			logger.Infof("OK!\n")

			exutil.By("Restoring original MachineSet AMI")
			o.Expect(testMS.SetCoreOsBootImage(originalAMI)).To(o.Succeed())
			logger.Infof("OK!\n")
		}()
		logger.Infof("OK!\n")

		exutil.By("Setting MachineSet stream label to rhel-9 to activate marketplace AMI testing")
		o.Expect(setStreamLabel(OSImageStreamRHEL9)).To(o.Succeed(),
			"error setting %s label on MachineSet %s", osStreamLabel, testMS)
		logger.Infof("OK!\n")

		exutil.By("Reading stream version token from coreos-bootimages ConfigMap")
		versionToken := getMarketplaceVersionTokenOrFail(oc.AsAdmin())
		logger.Infof("Target version token: %s\n", versionToken)

		exutil.By("Building AWS EC2 client from cluster credentials")
		ec2Client := newEC2ClientOrFail(oc.AsAdmin(), region)

		for _, product := range allMarketplaceProducts {
			exutil.By(fmt.Sprintf("Testing product line: %s (%s)", product.name, product.id))

			testAMI, skipReason, err := selectMarketplaceTestAMI(context.Background(), ec2Client, product.id, versionToken)
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

			updatedImg, descErr := describeMarketplaceImage(context.Background(), ec2Client, newAMIID)
			o.Expect(descErr).NotTo(o.HaveOccurred(), "error describing updated AMI %s", newAMIID)

			o.Expect(aws.ToString(updatedImg.OwnerId)).To(o.Equal(awsMarketplaceOwnerAccountID),
				"updated AMI %s should be owned by aws-marketplace (%s)", newAMIID, awsMarketplaceOwnerAccountID)
			o.Expect(aws.ToString(updatedImg.Name)).To(o.ContainSubstring(product.id),
				"updated AMI %s name should contain product ID %s", newAMIID, product.id)

			logger.Infof("Product %s OK: MachineSet updated to %s (name: %s)\n",
				product.name, newAMIID, aws.ToString(updatedImg.Name))
		}
	})
})

// getMarketplaceVersionTokenOrFail reads the RHCOS release string from the coreos-bootimages
// ConfigMap and returns the N.M version token (e.g. "9.6"). The release string is the same
// across architectures for a given OCP release, so x86_64 is used as the reference.
func getMarketplaceVersionTokenOrFail(oc *exutil.CLI) string {
	coreosBootimagesCM := NewConfigMap(oc.AsAdmin(), MachineConfigNamespace, "coreos-bootimages")
	streamJSON, err := coreosBootimagesCM.GetDataValue("stream")
	o.Expect(err).NotTo(o.HaveOccurred(), "error reading coreos-bootimages ConfigMap")

	parsed := gjson.Parse(streamJSON)
	releaseString := parsed.Get("architectures.x86_64.artifacts.aws.release").String()
	o.Expect(releaseString).NotTo(o.BeEmpty(), "RHCOS release string not found in coreos-bootimages ConfigMap")

	token, err := parseMarketplaceVersionToken(releaseString)
	o.Expect(err).NotTo(o.HaveOccurred(), "error parsing marketplace version token from %q", releaseString)
	return token
}

// parseMarketplaceVersionToken derives the N.M version token from a full RHCOS release string.
// "9.6.20260210-0" → "9.6"
func parseMarketplaceVersionToken(releaseString string) (string, error) {
	if releaseString == "" {
		return "", fmt.Errorf("RHCOS release string is empty")
	}
	parts := strings.SplitN(releaseString, ".", 3)
	if len(parts) < 2 {
		return "", fmt.Errorf("unexpected RHCOS release string format: %q", releaseString)
	}
	return parts[0] + "." + parts[1], nil
}

// newEC2ClientOrFail creates an EC2 client from the aws-cloud-credentials secret, supporting
// both static key credentials and STS web-identity (IRSA) credentials, mirroring the production
// getAWSEC2Client function in pkg/controller/bootimage/aws_helpers.go.
func newEC2ClientOrFail(oc *exutil.CLI, region string) *ec2.Client {
	secret := NewSecret(oc.AsAdmin(), MachineAPINamespace, "aws-cloud-credentials")
	data, err := secret.GetDecodedDataMap()
	o.Expect(err).NotTo(o.HaveOccurred(), "error reading aws-cloud-credentials secret")

	roleARN := data["role_arn"]
	tokenFile := data["web_identity_token_file"]
	if roleARN != "" && tokenFile != "" {
		creds := aws.NewCredentialsCache(aws.CredentialsProviderFunc(func(ctx context.Context) (aws.Credentials, error) {
			return e2eStsAssumeRoleWithWebIdentity(ctx, region, roleARN, tokenFile)
		}))
		return ec2.New(ec2.Options{
			Region:      region,
			Credentials: creds,
		})
	}

	accessKeyID := data["aws_access_key_id"]
	secretAccessKey := data["aws_secret_access_key"]
	o.Expect(accessKeyID).NotTo(o.BeEmpty(),
		"aws-cloud-credentials has neither STS (role_arn/web_identity_token_file) nor static key credentials")
	return ec2.New(ec2.Options{
		Region:      region,
		Credentials: credentials.NewStaticCredentialsProvider(accessKeyID, secretAccessKey, ""),
	})
}

// e2eStsAssumeRoleWithWebIdentity calls the STS regional endpoint to obtain temporary credentials
// via AssumeRoleWithWebIdentity. This mirrors the production implementation in aws_helpers.go.
func e2eStsAssumeRoleWithWebIdentity(ctx context.Context, region, roleARN, tokenFile string) (aws.Credentials, error) {
	token, err := os.ReadFile(tokenFile)
	if err != nil {
		return aws.Credentials{}, fmt.Errorf("reading web identity token file %s: %w", tokenFile, err)
	}

	params := url.Values{
		"Action":           {"AssumeRoleWithWebIdentity"},
		"Version":          {"2011-06-15"},
		"RoleArn":          {roleARN},
		"RoleSessionName":  {"mco-e2e-marketplace"},
		"WebIdentityToken": {strings.TrimSpace(string(token))},
	}
	endpoint := fmt.Sprintf("https://sts.%s.amazonaws.com/", region)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(params.Encode()))
	if err != nil {
		return aws.Credentials{}, fmt.Errorf("building STS request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	httpClient := &http.Client{Timeout: 30 * time.Second}
	resp, err := httpClient.Do(req)
	if err != nil {
		return aws.Credentials{}, fmt.Errorf("STS AssumeRoleWithWebIdentity: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return aws.Credentials{}, fmt.Errorf("reading STS response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var errResp e2eStsErrorResponse
		if xmlErr := xml.Unmarshal(body, &errResp); xmlErr == nil && errResp.Error.Code != "" {
			return aws.Credentials{}, fmt.Errorf("STS AssumeRoleWithWebIdentity: %s: %s", errResp.Error.Code, errResp.Error.Message)
		}
		return aws.Credentials{}, fmt.Errorf("STS AssumeRoleWithWebIdentity returned HTTP %d", resp.StatusCode)
	}

	var result e2eStsAssumeRoleResponse
	if err := xml.Unmarshal(body, &result); err != nil {
		return aws.Credentials{}, fmt.Errorf("parsing STS response: %w", err)
	}

	c := result.Result.Credentials
	expiry, err := time.Parse(time.RFC3339, c.Expiration)
	if err != nil {
		return aws.Credentials{}, fmt.Errorf("parsing STS credential expiration %q: %w", c.Expiration, err)
	}

	return aws.Credentials{
		AccessKeyID:     c.AccessKeyID,
		SecretAccessKey: c.SecretAccessKey,
		SessionToken:    c.SessionToken,
		CanExpire:       true,
		Expires:         expiry,
		Source:          "STS web identity",
	}, nil
}

type e2eStsAssumeRoleResponse struct {
	XMLName xml.Name `xml:"AssumeRoleWithWebIdentityResponse"`
	Result  struct {
		Credentials struct {
			AccessKeyID     string `xml:"AccessKeyId"`
			SecretAccessKey string `xml:"SecretAccessKey"`
			SessionToken    string `xml:"SessionToken"`
			Expiration      string `xml:"Expiration"`
		} `xml:"Credentials"`
	} `xml:"AssumeRoleWithWebIdentityResult"`
}

type e2eStsErrorResponse struct {
	XMLName xml.Name `xml:"ErrorResponse"`
	Error   struct {
		Code    string `xml:"Code"`
		Message string `xml:"Message"`
	} `xml:"Error"`
}

// selectMarketplaceTestAMI returns an AMI to patch the MachineSet to, chosen so that MCO
// will always have a strictly newer marketplace AMI to update to. Two strategies are tried:
//
//  1. RHEL-to-newer: if ≥2 RHEL-aligned AMIs exist at-or-below the target version, return the
//     second-newest. MCO will update to the newest RHEL-aligned build.
//
//  2. Pre-RHEL-to-RHEL: if exactly 1 RHEL-aligned AMI exists and pre-RHEL AMIs are also present,
//     return a pre-RHEL AMI. MCO will update from the old-format AMI to the RHEL-aligned one.
//     This covers product lines that are mid-transition between versioning schemes.
//
// skipReason is non-empty when neither strategy applies (e.g. only pre-RHEL AMIs exist, meaning
// the production code would also skip via the sawPreRHELAligned no-op path).
func selectMarketplaceTestAMI(ctx context.Context, client *ec2.Client, productID, versionToken string) (selected ec2types.Image, skipReason string, err error) {
	out, err := client.DescribeImages(ctx, &ec2.DescribeImagesInput{
		Owners: []string{awsMarketplaceOwnerAliasE2E},
		Filters: []ec2types.Filter{
			{Name: aws.String("name"), Values: []string{"*" + productID + "*"}},
		},
	})
	if err != nil {
		return ec2types.Image{}, "", fmt.Errorf("DescribeImages for product %s: %w", productID, err)
	}

	type candidate struct {
		image ec2types.Image
		token string
	}

	var rhelCandidates []candidate
	var preRHELCandidates []candidate

	for _, img := range out.Images {
		_, token, ok := parseVersionFromDescription(aws.ToString(img.Description))
		if !ok {
			continue
		}
		if marketplaceIsPreRHELAligned(token) {
			preRHELCandidates = append(preRHELCandidates, candidate{img, token})
		} else if marketplaceCmpToken(token, versionToken) <= 0 {
			rhelCandidates = append(rhelCandidates, candidate{img, token})
		}
	}

	// Strategy 1: ≥2 RHEL-aligned AMIs — patch to second-newest, MCO updates to newest.
	if len(rhelCandidates) >= 2 {
		sort.Slice(rhelCandidates, func(i, j int) bool {
			if cmp := marketplaceCmpToken(rhelCandidates[i].token, rhelCandidates[j].token); cmp != 0 {
				return cmp > 0
			}
			return aws.ToString(rhelCandidates[i].image.CreationDate) > aws.ToString(rhelCandidates[j].image.CreationDate)
		})
		return rhelCandidates[1].image, "", nil
	}

	// Strategy 2: exactly 1 RHEL-aligned AMI and pre-RHEL AMIs present — patch to a pre-RHEL
	// AMI so MCO transitions to the RHEL-aligned one. This mirrors the production path where
	// a MachineSet carrying an old-format marketplace AMI gets updated once the product line
	// publishes its first RHEL-aligned image.
	if len(rhelCandidates) == 1 && len(preRHELCandidates) > 0 {
		return preRHELCandidates[0].image, "", nil
	}

	// No testable scenario: production code would also skip (sawPreRHELAligned no-op) or error.
	if len(preRHELCandidates) > 0 {
		return ec2types.Image{}, fmt.Sprintf(
			"only pre-RHEL-aligned AMIs (e.g. 418.94.x) found for product %s in this region; "+
				"product line has not yet published any RHEL-aligned AMIs here",
			productID), nil
	}
	return ec2types.Image{}, fmt.Sprintf(
		"no marketplace AMIs found for product %s in this region", productID), nil
}

// describeMarketplaceImage returns the EC2 image metadata for the given AMI ID.
func describeMarketplaceImage(ctx context.Context, client *ec2.Client, amiID string) (ec2types.Image, error) {
	out, err := client.DescribeImages(ctx, &ec2.DescribeImagesInput{
		ImageIds: []string{amiID},
	})
	if err != nil {
		return ec2types.Image{}, fmt.Errorf("DescribeImages for %s: %w", amiID, err)
	}
	if len(out.Images) == 0 {
		return ec2types.Image{}, fmt.Errorf("AMI %s not found", amiID)
	}
	return out.Images[0], nil
}

// parseVersionFromDescription extracts the full RHCOS release string and N.M token from a
// marketplace AMI description. Handles all three formats in use:
//   - RHEL marketplace: "RHEL CoreOS 9.6 9.6.20260210-0 x86_64"
//   - ROSA:             "rhcos-9.6.20250701-0-x86_64"
//   - Pre-RHEL-aligned: "OpenShift 4.18 418.94.202511191518-0 x86_64"
func parseVersionFromDescription(description string) (fullVersion, token string, ok bool) {
	fullVersion = descVersionRe.FindString(description)
	if fullVersion == "" {
		return "", "", false
	}
	parts := strings.SplitN(fullVersion, ".", 3)
	return fullVersion, parts[0] + "." + parts[1], true
}

// marketplaceCmpToken compares two "major.minor" version tokens. Returns negative if a < b.
func marketplaceCmpToken(a, b string) int {
	parse := func(s string) (int, int) {
		p := strings.SplitN(s, ".", 2)
		if len(p) != 2 {
			return 0, 0
		}
		maj, _ := strconv.Atoi(p[0])
		minima, _ := strconv.Atoi(p[1])
		return maj, minima
	}
	aMaj, aMin := parse(a)
	bMaj, bMin := parse(b)
	if aMaj != bMaj {
		return aMaj - bMaj
	}
	return aMin - bMin
}

// marketplaceIsPreRHELAligned reports whether a version token uses the pre-4.19 OCP-based
// RHCOS versioning scheme (e.g. "418.94") rather than the RHEL-aligned scheme (e.g. "9.6").
func marketplaceIsPreRHELAligned(token string) bool {
	major, _ := strconv.Atoi(strings.SplitN(token, ".", 2)[0])
	return major > 100
}

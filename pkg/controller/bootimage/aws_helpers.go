package bootimage

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
	coreosstream "github.com/coreos/stream-metadata-go/stream"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"

	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
)

const (
	// awsMarketplaceOwnerID is the AWS account that owns all marketplace AMIs.
	awsMarketplaceOwnerID = "679593333241"
	// awsRHCOSOwnerID is the Red Hat AWS account that owns standard RHCOS AMIs.
	awsRHCOSOwnerID = "531415883065"
	// rosaProductID is the marketplace product ID for ROSA Classic.
	rosaProductID = "34850061-abaf-402d-92df-94325c9e947f"
	// awsMarketplaceOwnerAlias is the owner alias used in DescribeImages filters for marketplace AMIs.
	awsMarketplaceOwnerAlias = "aws-marketplace"
	// awsCredentialsSecretName is the secret in openshift-machine-api provisioned by
	// the machine-api-operator's CredentialsRequest. It already includes ec2:DescribeImages.
	awsCredentialsSecretName = "aws-cloud-credentials"
)

// marketplaceProductNames maps the AWS Marketplace product IDs to human-readable variant names.
// These IDs are stable — they are tied to marketplace listings and will not change.
var marketplaceProductNames = map[string]string{
	// x86_64
	"59ead7de-2540-4653-a8b0-fa7926d5c845": "OCP x86_64",
	"963b36c3-de6f-48ed-b802-2b38b2a2cdeb": "OKE x86_64",
	"f5da01a6-d046-487c-9072-42fe53b1cad4": "OPP x86_64",
	// arm64
	"abc249f8-7440-45f7-a4b1-c026baff64c1": "OCP arm64",
	"d2d3ebcd-c1ca-43d8-bf0a-530433200f35": "OKE arm64",
	"be6d3e94-c8dc-4a3e-9218-4b449b11f06f": "OPP arm64",
	// x86_64 EMEA
	"962791c7-3ae5-46d1-ba62-c7a5ebac54fd": "OCP EMEA x86_64",
	"7026c8d7-392c-4010-b93c-f93f7bc5495f": "OKE EMEA x86_64",
	"628c9df3-0254-4f91-bc1f-8619d1b8eaa8": "OPP EMEA x86_64",
	// ROSA
	rosaProductID: "ROSA",
}

// productName returns the human-readable variant name for a marketplace product ID,
// falling back to the product ID itself if it is not in the map.
func productName(productID string) string {
	if name, ok := marketplaceProductNames[productID]; ok {
		return name
	}
	return productID
}

// amiKind classifies a RHCOS AMI by its origin.
type amiKind int

const (
	amiKindUnknown     amiKind = iota // custom or unrecognised image — skip
	amiKindStandard                   // standard RHCOS (owner 531415883065) — stream configmap path
	amiKindMarketplace                // AWS Marketplace RHCOS (owner 679593333241) — marketplace path
	amiKindROSA                       // ROSA Classic — handled via marketplace path
)

// getAWSEC2Client fetches the aws-cloud-credentials secret and returns an EC2 client for the given region.
// It supports two credential formats: static keys (aws_access_key_id / aws_secret_access_key) and
// STS web-identity (role_arn / web_identity_token_file). STS is tried first.
func getAWSEC2Client(ctx context.Context, region string, secretClient clientset.Interface) (*ec2.Client, error) {
	secret, err := secretClient.CoreV1().Secrets(ctrlcommon.MachineAPINamespace).Get(
		ctx, awsCredentialsSecretName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to fetch %s secret: %w", awsCredentialsSecretName, err)
	}

	roleARN := string(secret.Data["role_arn"])
	tokenFile := string(secret.Data["web_identity_token_file"])
	if roleARN != "" && tokenFile != "" {
		creds := aws.NewCredentialsCache(aws.CredentialsProviderFunc(func(ctx context.Context) (aws.Credentials, error) {
			return stsAssumeRoleWithWebIdentity(ctx, region, roleARN, tokenFile)
		}))
		return ec2.New(ec2.Options{
			Region:      region,
			Credentials: creds,
		}), nil
	}

	accessKeyID := string(secret.Data["aws_access_key_id"])
	secretAccessKey := string(secret.Data["aws_secret_access_key"])
	if accessKeyID != "" && secretAccessKey != "" {
		return ec2.New(ec2.Options{
			Region:      region,
			Credentials: credentials.NewStaticCredentialsProvider(accessKeyID, secretAccessKey, ""),
		}), nil
	}

	return nil, fmt.Errorf("%s secret contains neither static credentials (aws_access_key_id/aws_secret_access_key) nor STS credentials (role_arn/web_identity_token_file)", awsCredentialsSecretName)
}

// stsAssumeRoleWithWebIdentity calls the STS regional endpoint to obtain temporary credentials
// via AssumeRoleWithWebIdentity. AssumeRoleWithWebIdentity does not require request signing —
// the OIDC token itself is the authentication mechanism.
func stsAssumeRoleWithWebIdentity(ctx context.Context, region, roleARN, tokenFile string) (aws.Credentials, error) {
	token, err := os.ReadFile(tokenFile)
	if err != nil {
		return aws.Credentials{}, fmt.Errorf("reading web identity token file %s: %w", tokenFile, err)
	}

	params := url.Values{
		"Action":           {"AssumeRoleWithWebIdentity"},
		"Version":          {"2011-06-15"},
		"RoleArn":          {roleARN},
		"RoleSessionName":  {"machine-config-operator"},
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
		var errResp stsErrorResponse
		if xmlErr := xml.Unmarshal(body, &errResp); xmlErr == nil && errResp.Error.Code != "" {
			return aws.Credentials{}, fmt.Errorf("STS AssumeRoleWithWebIdentity: %s: %s", errResp.Error.Code, errResp.Error.Message)
		}
		return aws.Credentials{}, fmt.Errorf("STS AssumeRoleWithWebIdentity returned HTTP %d", resp.StatusCode)
	}

	var result stsAssumeRoleResponse
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

type stsAssumeRoleResponse struct {
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

type stsErrorResponse struct {
	XMLName xml.Name `xml:"ErrorResponse"`
	Error   struct {
		Code    string `xml:"Code"`
		Message string `xml:"Message"`
	} `xml:"Error"`
}

// describeAMI returns the EC2 image details for the given AMI ID.
func describeAMI(ctx context.Context, client *ec2.Client, amiID string) (*ec2types.Image, error) {
	out, err := client.DescribeImages(ctx, &ec2.DescribeImagesInput{
		ImageIds: []string{amiID},
	})
	if err != nil {
		return nil, fmt.Errorf("DescribeImages failed for %s: %w", amiID, err)
	}
	if len(out.Images) == 0 {
		return nil, fmt.Errorf("AMI %s not found", amiID)
	}
	return &out.Images[0], nil
}

// detectAMIKind classifies the AMI by its owner. For marketplace AMIs it also returns
// the product ID extracted from the AMI name.
func detectAMIKind(image *ec2types.Image) (amiKind, string) {
	switch aws.ToString(image.OwnerId) {
	case awsRHCOSOwnerID:
		return amiKindStandard, ""
	case awsMarketplaceOwnerID:
		productID := extractProductID(aws.ToString(image.Name))
		switch productID {
		case "":
			return amiKindUnknown, ""
		case rosaProductID:
			return amiKindROSA, productID
		default:
			return amiKindMarketplace, productID
		}
	default:
		return amiKindUnknown, ""
	}
}

var productIDRegex = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// extractProductID returns the trailing UUID-format product ID from a marketplace AMI name, e.g.:
//
//	RHEL-9.4-RHCOS-9.6_HVM_GA-20260210-x86_64-0-{product-id}
//
// Returns an empty string if no valid product ID is found.
func extractProductID(name string) string {
	parts := strings.Split(name, "-")
	if len(parts) < 5 {
		return ""
	}
	candidate := strings.Join(parts[len(parts)-5:], "-")
	if productIDRegex.MatchString(candidate) {
		return candidate
	}
	return ""
}

// marketplaceVersionToken derives the version token used in marketplace AMI descriptions
// from the RHCOS release string in the stream configmap.
//
// This feature targets OCP 4.19+, where the release string uses RHEL-aligned versioning:
//
//	"9.6.20260210-0"  →  "9.6"
//
// Pre-4.19 clusters used a different release string format ("418.94.202511191518-0") and
// a different AMI naming scheme that predates marketplace boot image support.
func marketplaceVersionToken(releaseString string) (string, error) {
	if releaseString == "" {
		return "", fmt.Errorf("RHCOS release string is empty")
	}
	parts := strings.SplitN(releaseString, ".", 3)
	if len(parts) < 2 {
		return "", fmt.Errorf("unexpected RHCOS release string format: %q", releaseString)
	}
	return parts[0] + "." + parts[1], nil
}

// resolveMarketplaceAMI derives the version token from the stream configmap and delegates to
// findMarketplaceAMI. Shared by the marketplace and ROSA variant paths.
func resolveMarketplaceAMI(ctx context.Context, client *ec2.Client, streamData *coreosstream.Stream, arch, productID, machineSetName string) (string, error) {
	streamArch, err := streamData.GetArchitecture(arch)
	if err != nil {
		return "", err
	}
	awsArtifact, ok := streamArch.Artifacts["aws"]
	if !ok {
		return "", "", fmt.Errorf("MachineSet %s: no aws artifact in stream for arch %s", machineSetName, arch)
	}
	releaseString := awsArtifact.Release
	versionToken, err := marketplaceVersionToken(releaseString)
	if err != nil {
		return "", fmt.Errorf("MachineSet %s: %w", machineSetName, err)
	}
	return findMarketplaceAMI(ctx, client, productID, versionToken, machineSetName)
}

// descriptionVersionRe extracts the N.M version token from marketplace AMI descriptions.
// It handles both formats in use:
//   - RHEL marketplace: "RHEL CoreOS 9.6 9.6.20260210-0 x86_64"
//   - ROSA:             "rhcos-9.6.20250701-0-x86_64"
var descriptionVersionRe = regexp.MustCompile(`(?:CoreOS |rhcos-)(\d+\.\d+)[ .]`)

// extractVersionFromDescription parses the N.M version token from a marketplace AMI description.
func extractVersionFromDescription(description string) (string, bool) {
	m := descriptionVersionRe.FindStringSubmatch(description)
	if m == nil {
		return "", false
	}
	return m[1], true
}

// cmpVersionToken compares two "major.minor" version tokens.
// Returns negative if a < b, zero if equal, positive if a > b.
func cmpVersionToken(a, b string) int {
	parse := func(s string) (int, int) {
		parts := strings.SplitN(s, ".", 2)
		if len(parts) != 2 {
			return 0, 0
		}
		major, _ := strconv.Atoi(parts[0])
		minor, _ := strconv.Atoi(parts[1])
		return major, minor
	}
	aMaj, aMin := parse(a)
	bMaj, bMin := parse(b)
	if aMaj != bMaj {
		return aMaj - bMaj
	}
	return aMin - bMin
}

// findMarketplaceAMI returns the AMI ID of the best marketplace AMI for the given product ID
// and version token. It fetches all AMIs for the product ID, discards any whose description
// version exceeds the target, then returns the newest AMI at the highest version ≤ target.
// Accepting one version older prevents stalling when the target version has not yet replicated
// to this region. Returns an empty string (and no error) if no suitable AMI is found.
func findMarketplaceAMI(ctx context.Context, client *ec2.Client, productID, versionToken, machineSetName string) (string, error) {
	out, err := client.DescribeImages(ctx, &ec2.DescribeImagesInput{
		Owners: []string{awsMarketplaceOwnerAlias},
		Filters: []ec2types.Filter{
			{
				Name:   aws.String("name"),
				Values: []string{"*" + productID + "*"},
			},
		},
	})
	if err != nil {
		return "", fmt.Errorf("DescribeImages failed for product ID %s: %w", productID, err)
	}

	type candidate struct {
		image   ec2types.Image
		version string
	}

	// DescribeImages filtered by name glob already narrows to the right product ID;
	// parse each description to get its version and discard anything ahead of the target.
	var matches []candidate
	for _, img := range out.Images {
		v, ok := extractVersionFromDescription(aws.ToString(img.Description))
		if !ok {
			continue
		}
		if cmpVersionToken(v, versionToken) <= 0 {
			matches = append(matches, candidate{img, v})
		}
	}

	if len(matches) == 0 {
		klog.Infof("no marketplace AMI found for product ID %s version ≤ %s in region, will retry on next reconcile, skipping update of MachineSet %s", productID, versionToken, machineSetName)
		return "", nil
	}

	// Prefer the highest version not exceeding the target; break ties by newest CreationDate.
	sort.Slice(matches, func(i, j int) bool {
		if cmp := cmpVersionToken(matches[i].version, matches[j].version); cmp != 0 {
			return cmp > 0
		}
		return aws.ToString(matches[i].image.CreationDate) > aws.ToString(matches[j].image.CreationDate)
	})

	best := matches[0]
	if best.version != versionToken {
		klog.Infof("MachineSet %s: target version %s not yet available in region, using %s (%s)", machineSetName, versionToken, best.version, aws.ToString(best.image.ImageId))
	}
	return aws.ToString(best.image.ImageId), nil
}

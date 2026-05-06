package bootimage

import (
	"context"
	"fmt"
	"sort"
	"strings"

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
func getAWSEC2Client(ctx context.Context, region string, secretClient clientset.Interface) (*ec2.Client, error) {
	secret, err := secretClient.CoreV1().Secrets(ctrlcommon.MachineAPINamespace).Get(
		ctx, awsCredentialsSecretName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to fetch %s secret: %w", awsCredentialsSecretName, err)
	}

	accessKeyID := string(secret.Data["aws_access_key_id"])
	secretAccessKey := string(secret.Data["aws_secret_access_key"])
	if accessKeyID == "" || secretAccessKey == "" {
		return nil, fmt.Errorf("%s secret is missing aws_access_key_id or aws_secret_access_key", awsCredentialsSecretName)
	}

	return ec2.New(ec2.Options{
		Region:      region,
		Credentials: credentials.NewStaticCredentialsProvider(accessKeyID, secretAccessKey, ""),
	}), nil
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
		if productID == rosaProductID {
			return amiKindROSA, productID
		}
		return amiKindMarketplace, productID
	default:
		return amiKindUnknown, ""
	}
}

// extractProductID returns the trailing UUID-format product ID from a marketplace AMI name, e.g.:
//
//	RHEL-9.4-RHCOS-4.18_HVM_GA-20251119-x86_64-0-{product-id}
//
// Returns an empty string if no valid product ID is found.
func extractProductID(name string) string {
	parts := strings.Split(name, "-")
	if len(parts) < 5 {
		return ""
	}
	candidate := strings.Join(parts[len(parts)-5:], "-")
	if isProductID(candidate) {
		return candidate
	}
	return ""
}

// isProductID returns true if s is a UUID in the standard 8-4-4-4-12 hex format.
func isProductID(s string) bool {
	parts := strings.Split(s, "-")
	if len(parts) != 5 {
		return false
	}
	for i, p := range parts {
		expected := []int{8, 4, 4, 4, 12}[i]
		if len(p) != expected {
			return false
		}
		for _, c := range p {
			if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
				return false
			}
		}
	}
	return true
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
	releaseString := streamArch.Artifacts["aws"].Release
	versionToken, err := marketplaceVersionToken(releaseString)
	if err != nil {
		return "", fmt.Errorf("MachineSet %s: %w", machineSetName, err)
	}
	return findMarketplaceAMI(ctx, client, productID, versionToken, machineSetName)
}

// descriptionMatchesVersion reports whether an AMI description contains the version token
// at a word boundary. Two description formats are in use:
//
//   - RHEL marketplace: "RHEL CoreOS 9.6 9.6.20260210-0 x86_64"  → token bounded by spaces
//   - ROSA:             "rhcos-9.6.20250701-0-x86_64"             → token bounded by dash/dot
func descriptionMatchesVersion(description, versionToken string) bool {
	return strings.Contains(description, " "+versionToken+" ") ||
		strings.Contains(description, "-"+versionToken+".")
}

// findMarketplaceAMI returns the AMI ID of the best marketplace AMI for the given product ID
// and version token. It calls DescribeImages filtered by owner and name, then picks the
// most recently created image whose description contains the version token.
// Returns an empty string (and no error) if no matching AMI is found.
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

	var matches []ec2types.Image
	for _, img := range out.Images {
		if descriptionMatchesVersion(aws.ToString(img.Description), versionToken) {
			matches = append(matches, img)
		}
	}

	if len(matches) == 0 {
		// Skip rather than falling back to the latest available AMI for the product ID.
		// A fallback could inadvertently advance the node to a newer stream (e.g. 9.8
		// when 9.6 hasn't replicated to this region yet). The MCO reconciles continuously,
		// so the correct AMI will be picked up automatically once it appears in the region.
		klog.Infof("no marketplace AMI found for product ID %s version %s in region, will retry on next reconcile, skipping update of MachineSet %s", productID, versionToken, machineSetName)
		return "", nil
	}

	sort.Slice(matches, func(i, j int) bool {
		return aws.ToString(matches[i].CreationDate) > aws.ToString(matches[j].CreationDate)
	})

	return aws.ToString(matches[0].ImageId), nil
}

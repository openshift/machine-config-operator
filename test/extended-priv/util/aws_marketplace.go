package util

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
)

const (
	// AWSMarketplaceOwnerAccountID is the AWS account that owns all marketplace AMIs.
	AWSMarketplaceOwnerAccountID = "679593333241"
	// awsMarketplaceOwnerAlias is the owner alias used in DescribeImages filters for marketplace AMIs.
	awsMarketplaceOwnerAlias = "aws-marketplace"
)

// descVersionRe matches a full RHCOS release string embedded in an AMI description.
// Handles all three formats: "9.6.20260210-0", "9.6.20250701-0", "418.94.202511191518-0".
var descVersionRe = regexp.MustCompile(`\d+\.\d+\.(?:\d{8}|\d{12})-\d+`)

// NewEC2Client creates an EC2 client from AWS credential data, supporting both static key
// credentials (aws_access_key_id/aws_secret_access_key) and STS web-identity (IRSA)
// credentials (role_arn/web_identity_token_file). This mirrors the production
// getAWSEC2Client function in pkg/controller/bootimage/aws_helpers.go.
func NewEC2Client(region string, credentialsData map[string]string) (*ec2.Client, error) {
	roleARN := credentialsData["role_arn"]
	tokenFile := credentialsData["web_identity_token_file"]
	if roleARN != "" && tokenFile != "" {
		creds := aws.NewCredentialsCache(aws.CredentialsProviderFunc(func(ctx context.Context) (aws.Credentials, error) {
			return stsAssumeRoleWithWebIdentity(ctx, region, roleARN, tokenFile)
		}))
		return ec2.New(ec2.Options{
			Region:      region,
			Credentials: creds,
		}), nil
	}

	accessKeyID := credentialsData["aws_access_key_id"]
	secretAccessKey := credentialsData["aws_secret_access_key"]
	if accessKeyID == "" {
		return nil, fmt.Errorf("credentials contain neither STS (role_arn/web_identity_token_file) nor static key credentials (aws_access_key_id/aws_secret_access_key)")
	}
	return ec2.New(ec2.Options{
		Region:      region,
		Credentials: credentials.NewStaticCredentialsProvider(accessKeyID, secretAccessKey, ""),
	}), nil
}

// stsAssumeRoleWithWebIdentity calls the STS regional endpoint to obtain temporary credentials
// via AssumeRoleWithWebIdentity. This mirrors the production implementation in aws_helpers.go.
func stsAssumeRoleWithWebIdentity(ctx context.Context, region, roleARN, tokenFile string) (aws.Credentials, error) {
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

// SelectMarketplaceTestAMI returns an AMI to patch the MachineSet to, chosen so that MCO
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
func SelectMarketplaceTestAMI(ctx context.Context, client *ec2.Client, productID, versionToken string) (selected ec2types.Image, skipReason string, err error) {
	out, err := client.DescribeImages(ctx, &ec2.DescribeImagesInput{
		Owners: []string{awsMarketplaceOwnerAlias},
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

// DescribeMarketplaceImage returns the EC2 image metadata for the given AMI ID.
func DescribeMarketplaceImage(ctx context.Context, client *ec2.Client, amiID string) (ec2types.Image, error) {
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

// ParseMarketplaceVersionToken derives the N.M version token from a full RHCOS release string.
// "9.6.20260210-0" → "9.6"
func ParseMarketplaceVersionToken(releaseString string) (string, error) {
	if releaseString == "" {
		return "", fmt.Errorf("RHCOS release string is empty")
	}
	parts := strings.SplitN(releaseString, ".", 3)
	if len(parts) < 2 {
		return "", fmt.Errorf("unexpected RHCOS release string format: %q", releaseString)
	}
	return parts[0] + "." + parts[1], nil
}

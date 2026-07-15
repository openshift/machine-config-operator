package bootimage

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aws/aws-sdk-go-v2/aws"
)

func TestExtractProductID(t *testing.T) {
	cases := []struct {
		name     string
		amiName  string
		expected string
	}{
		{
			name:     "OCP x86_64 AMI",
			amiName:  "RHEL-9.4-RHCOS-9.6_HVM_GA-20260210-x86_64-0-59ead7de-2540-4653-a8b0-fa7926d5c845",
			expected: "59ead7de-2540-4653-a8b0-fa7926d5c845",
		},
		{
			name:     "OCP arm64 AMI",
			amiName:  "RHEL-9.4-RHCOS-9.6_HVM_GA-20260210-aarch64-0-abc249f8-7440-45f7-a4b1-c026baff64c1",
			expected: "abc249f8-7440-45f7-a4b1-c026baff64c1",
		},
		{
			name:     "ROSA AMI",
			amiName:  "RHEL-9.4-RHCOS-9.6_HVM_GA-20260210-x86_64-0-34850061-abaf-402d-92df-94325c9e947f",
			expected: "34850061-abaf-402d-92df-94325c9e947f",
		},
		{
			name:     "standard RHCOS AMI has no product ID",
			amiName:  "rhcos-419-94-202504151514-0-x86_64",
			expected: "",
		},
		{
			name:     "name too short",
			amiName:  "short",
			expected: "",
		},
		{
			name:     "trailing segment not a UUID",
			amiName:  "RHEL-9.4-RHCOS-9.6_HVM_GA-20260210-x86_64-0-notauuid",
			expected: "",
		},
		{
			name:     "empty name",
			amiName:  "",
			expected: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.expected, extractProductID(tc.amiName))
		})
	}
}

func TestMarketplaceVersionToken(t *testing.T) {
	cases := []struct {
		name          string
		releaseString string
		expected      string
		expectError   bool
	}{
		{
			name:          "standard RHEL-aligned release string",
			releaseString: "9.6.20260210-0",
			expected:      "9.6",
		},
		{
			name:          "double-digit minor",
			releaseString: "9.10.20260210-0",
			expected:      "9.10",
		},
		{
			name:          "two-segment string (no build suffix)",
			releaseString: "9.6",
			expected:      "9.6",
		},
		{
			name:          "empty string",
			releaseString: "",
			expectError:   true,
		},
		{
			name:          "single segment without dot",
			releaseString: "9",
			expectError:   true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := marketplaceVersionToken(tc.releaseString)
			if tc.expectError {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tc.expected, got)
			}
		})
	}
}

func TestExtractVersionFromDescription(t *testing.T) {
	cases := []struct {
		name        string
		description string
		fullVersion string
		token       string
		ok          bool
	}{
		{
			name:        "RHEL marketplace format x86_64",
			description: "RHEL CoreOS 9.6 9.6.20260210-0 x86_64",
			fullVersion: "9.6.20260210-0",
			token:       "9.6",
			ok:          true,
		},
		{
			name:        "RHEL marketplace format aarch64",
			description: "RHEL CoreOS 9.6 9.6.20260210-0 aarch64",
			fullVersion: "9.6.20260210-0",
			token:       "9.6",
			ok:          true,
		},
		{
			name:        "ROSA format",
			description: "rhcos-9.6.20250701-0-x86_64",
			fullVersion: "9.6.20250701-0",
			token:       "9.6",
			ok:          true,
		},
		{
			name:        "pre-RHEL-aligned old OCP format",
			description: "OpenShift 4.18 418.94.202511191518-0 x86_64",
			fullVersion: "418.94.202511191518-0",
			token:       "418.94",
			ok:          true,
		},
		{
			name:        "no version in description",
			description: "some random description without a version",
			ok:          false,
		},
		{
			name:        "empty description",
			description: "",
			ok:          false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fullVersion, token, ok := extractVersionFromDescription(tc.description)
			assert.Equal(t, tc.ok, ok)
			assert.Equal(t, tc.fullVersion, fullVersion)
			assert.Equal(t, tc.token, token)
		})
	}
}

func TestCmpVersionToken(t *testing.T) {
	cases := []struct {
		name string
		a, b string
		sign int // negative, zero, or positive
	}{
		{"equal tokens", "9.6", "9.6", 0},
		{"a less than b (minor)", "9.5", "9.6", -1},
		{"a greater than b (minor)", "9.7", "9.6", 1},
		{"a less than b (major)", "9.6", "10.0", -1},
		{"a greater than b (major)", "10.0", "9.6", 1},
		{"pre-RHEL-aligned vs RHEL-aligned", "418.94", "9.6", 1},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := cmpVersionToken(tc.a, tc.b)
			switch {
			case tc.sign < 0:
				assert.Less(t, got, 0)
			case tc.sign > 0:
				assert.Greater(t, got, 0)
			default:
				assert.Equal(t, 0, got)
			}
		})
	}
}

func TestCmpRHCOSVersion(t *testing.T) {
	cases := []struct {
		name string
		a, b string
		sign int
	}{
		{"same major.minor, different date", "9.6.20260210-0", "9.6.20260110-0", 0},
		{"higher minor version", "9.6.20260210-0", "9.5.20251001-0", 1},
		{"lower minor version", "9.5.20251001-0", "9.6.20260210-0", -1},
		{"higher major version", "10.0.20260210-0", "9.6.20260210-0", 1},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := cmpRHCOSVersion(tc.a, tc.b)
			switch {
			case tc.sign < 0:
				assert.Less(t, got, 0)
			case tc.sign > 0:
				assert.Greater(t, got, 0)
			default:
				assert.Equal(t, 0, got)
			}
		})
	}
}

func TestIsPreRHELAlignedToken(t *testing.T) {
	cases := []struct {
		token    string
		expected bool
	}{
		{"418.94", true},
		{"400.0", true},
		{"101.0", true},
		{"100.0", false},
		{"9.6", false},
		{"10.0", false},
		{"99.9", false},
	}

	for _, tc := range cases {
		t.Run(tc.token, func(t *testing.T) {
			assert.Equal(t, tc.expected, isPreRHELAlignedToken(tc.token))
		})
	}
}

func TestDetectAMIKind(t *testing.T) {
	const (
		ocpProductID  = "59ead7de-2540-4653-a8b0-fa7926d5c845"
		rosaProductID = "34850061-abaf-402d-92df-94325c9e947f"
	)

	cases := []struct {
		name            string
		ownerID         string
		amiName         string
		expectedKind    amiKind
		expectedProduct string
	}{
		{
			name:            "standard RHCOS AMI",
			ownerID:         awsRHCOSOwnerID,
			amiName:         "rhcos-419-94-202504151514-0-x86_64",
			expectedKind:    amiKindStandard,
			expectedProduct: "",
		},
		{
			name:            "marketplace OCP AMI",
			ownerID:         awsMarketplaceOwnerID,
			amiName:         "RHEL-9.4-RHCOS-9.6_HVM_GA-20260210-x86_64-0-" + ocpProductID,
			expectedKind:    amiKindMarketplace,
			expectedProduct: ocpProductID,
		},
		{
			name:            "marketplace ROSA AMI",
			ownerID:         awsMarketplaceOwnerID,
			amiName:         "RHEL-9.4-RHCOS-9.6_HVM_GA-20260210-x86_64-0-" + rosaProductID,
			expectedKind:    amiKindROSA,
			expectedProduct: rosaProductID,
		},
		{
			name:            "marketplace AMI with no product ID in name",
			ownerID:         awsMarketplaceOwnerID,
			amiName:         "some-image-without-a-uuid",
			expectedKind:    amiKindUnknown,
			expectedProduct: "",
		},
		{
			name:            "unknown owner",
			ownerID:         "123456789012",
			amiName:         "RHEL-9.4-RHCOS-9.6_HVM_GA-20260210-x86_64-0-" + ocpProductID,
			expectedKind:    amiKindUnknown,
			expectedProduct: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			image := &ec2types.Image{
				OwnerId: aws.String(tc.ownerID),
				Name:    aws.String(tc.amiName),
			}
			kind, productID := detectAMIKind(image)
			assert.Equal(t, tc.expectedKind, kind)
			assert.Equal(t, tc.expectedProduct, productID)
		})
	}
}

// mockEC2HTTPClient intercepts EC2 API calls and returns a canned XML response.
type mockEC2HTTPClient struct {
	responseXML string
	err         error
}

func (m *mockEC2HTTPClient) Do(_ *http.Request) (*http.Response, error) {
	if m.err != nil {
		return nil, m.err
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(m.responseXML)),
		Header:     make(http.Header),
	}, nil
}

// describeImagesXML builds a minimal DescribeImages XML response body from a slice of fake AMIs.
func describeImagesXML(images []fakeAMI) string {
	var items strings.Builder
	for _, img := range images {
		fmt.Fprintf(&items, `
		<item>
			<imageId>%s</imageId>
			<ownerId>%s</ownerId>
			<name>%s</name>
			<description>%s</description>
			<creationDate>%s</creationDate>
		</item>`, img.imageID, img.ownerID, img.name, img.description, img.creationDate)
	}
	return fmt.Sprintf(`<DescribeImagesResponse xmlns="http://ec2.amazonaws.com/doc/2016-11-15/">
	<imagesSet>%s
	</imagesSet>
</DescribeImagesResponse>`, items.String())
}

type fakeAMI struct {
	imageID      string
	ownerID      string
	name         string
	description  string
	creationDate string
}

// newMockEC2Client returns an *ec2.Client whose HTTP transport is replaced with the given mock.
func newMockEC2Client(xmlBody string) *ec2.Client {
	return ec2.New(ec2.Options{
		Region:      "us-east-1",
		Credentials: credentials.NewStaticCredentialsProvider("fake-key", "fake-secret", ""),
		HTTPClient:  &mockEC2HTTPClient{responseXML: xmlBody},
	})
}

const (
	testProductID = "59ead7de-2540-4653-a8b0-fa7926d5c845"
)

func TestFindMarketplaceAMI(t *testing.T) {
	cases := []struct {
		name         string
		images       []fakeAMI
		versionToken string
		wantAMI      string
		wantVersion  string
		wantError    bool
	}{
		{
			name: "single AMI at target version selected",
			images: []fakeAMI{
				{
					imageID:      "ami-target",
					ownerID:      awsMarketplaceOwnerID,
					name:         "RHEL-9.4-RHCOS-9.6_HVM_GA-20260210-x86_64-0-" + testProductID,
					description:  "RHEL CoreOS 9.6 9.6.20260210-0 x86_64",
					creationDate: "2026-02-10T00:00:00.000Z",
				},
			},
			versionToken: "9.6",
			wantAMI:      "ami-target",
			wantVersion:  "9.6.20260210-0",
		},
		{
			name: "AMI newer than target is discarded",
			images: []fakeAMI{
				{
					imageID:      "ami-too-new",
					ownerID:      awsMarketplaceOwnerID,
					name:         "RHEL-9.4-RHCOS-9.7_HVM_GA-20260601-x86_64-0-" + testProductID,
					description:  "RHEL CoreOS 9.7 9.7.20260601-0 x86_64",
					creationDate: "2026-06-01T00:00:00.000Z",
				},
				{
					imageID:      "ami-good",
					ownerID:      awsMarketplaceOwnerID,
					name:         "RHEL-9.4-RHCOS-9.6_HVM_GA-20260210-x86_64-0-" + testProductID,
					description:  "RHEL CoreOS 9.6 9.6.20260210-0 x86_64",
					creationDate: "2026-02-10T00:00:00.000Z",
				},
			},
			versionToken: "9.6",
			wantAMI:      "ami-good",
			wantVersion:  "9.6.20260210-0",
		},
		{
			name: "fallback to older version when target not yet available",
			images: []fakeAMI{
				{
					imageID:      "ami-older",
					ownerID:      awsMarketplaceOwnerID,
					name:         "RHEL-9.4-RHCOS-9.5_HVM_GA-20251001-x86_64-0-" + testProductID,
					description:  "RHEL CoreOS 9.5 9.5.20251001-0 x86_64",
					creationDate: "2025-10-01T00:00:00.000Z",
				},
			},
			versionToken: "9.6",
			wantAMI:      "ami-older",
			wantVersion:  "9.5.20251001-0",
		},
		{
			name: "tie-breaking by newest CreationDate",
			images: []fakeAMI{
				{
					imageID:      "ami-earlier",
					ownerID:      awsMarketplaceOwnerID,
					name:         "RHEL-9.4-RHCOS-9.6_HVM_GA-20260110-x86_64-0-" + testProductID,
					description:  "RHEL CoreOS 9.6 9.6.20260110-0 x86_64",
					creationDate: "2026-01-10T00:00:00.000Z",
				},
				{
					imageID:      "ami-later",
					ownerID:      awsMarketplaceOwnerID,
					name:         "RHEL-9.4-RHCOS-9.6_HVM_GA-20260210-x86_64-0-" + testProductID,
					description:  "RHEL CoreOS 9.6 9.6.20260210-0 x86_64",
					creationDate: "2026-02-10T00:00:00.000Z",
				},
			},
			versionToken: "9.6",
			wantAMI:      "ami-later",
			wantVersion:  "9.6.20260210-0",
		},
		{
			name: "highest version preferred over newer creation date of older version",
			images: []fakeAMI{
				{
					imageID:      "ami-9-5-newer",
					ownerID:      awsMarketplaceOwnerID,
					name:         "RHEL-9.4-RHCOS-9.5_HVM_GA-20260101-x86_64-0-" + testProductID,
					description:  "RHEL CoreOS 9.5 9.5.20260101-0 x86_64",
					creationDate: "2026-01-01T00:00:00.000Z",
				},
				{
					imageID:      "ami-9-6-older",
					ownerID:      awsMarketplaceOwnerID,
					name:         "RHEL-9.4-RHCOS-9.6_HVM_GA-20260210-x86_64-0-" + testProductID,
					description:  "RHEL CoreOS 9.6 9.6.20260210-0 x86_64",
					creationDate: "2025-12-01T00:00:00.000Z",
				},
			},
			versionToken: "9.6",
			wantAMI:      "ami-9-6-older",
			wantVersion:  "9.6.20260210-0",
		},
		{
			name:         "no AMIs available returns empty result",
			images:       []fakeAMI{},
			versionToken: "9.6",
			wantAMI:      "",
			wantVersion:  "",
		},
		{
			name: "only pre-RHEL-aligned AMIs returns empty result with warning",
			images: []fakeAMI{
				{
					imageID:      "ami-old-format",
					ownerID:      awsMarketplaceOwnerID,
					name:         "some-old-ami-" + testProductID,
					description:  "OpenShift 4.18 418.94.202511191518-0 x86_64",
					creationDate: "2025-11-19T15:18:00.000Z",
				},
			},
			versionToken: "9.6",
			wantAMI:      "",
			wantVersion:  "",
		},
		{
			name: "AMI with unparseable description is skipped",
			images: []fakeAMI{
				{
					imageID:      "ami-bad-desc",
					ownerID:      awsMarketplaceOwnerID,
					name:         "some-ami-" + testProductID,
					description:  "no version here",
					creationDate: "2026-02-10T00:00:00.000Z",
				},
				{
					imageID:      "ami-good",
					ownerID:      awsMarketplaceOwnerID,
					name:         "RHEL-9.4-RHCOS-9.6_HVM_GA-20260210-x86_64-0-" + testProductID,
					description:  "RHEL CoreOS 9.6 9.6.20260210-0 x86_64",
					creationDate: "2026-02-10T00:00:00.000Z",
				},
			},
			versionToken: "9.6",
			wantAMI:      "ami-good",
			wantVersion:  "9.6.20260210-0",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := newMockEC2Client(describeImagesXML(tc.images))
			amiID, version, err := findMarketplaceAMI(context.Background(), client, testProductID, tc.versionToken, "test-machineset")
			if tc.wantError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tc.wantAMI, amiID)
				assert.Equal(t, tc.wantVersion, version)
			}
		})
	}
}

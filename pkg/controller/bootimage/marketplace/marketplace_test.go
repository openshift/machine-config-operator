package marketplace

import (
	"testing"

	"github.com/stretchr/testify/assert"
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
			assert.Equal(t, tc.expected, ExtractProductID(tc.amiName))
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
			fullVersion, token, ok := ExtractVersionFromDescription(tc.description)
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
			got := CmpVersionToken(tc.a, tc.b)
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
			got := CmpRHCOSVersion(tc.a, tc.b)
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
			assert.Equal(t, tc.expected, IsPreRHELAlignedToken(tc.token))
		})
	}
}

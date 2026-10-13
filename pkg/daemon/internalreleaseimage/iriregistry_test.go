package internalreleaseimage

import (
	"crypto/tls"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSelectReleaseTag(t *testing.T) {
	releaseTag := "68bdf24405449be5c78a1f27a7b64fc9ee980e4bc3c9b169e8b3da08e50e0389"
	signatureTag := "sha256-68bdf24405449be5c78a1f27a7b64fc9ee980e4bc3c9b169e8b3da08e50e0389.sig"

	cases := []struct {
		name      string
		tags      []string
		expected  string
		expectErr bool
	}{
		{
			name:     "unsigned release payload",
			tags:     []string{releaseTag},
			expected: releaseTag,
		},
		{
			name:     "signed release payload",
			tags:     []string{releaseTag, signatureTag},
			expected: releaseTag,
		},
		{
			name:     "signature listed before the release",
			tags:     []string{signatureTag, releaseTag},
			expected: releaseTag,
		},
		{
			name:      "more than one release is unsupported",
			tags:      []string{releaseTag, "4808c8cae33e5cd3d4f5d1b1f9d8cbb1bcb67e6e9cbbf1f7b0c0e2ba1f1d8e11"},
			expectErr: true,
		},
		{
			name:      "no tags",
			tags:      []string{},
			expectErr: true,
		},
		{
			name:      "only signature tags",
			tags:      []string{signatureTag},
			expectErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tag, err := selectReleaseTag(tc.tags)
			if tc.expectErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.expected, tag)
		})
	}
}

func TestGetOCPReleasePullSpec(t *testing.T) {
	digest := "68bdf24405449be5c78a1f27a7b64fc9ee980e4bc3c9b169e8b3da08e50e0389"
	versionTag := "4.22.16-x86_64"
	manifestEndpoint := "/v2/openshift/release-images/manifests/" + versionTag
	expectedPullSpec := "localhost:22625/openshift/release-images@sha256:" + digest

	// A manifest and the digest it hashes to, used for the version tag cases.
	manifest := "{}"
	manifestDigest := "sha256:44136fa355b3678a1146ad16f7e8649e94fb4fc21fe77e8310c060f61caaff8a"

	cases := []struct {
		name          string
		releaseTag    string
		setupRegistry func(r *FakeIRIRegistry)
		expected      string
		expectErr     bool
	}{
		{
			name:       "a digest tag needs no lookup",
			releaseTag: digest,
			expected:   expectedPullSpec,
		},
		{
			name:       "a version tag is resolved to a digest",
			releaseTag: versionTag,
			setupRegistry: func(r *FakeIRIRegistry) {
				r.AddResponseWithHeaders(manifestEndpoint, http.StatusOK, manifest,
					map[string]string{"Docker-Content-Digest": manifestDigest})
			},
			expected: "localhost:22625/openshift/release-images@" + manifestDigest,
		},
		{
			name:       "the registry reports no digest",
			releaseTag: versionTag,
			setupRegistry: func(r *FakeIRIRegistry) {
				r.AddResponse(manifestEndpoint, http.StatusOK, manifest)
			},
			expectErr: true,
		},
		{
			name:       "the reported digest does not match the manifest",
			releaseTag: versionTag,
			setupRegistry: func(r *FakeIRIRegistry) {
				r.AddResponseWithHeaders(manifestEndpoint, http.StatusOK, manifest,
					map[string]string{"Docker-Content-Digest": "sha256:" + digest})
			},
			expectErr: true,
		},
		{
			name:       "a malformed tag is rejected without querying the registry",
			releaseTag: "4.22.16#unexpected",
			expectErr:  true,
		},
		{
			name:       "the manifest is missing",
			releaseTag: versionTag,
			setupRegistry: func(r *FakeIRIRegistry) {
				r.AddResponse(manifestEndpoint, http.StatusNotFound, `{"errors":[{"code":"MANIFEST_UNKNOWN","message":"manifest unknown"}]}`)
			},
			expectErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.setupRegistry != nil {
				fakeRegistry := NewFakeIRIRegistry()
				tc.setupRegistry(fakeRegistry)
				require.NoError(t, fakeRegistry.Start())
				defer fakeRegistry.Close()
			}

			r := newIRIRegistry("master-0", &http.Client{
				Transport: &http.Transport{
					TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
				},
			}, "")

			pullSpec, err := r.GetOCPReleasePullSpec(tc.releaseTag)
			if tc.expectErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.expected, pullSpec)
		})
	}
}

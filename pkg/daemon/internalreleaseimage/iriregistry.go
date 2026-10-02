package internalreleaseimage

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"regexp"
	"strings"

	"k8s.io/klog/v2"

	"github.com/openshift/machine-config-operator/pkg/daemon/constants"
)

const (
	iriRegistryHost = "localhost"
	iriRegistryPort = 22625

	ocpReleasesRepo = "/openshift/release-images"
	ocpBundlesRepo  = "/openshift/release-bundles"

	// Signed release payloads store a cosign signature in the releases repo,
	// tagged "sha256-<digest>.sig". It is not a release image.
	cosignSignatureTagSuffix = ".sig"

	// dockerContentDigestHeader carries the manifest digest in a registry response.
	dockerContentDigestHeader = "Docker-Content-Digest"
)

var (
	// releaseDigestRe matches a release tag that is a bare image digest, as used by
	// CI and nightly payloads. Release builds are tagged by version instead.
	releaseDigestRe = regexp.MustCompile(`^[a-f0-9]{64}$`)

	// releaseTagRe matches the tag format accepted by the registry.
	releaseTagRe = regexp.MustCompile(`^[a-zA-Z0-9_][a-zA-Z0-9._-]{0,127}$`)

	manifestAcceptHeaders = map[string]string{
		"Accept": "application/vnd.oci.image.index.v1+json, " +
			"application/vnd.oci.image.manifest.v1+json, " +
			"application/vnd.docker.distribution.manifest.list.v2+json, " +
			"application/vnd.docker.distribution.manifest.v2+json",
	}
)

type iriRegistry struct {
	nodeName         string
	registryHostPort string
	client           *http.Client
	// authToken is the base64-encoded "user:password" value from the kubelet
	// auth file for localhost:22625. Empty if the registry is unauthenticated.
	authToken string
}

type registryTagsList struct {
	Name string   `json:"name"`
	Tags []string `json:"tags"`
}

type registryErrorCode struct {
	Code    string      `json:"code"`
	Message string      `json:"message"`
	Detail  interface{} `json:"detail"`
}

type registryErrorResponse struct {
	Errors []registryErrorCode `json:"errors"`
}

func newIRIRegistry(nodeName string, client *http.Client, authToken string) *iriRegistry {
	return &iriRegistry{
		nodeName:         nodeName,
		client:           client,
		registryHostPort: net.JoinHostPort(iriRegistryHost, fmt.Sprintf("%d", iriRegistryPort)),
		authToken:        authToken,
	}
}

// readIRIAuthToken reads the base64-encoded auth token for the IRI registry
// from the kubelet auth file (/var/lib/kubelet/config.json).
func readIRIAuthToken(registryHostPort string) (string, error) {
	data, err := os.ReadFile(constants.KubeletAuthFile)
	if err != nil {
		return "", fmt.Errorf("could not read %s for IRI registry auth: %w", constants.KubeletAuthFile, err)
	}

	var dockerConfig struct {
		Auths map[string]struct {
			Auth string `json:"auth"`
		} `json:"auths"`
	}
	if err := json.Unmarshal(data, &dockerConfig); err != nil {
		return "", fmt.Errorf("could not parse %s for IRI registry auth: %w", constants.KubeletAuthFile, err)
	}

	if entry, ok := dockerConfig.Auths[registryHostPort]; ok && entry.Auth != "" {
		return entry.Auth, nil
	}
	return "", fmt.Errorf("no auth entry found for %s in %s", registryHostPort, constants.KubeletAuthFile)
}

func (r *iriRegistry) query(endpoint string, headers ...map[string]string) (*http.Response, error) {
	regURL := fmt.Sprintf("https://%s/v2%s", r.registryHostPort, endpoint)

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, regURL, nil)
	if err != nil {
		return nil, err
	}
	if r.authToken != "" {
		req.Header.Set("Authorization", "Basic "+r.authToken)
	}
	if len(headers) > 0 {
		for k, v := range headers[0] {
			req.Header.Set(k, v)
		}
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("registry query %s failed with error: %v", regURL, err)
	}

	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		errMsg := fmt.Sprintf("registry query %s failed with code %d", regURL, resp.StatusCode)

		// Check if additional error details are reported in the message body.
		var errResp registryErrorResponse
		if err := json.NewDecoder(resp.Body).Decode(&errResp); err == nil {
			if len(errResp.Errors) > 0 {
				errMsg = fmt.Sprintf("%s. Message: %s. Details: %v", errMsg, errResp.Errors[0].Message, errResp.Errors[0].Detail)
			}
		}
		return nil, fmt.Errorf("%s", errMsg)
	}

	return resp, nil
}

func (r *iriRegistry) CheckLocalRegistry() error {
	klog.V(2).Infof("Checking local InternalReleaseImage registry status for node %s at %s", r.nodeName, r.registryHostPort)

	resp, err := r.query("")
	if err != nil {
		klog.Errorf("No available local InternalReleaseImage registry found for node %s. Error: %v", r.nodeName, err)
		return err
	}
	defer resp.Body.Close()

	klog.V(2).Infof("The local InternalReleaseImage registry is available for node %s (%s)", r.nodeName, r.registryHostPort)
	return nil
}

func (r *iriRegistry) parseTagsList(reader io.Reader) (*registryTagsList, error) {
	var resp registryTagsList

	dec := json.NewDecoder(reader)
	dec.DisallowUnknownFields()

	if err := dec.Decode(&resp); err != nil {
		return nil, fmt.Errorf("decode tags list response: %w", err)
	}
	if resp.Name == "" {
		return nil, fmt.Errorf("missing or empty field %q", "name")
	}
	if resp.Tags == nil {
		resp.Tags = []string{}
	}
	return &resp, nil
}

func (r *iriRegistry) getRepositoryTags(repo string) (*registryTagsList, error) {
	endpoint := fmt.Sprintf("%s/tags/list", repo)

	klog.V(2).Infof("Retrieving repository tags for %s", repo)
	resp, err := r.query(endpoint)
	if err != nil {
		return nil, fmt.Errorf("error while retrieving repository tags for %s: %w", endpoint, err)
	}
	defer resp.Body.Close()

	releaseTags, err := r.parseTagsList(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("error while parsing repository tags for %s: %w", endpoint, err)
	}
	return releaseTags, nil
}

func (r *iriRegistry) GetOCPBundlesTags() (*registryTagsList, error) {
	return r.getRepositoryTags(ocpBundlesRepo)
}

func (r *iriRegistry) GetOCPBundleReleaseTag(_ string) (string, error) {
	// TODO: Replace this temporary implementation by reading the associated
	// release tag via manifest annotation in the bundle image, as soon as
	// https://github.com/openshift/appliance/pull/685 will be completed.
	ocpReleases, err := r.getRepositoryTags(ocpReleasesRepo)
	if err != nil {
		return "", err
	}
	return selectReleaseTag(ocpReleases.Tags)
}

// selectReleaseTag picks the single release tag out of the releases repo,
// ignoring the cosign signature tags added by a signed payload.
func selectReleaseTag(tags []string) (string, error) {
	releaseTags := []string{}
	for _, tag := range tags {
		if !strings.HasSuffix(tag, cosignSignatureTagSuffix) {
			releaseTags = append(releaseTags, tag)
		}
	}

	if len(releaseTags) == 0 {
		return "", fmt.Errorf("no OCP release image found in %s", ocpReleasesRepo)
	}
	if len(releaseTags) > 1 {
		return "", fmt.Errorf("only one OCP release image is currently supported, found %d", len(releaseTags))
	}
	return releaseTags[0], nil
}

// resolveReleaseDigest returns the "sha256:<digest>" reference for a release tag.
// CI and nightly payloads are tagged by digest already; release builds are tagged
// by version, so their digest has to be read from the registry.
func (r *iriRegistry) resolveReleaseDigest(releaseTag string) (string, error) {
	// A malformed tag could otherwise alter the manifest URL, making the registry
	// report the digest of a different image.
	if !releaseTagRe.MatchString(releaseTag) {
		return "", fmt.Errorf("invalid release tag %q in %s", releaseTag, ocpReleasesRepo)
	}

	if releaseDigestRe.MatchString(releaseTag) {
		return "sha256:" + releaseTag, nil
	}

	endpoint := fmt.Sprintf("%s/manifests/%s", ocpReleasesRepo, releaseTag)
	resp, err := r.query(endpoint, manifestAcceptHeaders)
	if err != nil {
		return "", fmt.Errorf("error while resolving the digest for %s: %w", endpoint, err)
	}
	defer resp.Body.Close()

	manifest, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("error while reading the manifest for %s: %w", endpoint, err)
	}

	// The reported digest is only trusted once it matches the manifest it refers to.
	digest := resp.Header.Get(dockerContentDigestHeader)
	if manifestDigest := fmt.Sprintf("sha256:%x", sha256.Sum256(manifest)); digest != manifestDigest {
		return "", fmt.Errorf("registry reported the digest %q for release tag %s, but its manifest is %s", digest, releaseTag, manifestDigest)
	}
	return digest, nil
}

// GetOCPReleasePullSpec builds the pull spec for a release tag. The
// MachineConfigNode API only accepts a release image referenced by digest, so a
// version-tagged release build has its digest resolved first.
func (r *iriRegistry) GetOCPReleasePullSpec(releaseTag string) (string, error) {
	digest, err := r.resolveReleaseDigest(releaseTag)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s%s@%s", r.registryHostPort, ocpReleasesRepo, digest), nil
}

func (r *iriRegistry) CheckImageAvailability(pullspec string) error {
	var pullspecRe = regexp.MustCompile(`^([^/]+)/(.+)@(sha256:[a-f0-9]{64})$`)
	m := pullspecRe.FindStringSubmatch(pullspec)
	if m == nil {
		return fmt.Errorf("invalid pullspec: %s", pullspec)
	}
	registry := m[1]
	repo := m[2]
	digest := m[3]

	if registry != r.registryHostPort {
		return fmt.Errorf("pullspec %s not owned by the current registry", pullspec)
	}

	endpoint := fmt.Sprintf("/%s/manifests/%s", repo, digest)
	resp, err := r.query(endpoint, manifestAcceptHeaders)
	if err != nil {
		return fmt.Errorf("error while checking image availability for %s: %w", endpoint, err)
	}
	defer resp.Body.Close()

	return nil
}

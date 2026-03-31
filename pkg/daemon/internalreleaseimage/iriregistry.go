package internalreleaseimage

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"

	"k8s.io/klog/v2"
)

const (
	iriRegistryHost = "localhost"
	iriRegistryPort = 22625

	ocpReleasesRepo = "/openshift/release-images"
	ocpBundlesRepo  = "/openshift/release-bundles"
)

type iriRegistry struct {
	nodeName         string
	registryHostPort string
	client           *http.Client
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

func newIRIRegistry(nodeName string, client *http.Client) *iriRegistry {
	return &iriRegistry{
		nodeName: nodeName,
		client:   client,
		// The IRI registry runs on the current node.
		registryHostPort: fmt.Sprintf("%s:%d", iriRegistryHost, iriRegistryPort),
	}
}

func (r *iriRegistry) query(endpoint string, headers ...map[string]string) (*http.Response, error) {
	regURL := fmt.Sprintf("https://%s/v2%s", r.registryHostPort, endpoint)

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, regURL, nil)
	if err != nil {
		return nil, err
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
	endpoint := fmt.Sprintf("/%s/tags/list", repo)

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
	// Note: currently the IRI resource supports only one release bundle, and thus one OCP release. Since the release bundle
	// image does not yet contain the necessary release metadata (see https://redhat.atlassian.net/browse/AGENT-1312),
	// let's fetch directly the current release image.
	ocpReleases, err := r.getRepositoryTags(ocpReleasesRepo)
	if err != nil {
		return "", err
	}
	if len(ocpReleases.Tags) > 1 {
		return "", fmt.Errorf("only one OCP release image is currently supported")
	}
	return ocpReleases.Tags[0], nil
}

func (r *iriRegistry) GetOCPReleasePullSpec(releaseTag string) string {
	return fmt.Sprintf("%s%s@sha256:%s", r.registryHostPort, ocpReleasesRepo, releaseTag)
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
	resp, err := r.query(endpoint, map[string]string{
		"Accept": "application/vnd.oci.image.index.v1+json, " +
			"application/vnd.oci.image.manifest.v1+json, " +
			"application/vnd.docker.distribution.manifest.list.v2+json, " +
			"application/vnd.docker.distribution.manifest.v2+json"})
	if err != nil {
		return fmt.Errorf("error while checking image availability for %s: %w", endpoint, err)
	}
	defer resp.Body.Close()

	return nil
}

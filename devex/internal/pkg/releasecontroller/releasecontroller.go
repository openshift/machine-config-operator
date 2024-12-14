package releasecontroller

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
)

type ReleaseController string

func (r *ReleaseController) GetAllReleaseStreams() ([]string, error) {
	tmp := map[string][]string{}
	err := r.doHTTPRequestIntoStruct("/api/v1/releasestreams/all", &tmp)
	out := []string{}
	for key := range tmp {
		out = append(out, key)
	}
	return out, err
}

func (r *ReleaseController) GetLatestReleaseForStream(stream string) (*Release, error) {
	out := &Release{}
	err := r.doHTTPRequestIntoStruct(filepath.Join("/api/v1/releasestream", stream, "latest"), out)
	return out, err
}

func (r *ReleaseController) GetAllReleasesForStream(stream string) (*ReleaseTags, error) {
	out := &ReleaseTags{}
	err := r.doHTTPRequestIntoStruct(filepath.Join("/api/v1/releasestream", stream, "tags"), out)
	return out, err
}

func (r *ReleaseController) GetReleaseStatus(stream, tag string) (*APIReleaseInfo, error) {
	out := &APIReleaseInfo{}
	err := r.doHTTPRequestIntoStruct(filepath.Join("/api/v1/releasestream", stream, "release", tag), out)
	return out, err
}

// https://amd64.ocp.releases.ci.openshift.org/releasetag/4.15.0-0.nightly-2023-11-28-101923/json
//
// This returns raw bytes for now so we can use a dynamic JSON pathing library
// to parse it to avoid fighting with go mod.
//
// The raw bytes returned are very similar to the ones returned by $ oc adm
// release info. The sole difference seems to be that $ oc adm release info
// returns the fully qualified pullspec for the release instead of the tagged
// pullspec.
func (r *ReleaseController) GetReleaseInfo(tag string) ([]byte, error) {
	return r.doHTTPRequestIntoBytes(filepath.Join("releasetag", tag, "json"))
}

func (r *ReleaseController) doHTTPRequest(path string) (*http.Response, error) {
	u := url.URL{
		Scheme: "https",
		Host:   string(*r),
		Path:   path,
	}

	resp, err := http.Get(u.String())

	if err != nil {
		return nil, err
	}

	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("got HTTP 404 from %s", u.String())
	}

	return resp, nil
}

func (r *ReleaseController) doHTTPRequestIntoStruct(path string, out interface{}) error {
	resp, err := r.doHTTPRequest(path)
	if err != nil {
		return err
	}

	defer resp.Body.Close()

	return json.NewDecoder(resp.Body).Decode(out)
}

func (r *ReleaseController) doHTTPRequestIntoBytes(path string) ([]byte, error) {
	resp, err := r.doHTTPRequest(path)
	if err != nil {
		return nil, err
	}

	defer resp.Body.Close()

	out := bytes.NewBuffer([]byte{})

	if _, err := io.Copy(out, resp.Body); err != nil {
		return nil, err
	}

	return out.Bytes(), nil
}

const (
	Amd64OcpReleaseController   ReleaseController = "amd64.ocp.releases.ci.openshift.org"
	Arm64OcpReleaseController   ReleaseController = "arm64.ocp.releases.ci.openshift.org"
	Ppc64leOcpReleaseController ReleaseController = "ppc64le.ocp.releases.ci.openshift.org"
	S390xOcpReleaseController   ReleaseController = "s390x.ocp.releases.ci.openshift.org"
	MultiOcpReleaseController   ReleaseController = "multi.ocp.releases.ci.openshift.org"
	Amd64OkdReleaseController   ReleaseController = "amd64.origin.releases.ci.openshift.org"
)

type ReleaseTags struct {
	Name string    `json:"name"`
	Tags []Release `json:"tags"`
}

type Release struct {
	Name        string `json:"name"`
	Phase       string `json:"phase"`
	Pullspec    string `json:"pullSpec"`
	DownloadURL string `json:"downloadURL"`
}

func GetReleaseController(kind, arch string) (ReleaseController, error) {
	rcs := map[string]map[string]ReleaseController{
		"ocp": {
			"amd64": Amd64OcpReleaseController,
			"arm64": Arm64OcpReleaseController,
			"multi": MultiOcpReleaseController,
		},
		"okd": {
			"amd64": Amd64OkdReleaseController,
		},
		"okd-scos": {
			"amd64": Amd64OkdReleaseController,
		},
	}

	if _, ok := rcs[kind]; !ok {
		return "", fmt.Errorf("invalid kind %q", kind)
	}

	if _, ok := rcs[kind][arch]; !ok {
		return "", fmt.Errorf("invalid arch %q for kind %q", arch, kind)
	}

	return rcs[kind][arch], nil
}

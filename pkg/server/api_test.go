package server

import (
	"fmt"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/coreos/go-semver/semver"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime"

	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/test/helpers"
)

type mockServer struct {
	GetConfigFn func(poolRequest) (*runtime.RawExtension, error)
}

func (ms *mockServer) GetConfig(pr poolRequest) (*runtime.RawExtension, error) {
	return ms.GetConfigFn(pr)
}

type checkResponse func(t *testing.T, response *http.Response)

type scenario struct {
	name          string
	request       *http.Request
	serverFunc    func(poolRequest) (*runtime.RawExtension, error)
	checkResponse checkResponse
}

const expectedContentLength int = 32

type acceptHeaderScenario struct {
	name       string
	input      string
	headerVals []acceptHeaderValue
	versionOut *semver.Version
}

func float32ToPtr(f32 float32) *float32 {
	return &f32
}

func TestAcceptHeaders(t *testing.T) {
	v2_2 := semver.New("2.2.0")
	v2_4 := semver.New("2.4.0")
	v3_1 := semver.New("3.1.0")
	headers := []acceptHeaderScenario{
		{
			name:  "IgnV0",
			input: "application/vnd.coreos.ignition+json; version=2.4.0, application/vnd.coreos.ignition+json; version=1; q=0.5, */*; q=0.1",
			headerVals: []acceptHeaderValue{
				{
					MIMEType:    "application",
					MIMESubtype: "vnd.coreos.ignition+json",
					SemVer:      v2_4,
					QValue:      float32ToPtr(1.0),
				},
				{
					MIMEType:    "application",
					MIMESubtype: "vnd.coreos.ignition+json",
					SemVer:      nil, // 1 is not a valid semver
					QValue:      float32ToPtr(0.5),
				},
				{
					MIMEType:    "*",
					MIMESubtype: "*",
					SemVer:      nil,
					QValue:      float32ToPtr(0.1),
				},
			},
			versionOut: v2_2,
		},
		{
			name:  "IgnV2",
			input: "application/vnd.coreos.ignition+json;version=3.1.0, */*;q=0.1",
			headerVals: []acceptHeaderValue{
				{
					MIMEType:    "application",
					MIMESubtype: "vnd.coreos.ignition+json",
					SemVer:      v3_1,
					QValue:      float32ToPtr(1.0),
				},
				{
					MIMEType:    "*",
					MIMESubtype: "*",
					SemVer:      nil,
					QValue:      float32ToPtr(0.1),
				},
			},
			versionOut: v3_1,
		},
		{
			name:  "IgnNoVersion",
			input: "application/vnd.coreos.ignition+json",
			headerVals: []acceptHeaderValue{
				{
					MIMEType:    "application",
					MIMESubtype: "vnd.coreos.ignition+json",
					SemVer:      nil,
					QValue:      float32ToPtr(1.0),
				},
			},
			versionOut: v2_2,
		},
		{
			name:  "Text",
			input: "text/plain",
			headerVals: []acceptHeaderValue{
				{
					MIMEType:    "text",
					MIMESubtype: "plain",
					SemVer:      nil,
					QValue:      float32ToPtr(1.0),
				},
			},
			versionOut: v2_2,
		},
		{
			name:       "Invalid",
			input:      "an-invalid-accept-header",
			headerVals: nil,
			versionOut: v2_2,
		},
	}

	for _, header := range headers {
		t.Run(header.name, func(t *testing.T) {
			headers, _ := parseAcceptHeader(header.input)
			assert.Equal(t, header.headerVals, headers)
			version, err := detectSpecVersionFromAcceptHeader(header.input)
			assert.Equal(t, header.versionOut, version)
			require.NoError(t, err)
		})
	}
}

func setAcceptHeaderOnReq(req *http.Request) *http.Request {
	req.Header.Set("Accept", "application/vnd.coreos.ignition+json; version=2.4.0, application/vnd.coreos.ignition+json; version=1; q=0.5, */*; q=0.1")
	return req
}

func setV3AcceptHeaderOnReq(req *http.Request) *http.Request {
	req.Header.Set("Accept", "application/vnd.coreos.ignition+json;version=3.1.0, */*;q=0.1")
	return req
}

func TestAPIHandler(t *testing.T) {
	scenarios := []scenario{
		{
			name:    "get config path that does not exist",
			request: setAcceptHeaderOnReq(httptest.NewRequest(http.MethodGet, "http://testrequest/config/does-not-exist", nil)),
			serverFunc: func(poolRequest) (*runtime.RawExtension, error) {
				return &runtime.RawExtension{
					Raw: helpers.MarshalOrDie(ctrlcommon.NewIgnConfig()),
				}, fmt.Errorf("not acceptable")
			},
			checkResponse: func(t *testing.T, response *http.Response) {
				checkStatus(t, response, http.StatusInternalServerError)
				checkContentLength(t, response, 0)
				checkBodyLength(t, response, 0)
			},
		},
		{
			name:    "get config path that exists",
			request: setAcceptHeaderOnReq(httptest.NewRequest(http.MethodGet, "http://testrequest/config/master", nil)),
			serverFunc: func(poolRequest) (*runtime.RawExtension, error) {
				return &runtime.RawExtension{
					Raw: helpers.MarshalOrDie(ctrlcommon.NewIgnConfig()),
				}, nil
			},
			checkResponse: func(t *testing.T, response *http.Response) {
				checkStatus(t, response, http.StatusOK)
				checkContentType(t, response, "application/json")
				checkContentLength(t, response, expectedContentLength)
				checkBodyLength(t, response, expectedContentLength)
			},
		},
		{
			name:    "head config path that exists",
			request: setAcceptHeaderOnReq(httptest.NewRequest(http.MethodHead, "http://testrequest/config/master", nil)),
			serverFunc: func(poolRequest) (*runtime.RawExtension, error) {
				return &runtime.RawExtension{
					Raw: helpers.MarshalOrDie(ctrlcommon.NewIgnConfig()),
				}, nil
			},
			checkResponse: func(t *testing.T, response *http.Response) {
				checkStatus(t, response, http.StatusOK)
				checkContentType(t, response, "application/json")
				checkContentLength(t, response, expectedContentLength)
				checkBodyLength(t, response, 0)
			},
		},
		{
			name:    "post config path that exists",
			request: setAcceptHeaderOnReq(httptest.NewRequest(http.MethodPost, "http://testrequest/config/master", nil)),
			serverFunc: func(poolRequest) (*runtime.RawExtension, error) {
				return &runtime.RawExtension{
					Raw: helpers.MarshalOrDie(ctrlcommon.NewIgnConfig()),
				}, nil
			},
			checkResponse: func(t *testing.T, response *http.Response) {
				checkStatus(t, response, http.StatusMethodNotAllowed)
				checkContentLength(t, response, 0)
				checkBodyLength(t, response, 0)
			},
		},
		{
			name:    "get spec v3 config path that exists",
			request: setV3AcceptHeaderOnReq(httptest.NewRequest(http.MethodGet, "http://testrequest/config/master", nil)),
			serverFunc: func(poolRequest) (*runtime.RawExtension, error) {
				return &runtime.RawExtension{
					Raw: helpers.MarshalOrDie(ctrlcommon.NewIgnConfig()),
				}, nil
			},
			checkResponse: func(t *testing.T, response *http.Response) {
				checkStatus(t, response, http.StatusOK)
				checkContentType(t, response, "application/json")
				checkContentLength(t, response, expectedContentLength)
				checkBodyLength(t, response, expectedContentLength)
			},
		},
	}

	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			ms := &mockServer{
				GetConfigFn: scenario.serverFunc,
			}
			handler := NewServerAPIHandler(ms)
			handler.ServeHTTP(w, scenario.request)

			resp := w.Result()
			defer resp.Body.Close()
			scenario.checkResponse(t, resp)
		})
	}
}

func TestHealthzHandler(t *testing.T) {
	scenarios := []scenario{
		{
			name:    "get healthz",
			request: httptest.NewRequest(http.MethodGet, "http://testrequest/healthz", nil),
			serverFunc: func(poolRequest) (*runtime.RawExtension, error) {
				return &runtime.RawExtension{
					Raw: helpers.MarshalOrDie(ctrlcommon.NewIgnConfig()),
				}, nil
			},
			checkResponse: func(t *testing.T, response *http.Response) {
				checkStatus(t, response, http.StatusOK)
				checkContentLength(t, response, 0)
				checkBodyLength(t, response, 0)
			},
		},
		{
			name:    "head healthz",
			request: httptest.NewRequest(http.MethodHead, "http://testrequest/healthz", nil),
			serverFunc: func(poolRequest) (*runtime.RawExtension, error) {
				return &runtime.RawExtension{
					Raw: helpers.MarshalOrDie(ctrlcommon.NewIgnConfig()),
				}, nil
			},
			checkResponse: func(t *testing.T, response *http.Response) {
				checkStatus(t, response, http.StatusOK)
				checkContentLength(t, response, 0)
				checkBodyLength(t, response, 0)
			},
		},
		{
			name:    "post healthz",
			request: httptest.NewRequest(http.MethodPost, "http://testrequest/healthz", nil),
			serverFunc: func(poolRequest) (*runtime.RawExtension, error) {
				return &runtime.RawExtension{
					Raw: helpers.MarshalOrDie(ctrlcommon.NewIgnConfig()),
				}, nil
			},
			checkResponse: func(t *testing.T, response *http.Response) {
				checkStatus(t, response, http.StatusMethodNotAllowed)
				checkContentLength(t, response, 0)
				checkBodyLength(t, response, 0)
			},
		},
	}
	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			handler := &healthHandler{}
			handler.ServeHTTP(w, scenario.request)

			resp := w.Result()
			defer resp.Body.Close()
			scenario.checkResponse(t, resp)
		})
	}
}

func TestDefaultHandler(t *testing.T) {
	scenarios := []scenario{
		{
			name:    "get root",
			request: httptest.NewRequest(http.MethodGet, "http://testrequest/", nil),
			serverFunc: func(poolRequest) (*runtime.RawExtension, error) {
				return &runtime.RawExtension{
					Raw: helpers.MarshalOrDie(ctrlcommon.NewIgnConfig()),
				}, nil
			},
			checkResponse: func(t *testing.T, response *http.Response) {
				checkStatus(t, response, http.StatusNotFound)
				checkContentLength(t, response, 0)
				checkBodyLength(t, response, 0)
			},
		},
		{
			name:    "head root",
			request: httptest.NewRequest(http.MethodHead, "http://testrequest/", nil),
			serverFunc: func(poolRequest) (*runtime.RawExtension, error) {
				return &runtime.RawExtension{
					Raw: helpers.MarshalOrDie(ctrlcommon.NewIgnConfig()),
				}, nil
			},
			checkResponse: func(t *testing.T, response *http.Response) {
				checkStatus(t, response, http.StatusNotFound)
				checkContentLength(t, response, 0)
				checkBodyLength(t, response, 0)
			},
		},
		{
			name:    "post root",
			request: httptest.NewRequest(http.MethodPost, "http://testrequest/", nil),
			serverFunc: func(poolRequest) (*runtime.RawExtension, error) {
				return &runtime.RawExtension{
					Raw: helpers.MarshalOrDie(ctrlcommon.NewIgnConfig()),
				}, nil
			},
			checkResponse: func(t *testing.T, response *http.Response) {
				checkStatus(t, response, http.StatusMethodNotAllowed)
				checkContentLength(t, response, 0)
				checkBodyLength(t, response, 0)
			},
		},
	}
	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			handler := &defaultHandler{}
			handler.ServeHTTP(w, scenario.request)

			resp := w.Result()
			defer resp.Body.Close()
			scenario.checkResponse(t, resp)
		})
	}
}

func TestAPIServer(t *testing.T) {
	scenarios := []scenario{
		{
			name:    "get config path that does not exist",
			request: setAcceptHeaderOnReq(httptest.NewRequest(http.MethodGet, "http://testrequest/config/does-not-exist", nil)),
			serverFunc: func(poolRequest) (*runtime.RawExtension, error) {
				return &runtime.RawExtension{
					Raw: helpers.MarshalOrDie(ctrlcommon.NewIgnConfig()),
				}, fmt.Errorf("not acceptable")
			},
			checkResponse: func(t *testing.T, response *http.Response) {
				checkStatus(t, response, http.StatusInternalServerError)
				checkContentLength(t, response, 0)
				checkBodyLength(t, response, 0)
			},
		},
		{
			name:    "get config path that exists",
			request: setAcceptHeaderOnReq(httptest.NewRequest(http.MethodGet, "http://testrequest/config/master", nil)),
			serverFunc: func(poolRequest) (*runtime.RawExtension, error) {
				return &runtime.RawExtension{
					Raw: helpers.MarshalOrDie(ctrlcommon.NewIgnConfig()),
				}, nil
			},
			checkResponse: func(t *testing.T, response *http.Response) {
				checkStatus(t, response, http.StatusOK)
				checkContentType(t, response, "application/json")
				checkContentLength(t, response, expectedContentLength)
				checkBodyLength(t, response, expectedContentLength)
			},
		},
		{
			name:    "head config path that exists",
			request: setAcceptHeaderOnReq(httptest.NewRequest(http.MethodHead, "http://testrequest/config/master", nil)),
			serverFunc: func(poolRequest) (*runtime.RawExtension, error) {
				return &runtime.RawExtension{
					Raw: helpers.MarshalOrDie(ctrlcommon.NewIgnConfig()),
				}, nil
			},
			checkResponse: func(t *testing.T, response *http.Response) {
				checkStatus(t, response, http.StatusOK)
				checkContentType(t, response, "application/json")
				checkContentLength(t, response, expectedContentLength)
				checkBodyLength(t, response, 0)
			},
		},
		{
			name:    "post config path that exists",
			request: setAcceptHeaderOnReq(httptest.NewRequest(http.MethodPost, "http://testrequest/config/master", nil)),
			serverFunc: func(poolRequest) (*runtime.RawExtension, error) {
				return &runtime.RawExtension{
					Raw: helpers.MarshalOrDie(ctrlcommon.NewIgnConfig()),
				}, nil
			},
			checkResponse: func(t *testing.T, response *http.Response) {
				checkStatus(t, response, http.StatusMethodNotAllowed)
				checkContentLength(t, response, 0)
				checkBodyLength(t, response, 0)
			},
		},
		{
			name:    "get healthz",
			request: setAcceptHeaderOnReq(httptest.NewRequest(http.MethodGet, "http://testrequest/healthz", nil)),
			serverFunc: func(poolRequest) (*runtime.RawExtension, error) {
				return &runtime.RawExtension{
					Raw: helpers.MarshalOrDie(ctrlcommon.NewIgnConfig()),
				}, nil
			},
			checkResponse: func(t *testing.T, response *http.Response) {
				checkStatus(t, response, http.StatusOK)
				checkContentLength(t, response, 0)
				checkBodyLength(t, response, 0)
			},
		},
		{
			name:    "head healthz",
			request: setAcceptHeaderOnReq(httptest.NewRequest(http.MethodHead, "http://testrequest/healthz", nil)),
			serverFunc: func(poolRequest) (*runtime.RawExtension, error) {
				return &runtime.RawExtension{
					Raw: helpers.MarshalOrDie(ctrlcommon.NewIgnConfig()),
				}, nil
			},
			checkResponse: func(t *testing.T, response *http.Response) {
				checkStatus(t, response, http.StatusOK)
				checkContentLength(t, response, 0)
				checkBodyLength(t, response, 0)
			},
		},
		{
			name:    "post healthz",
			request: setAcceptHeaderOnReq(httptest.NewRequest(http.MethodPost, "http://testrequest/healthz", nil)),
			serverFunc: func(poolRequest) (*runtime.RawExtension, error) {
				return &runtime.RawExtension{
					Raw: helpers.MarshalOrDie(ctrlcommon.NewIgnConfig()),
				}, nil
			},
			checkResponse: func(t *testing.T, response *http.Response) {
				checkStatus(t, response, http.StatusMethodNotAllowed)
				checkContentLength(t, response, 0)
				checkBodyLength(t, response, 0)
			},
		},
		{
			name:    "get root",
			request: setAcceptHeaderOnReq(httptest.NewRequest(http.MethodGet, "http://testrequest/", nil)),
			serverFunc: func(poolRequest) (*runtime.RawExtension, error) {
				return &runtime.RawExtension{
					Raw: helpers.MarshalOrDie(ctrlcommon.NewIgnConfig()),
				}, nil
			},
			checkResponse: func(t *testing.T, response *http.Response) {
				checkStatus(t, response, http.StatusNotFound)
				checkContentLength(t, response, 0)
				checkBodyLength(t, response, 0)
			},
		},
		{
			name:    "head root",
			request: setAcceptHeaderOnReq(httptest.NewRequest(http.MethodHead, "http://testrequest/", nil)),
			serverFunc: func(poolRequest) (*runtime.RawExtension, error) {
				return &runtime.RawExtension{
					Raw: helpers.MarshalOrDie(ctrlcommon.NewIgnConfig()),
				}, nil
			},
			checkResponse: func(t *testing.T, response *http.Response) {
				checkStatus(t, response, http.StatusNotFound)
				checkContentLength(t, response, 0)
				checkBodyLength(t, response, 0)
			},
		},
		{
			name:    "post root",
			request: setAcceptHeaderOnReq(httptest.NewRequest(http.MethodPost, "http://testrequest/", nil)),
			serverFunc: func(poolRequest) (*runtime.RawExtension, error) {
				return &runtime.RawExtension{
					Raw: helpers.MarshalOrDie(ctrlcommon.NewIgnConfig()),
				}, nil
			},
			checkResponse: func(t *testing.T, response *http.Response) {
				checkStatus(t, response, http.StatusMethodNotAllowed)
				checkContentLength(t, response, 0)
				checkBodyLength(t, response, 0)
			},
		},
	}
	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			ms := &mockServer{
				GetConfigFn: scenario.serverFunc,
			}
			server := NewAPIServer(NewServerAPIHandler(ms), 0, false, "", "")
			server.handler.ServeHTTP(w, scenario.request)

			resp := w.Result()
			defer resp.Body.Close()
			scenario.checkResponse(t, resp)
		})
	}
}

func checkStatus(t *testing.T, response *http.Response, expected int) {
	if response.StatusCode != expected {
		t.Errorf("expected response status %d, received %d", expected, response.StatusCode)
	}
}

func checkContentType(t *testing.T, response *http.Response, expected string) {
	actual := response.Header.Get("Content-Type")
	if actual != expected {
		t.Errorf("expected response Content-Type %q, received %q", expected, actual)
	}
}

func checkContentLength(t *testing.T, response *http.Response, l int) {
	if int(response.ContentLength) != l {
		t.Errorf("expected response Content-Length %d, received %d", l, int(response.ContentLength))
	}
}

func checkBodyLength(t *testing.T, response *http.Response, l int) {
	body, err := ioutil.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if len(body) != l {
		t.Errorf("expected response body length %d, received %d", l, len(body))
	}
}

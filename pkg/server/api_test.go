package server

import (
	"fmt"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"testing"

	igntypes "github.com/coreos/ignition/config/v2_2/types"
	"github.com/openshift/machine-config-operator/test/helpers"
	"k8s.io/apimachinery/pkg/runtime"
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

const expectedContentLength int = 15

func TestAPIHandler(t *testing.T) {
	scenarios := []scenario{
		{
			name:    "get config path that does not exist",
			request: httptest.NewRequest(http.MethodGet, "http://testrequest/config/does-not-exist", nil),
			serverFunc: func(poolRequest) (*runtime.RawExtension, error) {
				return &runtime.RawExtension{
					Raw: helpers.MarshalOrDie(new(igntypes.Config)),
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
			request: httptest.NewRequest(http.MethodGet, "http://testrequest/config/master", nil),
			serverFunc: func(poolRequest) (*runtime.RawExtension, error) {
				return &runtime.RawExtension{
					Raw: helpers.MarshalOrDie(new(igntypes.Config)),
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
			request: httptest.NewRequest(http.MethodHead, "http://testrequest/config/master", nil),
			serverFunc: func(poolRequest) (*runtime.RawExtension, error) {
				return &runtime.RawExtension{
					Raw: helpers.MarshalOrDie(new(igntypes.Config)),
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
			request: httptest.NewRequest(http.MethodPost, "http://testrequest/config/master", nil),
			serverFunc: func(poolRequest) (*runtime.RawExtension, error) {
				return &runtime.RawExtension{
					Raw: helpers.MarshalOrDie(new(igntypes.Config)),
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
					Raw: helpers.MarshalOrDie(new(igntypes.Config)),
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
					Raw: helpers.MarshalOrDie(new(igntypes.Config)),
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
					Raw: helpers.MarshalOrDie(new(igntypes.Config)),
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
					Raw: helpers.MarshalOrDie(new(igntypes.Config)),
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
					Raw: helpers.MarshalOrDie(new(igntypes.Config)),
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
					Raw: helpers.MarshalOrDie(new(igntypes.Config)),
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
			request: httptest.NewRequest(http.MethodGet, "http://testrequest/config/does-not-exist", nil),
			serverFunc: func(poolRequest) (*runtime.RawExtension, error) {
				return &runtime.RawExtension{
					Raw: helpers.MarshalOrDie(new(igntypes.Config)),
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
			request: httptest.NewRequest(http.MethodGet, "http://testrequest/config/master", nil),
			serverFunc: func(poolRequest) (*runtime.RawExtension, error) {
				return &runtime.RawExtension{
					Raw: helpers.MarshalOrDie(new(igntypes.Config)),
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
			request: httptest.NewRequest(http.MethodHead, "http://testrequest/config/master", nil),
			serverFunc: func(poolRequest) (*runtime.RawExtension, error) {
				return &runtime.RawExtension{
					Raw: helpers.MarshalOrDie(new(igntypes.Config)),
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
			request: httptest.NewRequest(http.MethodPost, "http://testrequest/config/master", nil),
			serverFunc: func(poolRequest) (*runtime.RawExtension, error) {
				return &runtime.RawExtension{
					Raw: helpers.MarshalOrDie(new(igntypes.Config)),
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
			request: httptest.NewRequest(http.MethodGet, "http://testrequest/healthz", nil),
			serverFunc: func(poolRequest) (*runtime.RawExtension, error) {
				return &runtime.RawExtension{
					Raw: helpers.MarshalOrDie(new(igntypes.Config)),
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
					Raw: helpers.MarshalOrDie(new(igntypes.Config)),
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
					Raw: helpers.MarshalOrDie(new(igntypes.Config)),
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
			request: httptest.NewRequest(http.MethodGet, "http://testrequest/", nil),
			serverFunc: func(poolRequest) (*runtime.RawExtension, error) {
				return &runtime.RawExtension{
					Raw: helpers.MarshalOrDie(new(igntypes.Config)),
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
					Raw: helpers.MarshalOrDie(new(igntypes.Config)),
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
					Raw: helpers.MarshalOrDie(new(igntypes.Config)),
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

package server

import (
	"crypto/tls"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/coreos/go-semver/semver"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/http2"
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
	v3_2 := semver.New("3.2.0")
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
			input: "application/vnd.coreos.ignition+json;version=3.2.0, */*;q=0.1",
			headerVals: []acceptHeaderValue{
				{
					MIMEType:    "application",
					MIMESubtype: "vnd.coreos.ignition+json",
					SemVer:      v3_2,
					QValue:      float32ToPtr(1.0),
				},
				{
					MIMEType:    "*",
					MIMESubtype: "*",
					SemVer:      nil,
					QValue:      float32ToPtr(0.1),
				},
			},
			versionOut: v3_2,
		},
		{
			name:  "IgnV2_31",
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

func setV3_1AcceptHeaderOnReq(req *http.Request) *http.Request {
	req.Header.Set("Accept", "application/vnd.coreos.ignition+json;version=3.1.0, */*;q=0.1")
	return req
}

func setV3AcceptHeaderOnReq(req *http.Request) *http.Request {
	req.Header.Set("Accept", "application/vnd.coreos.ignition+json;version=3.2.0, */*;q=0.1")
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
			name:    "get spec v3_1 config path that exists",
			request: setV3_1AcceptHeaderOnReq(httptest.NewRequest(http.MethodGet, "http://testrequest/config/master", nil)),
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

func TestAPIServerCiphers(t *testing.T) {
	certData, err := tls.X509KeyPair(
		[]byte(`-----BEGIN CERTIFICATE-----
MIIBhTCCASugAwIBAgIQIRi6zePL6mKjOipn+dNuaTAKBggqhkjOPQQDAjASMRAw
DgYDVQQKEwdBY21lIENvMB4XDTE3MTAyMDE5NDMwNloXDTE4MTAyMDE5NDMwNlow
EjEQMA4GA1UEChMHQWNtZSBDbzBZMBMGByqGSM49AgEGCCqGSM49AwEHA0IABD0d
7VNhbWvZLWPuj/RtHFjvtJBEwOkhbN/BnnE8rnZR8+sbwnc/KhCk3FhnpHZnQz7B
5aETbbIgmuvewdjvSBSjYzBhMA4GA1UdDwEB/wQEAwICpDATBgNVHSUEDDAKBggr
BgEFBQcDATAPBgNVHRMBAf8EBTADAQH/MCkGA1UdEQQiMCCCDmxvY2FsaG9zdDo1
NDUzgg4xMjcuMC4wLjE6NTQ1MzAKBggqhkjOPQQDAgNIADBFAiEA2zpJEPQyz6/l
Wf86aX6PepsntZv2GYlA5UpabfT2EZICICpJ5h/iI+i341gBmLiAFQOyTDT+/wQc
6MF9+Yw1Yy0t
-----END CERTIFICATE-----`),
		[]byte(`-----BEGIN EC PRIVATE KEY-----
MHcCAQEEIIrYSSNQFaA2Hwf1duRSxKtLYX5CB04fSeQ6tF1aY/PuoAoGCCqGSM49
AwEHoUQDQgAEPR3tU2Fta9ktY+6P9G0cWO+0kETA6SFs38GecTyudlHz6xvCdz8q
EKTcWGekdmdDPsHloRNtsiCa697B2O9IFA==
-----END EC PRIVATE KEY-----`),
	)
	if err != nil {
		t.Fatalf("failed to create test x509 cert")
	}

	ts := getHTTPServerCfg(
		fmt.Sprintf(":%d", 8088),
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintln(w, "Hello, client")
		}),
	)

	// Configure the TLS testing to allow
	ts.TLSConfig.Certificates = []tls.Certificate{certData}
	ts.TLSConfig.InsecureSkipVerify = true

	go func(t *testing.T) {
		if err := ts.ListenAndServeTLS("", ""); err != http.ErrServerClosed {
			log.Fatal("error creating test server")
		}
	}(t)
	defer ts.Close()

	type testCase struct {
		desc      string
		wantErr   bool
		errString string
		client    *http.Client
		ciphers   []uint16
	}

	tests := []testCase{
		{
			desc:    "ensure base TLS configuration",
			wantErr: false,
			client: &http.Client{
				Transport: &http.Transport{
					TLSClientConfig: &tls.Config{
						InsecureSkipVerify: true, // test server certificate is not trusted.
					},
				},
			},
		},
		{
			desc:      "http/2 should fail",
			wantErr:   true,
			errString: "http2: unexpected ALPN protocol",
			client: &http.Client{
				Transport: &http2.Transport{
					TLSClientConfig: &tls.Config{
						InsecureSkipVerify: true, // test server certificate is not trusted.
					},
				},
			},
		},
		{
			desc:      "TLS1.1 should fail",
			wantErr:   true,
			errString: "protocol version not supported",
			client: &http.Client{
				Transport: &http.Transport{
					TLSClientConfig: &tls.Config{
						MaxVersion:         tls.VersionTLS11,
						InsecureSkipVerify: true, // test server certificate is not trusted.
					},
				},
			},
		},
		{
			desc:    "TLS1.2 should work",
			wantErr: false,
			client: &http.Client{
				Transport: &http.Transport{
					TLSClientConfig: &tls.Config{
						MaxVersion:         tls.VersionTLS12,
						InsecureSkipVerify: true, // test server certificate is not trusted.
					},
				},
			},
		},
	}

	// Test that all known insecure ciphers are straight up refused.
	for _, c := range tls.CipherSuites() {
		if c.Insecure || strings.HasSuffix(c.Name, "CBC_SHA") || strings.HasSuffix(c.Name, "DES") {
			tests = append(
				tests,
				testCase{
					desc:    fmt.Sprintf("refuse insecure ciphers %v", c.Name),
					ciphers: []uint16{c.ID},
					client: &http.Client{
						Transport: &http.Transport{
							TLSClientConfig: &tls.Config{
								CipherSuites:       []uint16{c.ID},
								InsecureSkipVerify: true, // test server certificate is not trusted.
							},
						},
					},
				})
		}
	}

	for idx, c := range tests {
		t.Run(fmt.Sprintf("case#%d: %s", idx, c.desc), func(t *testing.T) {
			resp, err := c.client.Get("https://localhost:8088")
			for _, insecure := range c.ciphers {
				if insecure == resp.TLS.CipherSuite {
					t.Fatalf("failed to %s:", c.desc)
				}
			}
			if c.wantErr {
				if !strings.Contains(fmt.Sprintf("%v", err), c.errString) {
					t.Fatalf("want: %s\n got: %v", c.errString, err)
				}
			}
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

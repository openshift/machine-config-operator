package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	ignv2_2types "github.com/coreos/ignition/config/v2_2/types"
)

type mockServer struct {
	GetConfigFn func(poolRequest) (*ignv2_2types.Config, error)
}

func (ms *mockServer) GetConfig(pr poolRequest) (*ignv2_2types.Config, error) {
	return ms.GetConfigFn(pr)
}

type scenario struct {
	request        *http.Request
	expectedStatus int
	serverFunc     func(poolRequest) (*ignv2_2types.Config, error)
}

func TestAPIHandler(t *testing.T) {
	scenarios := []scenario{
		{
			request:        httptest.NewRequest("GET", "http://testrequest/does-not-exist", nil),
			expectedStatus: http.StatusNotFound,
			serverFunc: func(poolRequest) (*ignv2_2types.Config, error) {
				return nil, nil
			},
		},
		{
			request:        httptest.NewRequest("GET", "http://testrequest/config/does-not-exist", nil),
			expectedStatus: http.StatusInternalServerError,
			serverFunc: func(poolRequest) (*ignv2_2types.Config, error) {
				return new(ignv2_2types.Config), fmt.Errorf("not acceptable")
			},
		},
		{
			request:        httptest.NewRequest("GET", "http://testrequest/config/master", nil),
			expectedStatus: http.StatusOK,
			serverFunc: func(poolRequest) (*ignv2_2types.Config, error) {
				return new(ignv2_2types.Config), nil
			},
		},
	}

	for _, scenario := range scenarios {
		t.Run(scenario.request.URL.Path, func(t *testing.T) {
			w := httptest.NewRecorder()
			ms := &mockServer{
				GetConfigFn: scenario.serverFunc,
			}
			handler := NewServerAPIHandler(ms)
			handler.ServeHTTP(w, scenario.request)

			resp := w.Result()

			if resp.StatusCode != scenario.expectedStatus {
				t.Errorf("API Handler test failed: expected: %d, received: %d", scenario.expectedStatus, resp.StatusCode)
			}
		})
	}
}

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
	name           string
	expectedStatus int
	serverFunc     func(poolRequest) (*ignv2_2types.Config, error)
}

func TestAPIHandler(t *testing.T) {
	scenarios := []scenario{
		{
			name:           "not-found",
			expectedStatus: http.StatusNotFound,
			serverFunc: func(poolRequest) (*ignv2_2types.Config, error) {
				return nil, nil
			},
		},
		{
			name:           "internal-server",
			expectedStatus: http.StatusInternalServerError,
			serverFunc: func(poolRequest) (*ignv2_2types.Config, error) {
				return new(ignv2_2types.Config), fmt.Errorf("not acceptable")
			},
		},
		{
			name:           "success",
			expectedStatus: http.StatusOK,
			serverFunc: func(poolRequest) (*ignv2_2types.Config, error) {
				return new(ignv2_2types.Config), nil
			},
		},
	}

	for i := range scenarios {
		req := httptest.NewRequest("GET", "http://testrequest/", nil)
		w := httptest.NewRecorder()
		ms := &mockServer{
			GetConfigFn: scenarios[i].serverFunc,
		}
		handler := NewServerAPIHandler(ms)
		handler.ServeHTTP(w, req)

		resp := w.Result()

		if resp.StatusCode != scenarios[i].expectedStatus {
			t.Errorf("API Handler test failed for: %s, expected: %d, received: %d", scenarios[i].name, scenarios[i].expectedStatus, resp.StatusCode)
		}
	}
}

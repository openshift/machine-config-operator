package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"path"

	ignv2_2types "github.com/coreos/ignition/config/v2_2/types"
	"github.com/golang/glog"
)

const (
	apiPathConfig = "/config/"
)

type poolRequest struct {
	machinePool string
}

type getConfig func(request poolRequest) (*ignv2_2types.Config, error)

func newHandler(getConfig getConfig) http.Handler {
	mux := http.NewServeMux()
	mux.Handle(apiPathConfig, &configHandler{getConfig: getConfig})
	mux.Handle("/", &defaultHandler{})
	return mux
}

// configHandler is the HTTP Handler for machine configuration.
type configHandler struct {
	getConfig getConfig
}

// ServeHTTP handles the requests for the machine config server
// API handler.
func (h *configHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Content-Length", "0")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	if r.URL.Path == "" {
		w.Header().Set("Content-Length", "0")
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	cr := poolRequest{
		machinePool: path.Base(r.URL.Path),
	}

	conf, err := h.getConfig(cr)
	if err != nil {
		w.Header().Set("Content-Length", "0")
		w.WriteHeader(http.StatusInternalServerError)
		glog.Errorf("couldn't get config for req: %v, error: %v", cr, err)
		return
	}
	if conf == nil && err == nil {
		w.Header().Set("Content-Length", "0")
		w.WriteHeader(http.StatusNotFound)
		return
	}

	data, err := json.Marshal(conf)
	if err != nil {
		w.Header().Set("Content-Length", "0")
		w.WriteHeader(http.StatusInternalServerError)
		glog.Errorf("failed to marshal %v config: %v", cr, err)
		return
	}

	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(data)))
	w.Header().Set("Content-Type", "application/json")
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}

	_, err = w.Write(data)
	if err != nil {
		glog.Errorf("failed to write %v response: %v", cr, err)
	}
}

// defaultHandler is the HTTP Handler for backstopping invalid requests.
type defaultHandler struct{}

// ServeHTTP handles invalid requests.
func (h *defaultHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Length", "0")
	w.WriteHeader(http.StatusNotFound)
	return
}

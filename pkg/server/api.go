package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"path"

	"github.com/golang/glog"
)

const (
	apiPathConfig = "/config/"
)

type poolRequest struct {
	machinePool string
}

// APIServer provides the HTTP(s) endpoint
// for providing the machine configs.
type APIServer struct {
	handler  *APIHandler
	port     int
	insecure bool
	cert     string
	key      string
}

// NewAPIServer initializes a new API server
// that runs the Machine Config Server as a
// handler.
func NewAPIServer(a *APIHandler, p int, is bool, c, k string) *APIServer {
	return &APIServer{
		handler:  a,
		port:     p,
		insecure: is,
		cert:     c,
		key:      k,
	}
}

// Serve launches the API Server.
func (a *APIServer) Serve() {
	mux := http.NewServeMux()
	mux.Handle(apiPathConfig, a.handler)

	mcs := &http.Server{
		Addr:    fmt.Sprintf(":%v", a.port),
		Handler: mux,
	}

	glog.Info("launching server")
	if a.insecure {
		// Serve a non TLS server.
		if err := mcs.ListenAndServe(); err != http.ErrServerClosed {
			glog.Exitf("Machine Config Server exited with error: %v", err)
		}
	} else {
		if err := mcs.ListenAndServeTLS(a.cert, a.key); err != http.ErrServerClosed {
			glog.Exitf("Machine Config Server exited with error: %v", err)
		}
	}
}

// APIHandler is the HTTP Handler for the
// Machine Config Server.
type APIHandler struct {
	server Server
}

// NewServerAPIHandler initializes a new API handler
// for the Machine Config Server.
func NewServerAPIHandler(s Server) *APIHandler {
	return &APIHandler{
		server: s,
	}
}

// ServeHTTP handles the requests for the machine config server
// API handler.
func (sh *APIHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
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

	conf, err := sh.server.GetConfig(cr)
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

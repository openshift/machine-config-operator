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
	apiParamEtcd  = "etcd_index"
)

type poolRequest struct {
	machinePool string
	etcdIndex   string
}

// APIServer provides the HTTP(s) endpoint
// for providing the machine configs.
type APIServer struct {
	handler *APIHandler
	port    int
	debug   bool
	cert    string
	key     string
}

// NewAPIServer initializes a new API server
// that runs the Machine Config Server as a
// handler.
func NewAPIServer(a *APIHandler, p int, d bool, c, k string) *APIServer {
	return &APIServer{
		handler: a,
		port:    p,
		debug:   d,
		cert:    c,
		key:     k,
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
	if a.debug {
		// Serve a non TLS server for debugging
		glog.Warning("INSECURE MODE only for testings")
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

	if r.URL.Path == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	cr := poolRequest{
		machinePool: path.Base(r.URL.Path),
		etcdIndex:   r.URL.Query().Get(apiParamEtcd),
	}

	conf, err := sh.server.GetConfig(cr)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	if conf == nil && err == nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	encoder := json.NewEncoder(w)
	if err := encoder.Encode(conf); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
	}
}

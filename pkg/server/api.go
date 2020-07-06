package server

import (
	"crypto/tls"
	"fmt"
	"net/http"
	"path"
	"strings"

	"github.com/clarketm/json"
	"github.com/coreos/go-semver/semver"
	"github.com/golang/glog"
	"github.com/pkg/errors"
	"k8s.io/apimachinery/pkg/runtime"

	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
)

type poolRequest struct {
	machineConfigPool string
}

// APIServer provides the HTTP(s) endpoint
// for providing the machine configs.
type APIServer struct {
	handler  http.Handler
	port     int
	insecure bool
	cert     string
	key      string
}

// NewAPIServer initializes a new API server
// that runs the Machine Config Server as a
// handler.
func NewAPIServer(a *APIHandler, p int, is bool, c, k string) *APIServer {
	mux := http.NewServeMux()
	mux.Handle("/config/", a)
	mux.Handle("/healthz", &healthHandler{})
	mux.Handle("/", &defaultHandler{})

	return &APIServer{
		handler:  mux,
		port:     p,
		insecure: is,
		cert:     c,
		key:      k,
	}
}

// Serve launches the API Server.
func (a *APIServer) Serve() {
	mcs := &http.Server{
		Addr:    fmt.Sprintf(":%v", a.port),
		Handler: a.handler,
		// We don't want to allow 1.1 as that's old.  This was flagged in a security audit.
		TLSConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
		},
	}

	glog.Infof("Launching server on %s", mcs.Addr)
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
		machineConfigPool: path.Base(r.URL.Path),
	}

	useragent := r.Header.Get("User-Agent")
	glog.Infof("Pool %s requested by address:%q User-Agent:%q", cr.machineConfigPool, r.RemoteAddr, useragent)

	reqConfigVer, err := deriveSpecVersionFromUseragent(useragent)
	if err != nil {
		w.Header().Set("Content-Length", "0")
		w.WriteHeader(http.StatusBadRequest)
		glog.Errorf("invalid useragent: %v", err)
		return
	}

	conf, err := sh.server.GetConfig(cr)
	if err != nil {
		w.Header().Set("Content-Length", "0")
		w.WriteHeader(http.StatusInternalServerError)
		glog.Errorf("couldn't get config for req: %v, error: %v", cr, err)
		return
	}
	if conf == nil {
		w.Header().Set("Content-Length", "0")
		w.WriteHeader(http.StatusNotFound)
		return
	}

	// the internally saved config is always spec v2.2
	// so translation is only necessary when spec v3.1 is requested
	var serveConf *runtime.RawExtension
	if reqConfigVer.Equal(*semver.New("3.1.0")) {
		converted3, err := ctrlcommon.ConvertRawExtIgnition2to3(conf)
		if err != nil {
			w.Header().Set("Content-Length", "0")
			w.WriteHeader(http.StatusInternalServerError)
			glog.Errorf("couldn't convert config for req: %v, error: %v", cr, err)
			return
		}

		serveConf = &converted3
	} else {
		serveConf = conf
	}

	data, err := json.Marshal(serveConf)
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

type healthHandler struct{}

// deriveSpecVersionFromUseragent returns a supported Ignition config spec version for a given Ignition user agent
// or an error for unsuported user agents.
// Only Ignition versions that come with support for either spec v2.2 or spec v3.1 are supported
func deriveSpecVersionFromUseragent(useragent string) (*semver.Version, error) {
	ignVersionString := strings.SplitAfter(useragent, "/")[1]
	ignSemver, err := semver.NewVersion(ignVersionString)
	if err != nil {
		return ignSemver, errors.Errorf("invalid version in useragent: %s", useragent)
	}

	if !ignSemver.LessThan(*semver.New("0.22.0")) && ignSemver.LessThan(*semver.New("1.0.0")) {
		// versions [0.22.0;1) support config spec v2.2, and
		return semver.New("2.2.0"), nil
	} else if !ignSemver.LessThan(*semver.New("2.3.0")) && ignSemver.LessThan(*semver.New("3.0.0")) {
		// versions [2.3.0;3) support config spec v3.1
		return semver.New("3.1.0"), nil
	}

	return nil, errors.Errorf("unsupported version in useragent: %s", useragent)
}

// ServeHTTP handles /healthz requests.
func (h *healthHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Length", "0")
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}

	w.WriteHeader(http.StatusMethodNotAllowed)
	return
}

// defaultHandler is the HTTP Handler for backstopping invalid requests.
type defaultHandler struct{}

// ServeHTTP handles invalid requests.
func (h *defaultHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Length", "0")
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	w.WriteHeader(http.StatusMethodNotAllowed)
	return
}

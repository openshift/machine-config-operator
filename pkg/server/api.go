package server

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"
	"path"
	"strings"

	"github.com/coreos/go-semver/semver"
	ign2types "github.com/coreos/ignition/config/v2_2/types"
	ign3types "github.com/coreos/ignition/v2/config/v3_1/types"
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

	data, translated, err := translateConfigForIgnitionMiddleware(conf, useragent)
	if err != nil {
		w.Header().Set("Content-Length", "0")
		w.WriteHeader(http.StatusInternalServerError)
		if translated {
			glog.Errorf("failed to translate %v config: %v", cr, err)
		} else {
			glog.Errorf("failed to prepare %v config: %v", cr, err)
		}
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

// translateConfigForIgnitionMiddleware outputs the Ignition config in a spec version
// understandable for the useragent and indicates whether or not the config has been
// translated
func translateConfigForIgnitionMiddleware(rawConf *runtime.RawExtension, useragent string) ([]byte, bool, error) {
	translated := false

	var ignSemver *semver.Version
	var err error
	// Default to Ignition v2.3.0 (so this function serves config spec v3.1) for non-Ignition user agents
	if !strings.HasPrefix(useragent, "Ignition/") {
		ignSemver = semver.New("2.3.0")
	} else {
		ignVersionString := strings.SplitAfter(useragent, "/")[1]
		ignSemver, err = semver.NewVersion(ignVersionString)
		if err != nil {
			return nil, translated, errors.Errorf("invalid version in useragent: %s", useragent)
		}
	}

	v1 := semver.New("1.0.0")
	v2 := semver.New("2.0.0")
	v3 := semver.New("3.0.0")
	var reqCfgVersion semver.Version
	if ignSemver.LessThan(*v1) {
		// Ignition v0.x supprts config spec v2.x
		reqCfgVersion = *v2
	} else if !ignSemver.LessThan(*v2) && ignSemver.LessThan(*v3) {
		// Ignition v2.x supports config spec v3.x
		reqCfgVersion = *v3
	} else {
		return nil, translated, errors.Errorf("config version not supported: %s", ignSemver.String())
	}

	confi, err := ctrlcommon.IgnParseWrapper(rawConf.Raw)
	if err != nil {
		return nil, translated, errors.Errorf("couldn't parse rendered config: %v", err)
	}

	var serveIgn interface{}
	switch typedConfig := confi.(type) {
	case ign3types.Config:
		if reqCfgVersion == *v2 {
			translated = true
			converted2, err := ctrlcommon.ConvertIgnition3to2(typedConfig)
			if err != nil {
				return nil, translated, errors.Errorf("failed to convert Ignition config spec v3 to v2: %v", err)
			}
			serveIgn = converted2
		} else if reqCfgVersion == *v3 {
			serveIgn = typedConfig
		}
	case ign2types.Config:
		if reqCfgVersion == *v3 {
			translated = true
			converted3, err := ctrlcommon.ConvertIgnition2to3(typedConfig)
			if err != nil {
				return nil, translated, errors.Errorf("failed to convert Ignition config spec v2 to v3: %v", err)
			}
			serveIgn = converted3
		} else if reqCfgVersion == *v2 {
			serveIgn = typedConfig
		}
	default:
		return nil, translated, errors.Errorf("unexpected type for ignition config: %v", typedConfig)
	}

	serveData, err := json.Marshal(serveIgn)
	if err != nil {
		return nil, translated, errors.Errorf("failed to marshal Ignition config: %v", err)
	}

	return serveData, translated, nil
}

type healthHandler struct{}

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

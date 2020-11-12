package server

import (
	"crypto/tls"
	"fmt"
	"net/http"
	"path"
	"sort"
	"strconv"
	"strings"

	"github.com/clarketm/json"
	"github.com/coreos/go-semver/semver"
	"github.com/golang/glog"
	"github.com/pkg/errors"
	"k8s.io/apimachinery/pkg/runtime"

	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
)

const (
	// SecurePort is the tls secured port to serve ignition configs
	SecurePort = 22623
	// InsecurePort is the port to serve ignition configs w/o tls
	InsecurePort = 22624
)

type poolRequest struct {
	machineConfigPool string
	// The provisioning token, see https://github.com/openshift/enhancements/pull/443
	token   string
	version *semver.Version
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

	poolName := path.Base(r.URL.Path)
	useragent := r.Header.Get("User-Agent")
	acceptHeader := r.Header.Get("Accept")
	q := r.URL.Query()
	token := q.Get("token")
	tokenProvided := token != ""
	glog.Infof("Pool %s requested by address:%q User-Agent:%q Accept-Header: %q TokenPresent: %v", poolName, r.RemoteAddr, useragent, acceptHeader, tokenProvided)

	reqConfigVer, err := detectSpecVersionFromAcceptHeader(acceptHeader)
	if err != nil {
		w.Header().Set("Content-Length", "0")
		w.WriteHeader(http.StatusBadRequest)
		glog.Error(err)
		return
	}

	cr := poolRequest{
		machineConfigPool: poolName,
		token:             token,
		version:           reqConfigVer,
	}

	conf, err := sh.server.GetConfig(cr)
	if err != nil {
		w.Header().Set("Content-Length", "0")
		if IsForbidden(err) {
			w.WriteHeader(http.StatusForbidden)
			glog.Infof("Denying unauthorized request: %v", err)
		} else {
			w.WriteHeader(http.StatusInternalServerError)
			glog.Errorf("couldn't get config for req: %v, error: %v", cr, err)
		}
		return
	}
	if conf == nil {
		w.Header().Set("Content-Length", "0")
		w.WriteHeader(http.StatusNotFound)
		return
	}
	// we know we're at 3.1 in code.. serve directly, parsing is expensive...
	// we're doing it during an HTTP request, and most notably before we write the HTTP headers
	var serveConf *runtime.RawExtension
	if reqConfigVer.Equal(*semver.New("3.1.0")) {
		serveConf = conf
	} else {
		// Can only be 2.2 here
		converted2, err := ctrlcommon.ConvertRawExtIgnitionToV2(conf)
		if err != nil {
			w.Header().Set("Content-Length", "0")
			w.WriteHeader(http.StatusInternalServerError)
			glog.Errorf("couldn't convert config for req: %v, error: %v", cr, err)
			return
		}

		serveConf = &converted2
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

type acceptHeaderValue struct {
	MIMEType    string
	MIMESubtype string
	SemVer      *semver.Version
	QValue      *float32
}

// Parse an accept header, ignoring any extensions that aren't
// either version or relative quality factor q.
func parseAcceptHeader(input string) ([]acceptHeaderValue, error) {
	var header []acceptHeaderValue

	values := strings.Split(input, ",")
	for _, value := range values {
		// remove spaces
		value = strings.TrimSpace(value)
		parts := strings.Split(value, ";")
		for i := range parts {
			parts[i] = strings.TrimSpace(parts[i])
		}

		if !strings.Contains(parts[0], "/") {
			// This is not a MIME type, ignore bad data
			continue
		}
		// mtype[0] is the main MIME type, mtype[1] is the sub MIME type
		mtype := strings.SplitN(parts[0], "/", 2)

		// check value extensions for version and q parameters, ignore other extensions
		var v *semver.Version
		var q *float32
		for _, ext := range parts[1:] {
			if strings.Contains(ext, "=") {
				keyval := strings.SplitN(ext, "=", 2)
				if keyval[0] == "version" && v == nil {
					var err error
					v, err = semver.NewVersion(keyval[1])
					if err != nil {
						// This is not a valid version
						continue
					}
				} else if keyval[0] == "q" && q == nil {
					q64, err := strconv.ParseFloat(keyval[1], 32)
					if err != nil {
						// This is not a valid relative quality factor
						continue
					}
					qval := float32(q64)
					q = &qval
				}
			}
		}

		// Default q to 1
		if q == nil {
			q1 := float32(1.0)
			q = &q1
		}

		header = append(header, acceptHeaderValue{
			mtype[0],
			mtype[1],
			v,
			q,
		})
	}

	if len(header) == 0 {
		return nil, errors.New("no valid accept header detected")
	}

	// Sort headers by descending q factor value.
	// This is the order of precedence any application
	// that receives this header should operate with.
	sort.SliceStable(header, func(i, j int) bool { return *header[i].QValue > *header[j].QValue })

	return header, nil
}

// detectSpecVersionFromAcceptHeaderUseragent returns a supported Ignition config spec version for a given Accept header.
// For non-Ignition Accept headers it defaults to config spec v2.2.0
func detectSpecVersionFromAcceptHeader(acceptHeader string) (*semver.Version, error) {
	// for now, serve v2 if we receive a request without an Ignition accept header.
	// This happens if the user pings the endpoint directly (e.g. with curl)
	// and we don't want to break existing behaviour.
	// For Ignition v0.x, the accept header looks like:
	// "application/vnd.coreos.ignition+json; version=2.4.0, application/vnd.coreos.ignition+json; version=1; q=0.5, */*; q=0.1".
	// For v2.x, it looks like:
	// "application/vnd.coreos.ignition+json;version=3.1.0, */*;q=0.1".
	v2_2 := semver.New("2.2.0")
	v3_1 := semver.New("3.1.0")

	var ignVersionError error
	headers, err := parseAcceptHeader(acceptHeader)
	if err != nil {
		// no valid accept headers detected at all, serve default
		return v2_2, nil
	}

	for _, header := range headers {
		if header.MIMESubtype == "vnd.coreos.ignition+json" && header.SemVer != nil {
			if !header.SemVer.LessThan(*v3_1) && header.SemVer.LessThan(*semver.New("4.0.0")) {
				return v3_1, nil
			} else if !header.SemVer.LessThan(*v2_2) && header.SemVer.LessThan(*semver.New("3.0.0")) {
				return v2_2, nil
			}
			ignVersionError = errors.Errorf("unsupported Ignition version in Accept header: %s", acceptHeader)
		}
	}
	// return error if version of Ignition MIME subtype is not supported
	if ignVersionError != nil {
		return nil, ignVersionError
	}

	// default to serving spec v2.2 for all non-Ignition headers
	// as well as Ignition headers without a version specified.
	return v2_2, nil
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

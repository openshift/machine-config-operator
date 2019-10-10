package daemon

import (
	"context"
	"net/http"

	"github.com/golang/glog"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// DefaultBindAddress is the port for the metrics listener
var DefaultBindAddress = ":8797"

// StartMetricsListener is metrics listener via http on localhost
func StartMetricsListener(addr string, stopCh chan struct{}) {
	if addr == "" {
		addr = DefaultBindAddress
	}

	glog.Infof("Starting metrics listener on %s", addr)
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	s := http.Server{Addr: addr, Handler: mux}
	go func() {
		if err := s.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			glog.Errorf("metrics listener exited with error: %v", err)
		}
	}()
	<-stopCh
	if err := s.Shutdown(context.Background()); err != http.ErrServerClosed {
		glog.Errorf("error stopping metrics listener: %v", err)
	}
}

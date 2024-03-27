package webhook

import (
	"fmt"
	"net/http"

	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	configv1 "github.com/openshift/api/config/v1"
	mcfginformersv1alpha1 "github.com/openshift/client-go/machineconfiguration/informers/externalversions/machineconfiguration/v1alpha1"
	mcfglistersv1alpha1 "github.com/openshift/client-go/machineconfiguration/listers/machineconfiguration/v1alpha1"
	"github.com/openshift/library-go/pkg/operator/configobserver/featuregates"
)

// NewConfig returns a new ServerConfig.
func NewConfig(port int, certDir string) *Config {
	return &Config{
		Port:    fmt.Sprintf(":%d", port),
		CertDir: certDir,
	}
}

// Config is the configuration for the webhook server.
type Config struct {
	Port    string
	CertDir string
}

// Server is a webhook server.
type Server struct {
	mux                  *http.ServeMux
	cfg                  *Config
	imageSetLister       mcfglistersv1alpha1.PinnedImageSetLister
	imageSetListerSynced cache.InformerSynced
	fgAccessor           featuregates.FeatureGateAccess
}

// NewServer returns a new webhook server instance which uses the controller
// runtime admission package in standalone mode.
func NewServer(
	cfg *Config,
	imageSetInformer mcfginformersv1alpha1.PinnedImageSetInformer,
	fgAccessor featuregates.FeatureGateAccess,
) *Server {
	w := &Server{
		cfg:        cfg,
		mux:        http.NewServeMux(),
		fgAccessor: fgAccessor,
	}
	w.imageSetLister = imageSetInformer.Lister()
	w.imageSetListerSynced = imageSetInformer.Informer().HasSynced
	return w
}

// Run starts the webhook server.
func (s *Server) Run(stopCh <-chan struct{}) {
	fg, err := s.fgAccessor.CurrentFeatureGates()
	if err != nil {
		klog.Errorf("Could not get feature gate: %v", err)
		return
	}
	if !fg.Enabled(configv1.FeatureGatePinnedImages) {
		klog.Info("FeatureGatePinnedImages is not enabled, skipped starting webhook server")
		return
	}
	cache.WaitForCacheSync(stopCh, s.imageSetListerSynced)

	machineConfigPoolValidator, err := NewMachineConfigPoolValidator(s.imageSetLister)
	if err != nil {
		klog.Fatalf("Failed to create we hook: %v", err)
	}

	if err := s.RegisterValidatingWebHook(machineConfigPoolValidatingHookPath, machineConfigPoolValidator); err != nil {
		klog.Fatalf("Failed to register webhook: %v", err)
	}

	certFile := fmt.Sprintf("%s/tls.crt", s.cfg.CertDir)
	keyFile := fmt.Sprintf("%s/tls.key", s.cfg.CertDir)
	if err := http.ListenAndServeTLS(s.cfg.Port, certFile, keyFile, s.mux); err != nil {
		klog.Fatalf("Failed to start webhook server: %v", err)
	}
}

// RegisterValidatingWebHook registers a validating webhook.
func (w *Server) RegisterValidatingWebHook(path string, hook *admission.Webhook) error {
	validatingHookHandler, err := admission.StandaloneWebhook(hook, admission.StandaloneOptions{})
	if err != nil {
		return err
	}
	w.mux.Handle(path, validatingHookHandler)
	return nil
}

// RegisterMutatingWebHook registers a mutating webhook.
func (w *Server) RegisterMutatingWebHook(path string, hook *admission.Webhook) error {
	mutatingHookHandler, err := admission.StandaloneWebhook(hook, admission.StandaloneOptions{})
	if err != nil {
		return err
	}
	w.mux.Handle(path, mutatingHookHandler)
	return nil
}

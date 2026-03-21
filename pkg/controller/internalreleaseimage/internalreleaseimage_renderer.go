package internalreleaseimage

import (
	"bytes"
	"crypto/tls"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/clarketm/json"
	ign3types "github.com/coreos/ignition/v2/config/v3_5/types"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	configv1 "github.com/openshift/api/config/v1"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	mcfgv1alpha1 "github.com/openshift/api/machineconfiguration/v1alpha1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	templatectrl "github.com/openshift/machine-config-operator/pkg/controller/template"
	"github.com/openshift/machine-config-operator/pkg/version"
)

var (
	//go:embed templates/*
	templatesFS embed.FS

	// List of supported roles for generating the machine configs.
	// Templates folders are organized by those roles.
	SupportedRoles = []string{"master", "worker"}

	// Format of the name for the InternalReleaseImage machine configs.
	machineConfigNameFmt = "02-%s-internalreleaseimage"
)

// Renderer takes care of generating the required ignition (by role) for
// the InternalReleaseImage machine config resources. It can also create
// a MachineConfig instance when required.
type Renderer struct {
	role       string
	iri        *mcfgv1alpha1.InternalReleaseImage
	iriSecret  *corev1.Secret
	cconfig    *mcfgv1.ControllerConfig
	tlsProfile *configv1.TLSSecurityProfile
}

// NewRendererByRole creates a new Renderer instance for generating
// the machine config for the given role.
func NewRendererByRole(role string, iri *mcfgv1alpha1.InternalReleaseImage, iriSecret *corev1.Secret, cconfig *mcfgv1.ControllerConfig, tlsProfile *configv1.TLSSecurityProfile) *Renderer {
	return &Renderer{
		role:       role,
		iri:        iri,
		iriSecret:  iriSecret,
		cconfig:    cconfig,
		tlsProfile: tlsProfile,
	}
}

// GetMachineConfigName returns the name of the MachineConfig instance.
func (r *Renderer) GetMachineConfigName() string {
	return fmt.Sprintf(machineConfigNameFmt, r.role)
}

// CreateEmptyMachineConfig creates an empty MachineConfig (without any ignition configured) owned by InternalReleaseImage.
func (r *Renderer) CreateEmptyMachineConfig() (*mcfgv1.MachineConfig, error) {
	mc, err := ctrlcommon.MachineConfigFromIgnConfig(r.role, r.GetMachineConfigName(), ctrlcommon.NewIgnConfig())
	if err != nil {
		return nil, err
	}

	cref := metav1.NewControllerRef(r.iri, controllerKind)
	mc.SetOwnerReferences([]metav1.OwnerReference{*cref})
	mc.SetAnnotations(map[string]string{
		ctrlcommon.GeneratedByControllerVersionAnnotationKey: version.Hash,
	})
	return mc, nil
}

// RenderAndSetIgnition generates the required ignition for the given role,
// and sets it on the specified MachineConfig.
func (r *Renderer) RenderAndSetIgnition(mc *mcfgv1.MachineConfig) error {
	rc, err := r.newRenderContext()
	if err != nil {
		return err
	}

	ignCfg, err := r.generateIgnitionFromTemplates(rc)
	if err != nil {
		return err
	}

	rawIgn, err := json.Marshal(ignCfg)
	if err != nil {
		return err
	}

	mc.Spec.Config.Raw = rawIgn
	return nil
}

// renderContext is a type used to hold the configuration required
// for current the template rendering.
type renderContext struct {
	DockerRegistryImage string
	IriTLSKey           string
	IriTLSCert          string
	RootCA              string
	TLSMinVersion       string
	TLSCipherSuites     string
}

// newRenderContext creates a new renderContext instance.
func (r *Renderer) newRenderContext() (*renderContext, error) {
	iriTLSKey, err := r.extractTLSCertFieldFromSecret(r.iriSecret, "tls.key")
	if err != nil {
		return nil, err
	}
	iriTLSCert, err := r.extractTLSCertFieldFromSecret(r.iriSecret, "tls.crt")
	if err != nil {
		return nil, err
	}

	tlsMinVersion, tlsCipherSuites := registryTLSFromProfile(r.tlsProfile)

	return &renderContext{
		DockerRegistryImage: r.cconfig.Spec.Images[templatectrl.DockerRegistryKey],
		IriTLSKey:           iriTLSKey,
		IriTLSCert:          iriTLSCert,
		RootCA:              string(r.cconfig.Spec.RootCAData),
		TLSMinVersion:       tlsMinVersion,
		TLSCipherSuites:     tlsCipherSuites,
	}, nil
}

// registryTLSFromProfile converts an OpenShift TLSSecurityProfile to the
// Distribution registry's environment variable values for REGISTRY_HTTP_TLS_MINIMUMTLS
// and REGISTRY_HTTP_TLS_CIPHERSUITES.
func registryTLSFromProfile(profile *configv1.TLSSecurityProfile) (minVersion, cipherSuites string) {
	tlsVersion, ciphers := ctrlcommon.GetSecurityProfileCiphers(profile)
	minVersion = openShiftTLSVersionToRegistryVersion(tlsVersion)

	// Filter to only configurable cipher suites (TLS 1.2 and below). TLS 1.3+
	// cipher suites are fixed by the protocol and cannot be configured, so they
	// are excluded. This is future-proof: if a future TLS version also uses
	// fixed cipher suites, its ciphers won't appear in Go's configurable set
	// and will be filtered out automatically.
	configurableCiphers := buildConfigurableCipherSet()
	var filtered []string
	for _, c := range ciphers {
		if configurableCiphers[c] {
			filtered = append(filtered, c)
		}
	}

	if len(filtered) == 0 {
		return minVersion, ""
	}
	// Format as a YAML array because the Distribution registry parses environment
	// variables as YAML values, and CIPHERSUITES is a []string field.
	return minVersion, "[" + strings.Join(filtered, ", ") + "]"
}

// buildConfigurableCipherSet returns a set of IANA cipher suite names that
// can be configured via tls.Config.CipherSuites (i.e., TLS 1.2 and below).
// TLS 1.3+ cipher suites are not included because they are fixed by the protocol.
func buildConfigurableCipherSet() map[string]bool {
	cipherSet := make(map[string]bool)
	for _, cs := range tls.CipherSuites() {
		if isConfigurableCipher(cs) {
			cipherSet[cs.Name] = true
		}
	}
	for _, cs := range tls.InsecureCipherSuites() {
		cipherSet[cs.Name] = true
	}
	return cipherSet
}

// isConfigurableCipher returns true if the cipher suite supports any TLS version
// below 1.3. Cipher suites that only support TLS 1.3+ are fixed by the protocol
// and cannot be configured, so they should not be passed to the registry.
func isConfigurableCipher(cs *tls.CipherSuite) bool {
	for _, v := range cs.SupportedVersions {
		if v < tls.VersionTLS13 {
			return true
		}
	}
	return false
}

// openShiftTLSVersionToRegistryVersion converts an OpenShift TLS version string
// (e.g. "VersionTLS12") to the Distribution registry format (e.g. "tls1.2").
// The conversion is done programmatically so future TLS versions (e.g. "VersionTLS14")
// are handled automatically without code changes.
func openShiftTLSVersionToRegistryVersion(version string) string {
	const prefix = "VersionTLS"
	if !strings.HasPrefix(version, prefix) {
		return "tls1.2" // default to Intermediate
	}
	digits := strings.TrimPrefix(version, prefix)
	if len(digits) < 2 {
		return "tls1.2"
	}
	return "tls" + string(digits[0]) + "." + digits[1:]
}

// extractTLSCertFieldFromSecret is an helper func to get the specified secret field data.
func (r *Renderer) extractTLSCertFieldFromSecret(secret *corev1.Secret, fieldName string) (string, error) {
	raw, found := secret.Data[fieldName]
	if !found {
		return "", fmt.Errorf("cannot find %s in secret %s", fieldName, secret.Name)
	}
	return string(raw), nil
}

// generateIgnitionFromTemplates creates the required ignition for the given roles
// using the InternalReleaseImage templates.
func (r *Renderer) generateIgnitionFromTemplates(rc *renderContext) (*ign3types.Config, error) {
	// Render template subfolders, if defined.
	units, err := r.renderTemplateFolder(rc, filepath.Join(r.role, "units"))
	if err != nil {
		return nil, err
	}
	files, err := r.renderTemplateFolder(rc, filepath.Join(r.role, "files"))
	if err != nil {
		return nil, err
	}

	ignCfg, err := ctrlcommon.TranspileCoreOSConfigToIgn(files, units)
	if err != nil {
		return nil, fmt.Errorf("error transpiling CoreOS config to Ignition config: %w", err)
	}
	return ignCfg, nil
}

// renderTemplateFolder renders all the templates found in the specified folder.
func (r *Renderer) renderTemplateFolder(rc any, folder string) ([]string, error) {
	tmplFolder := filepath.Join("templates", folder)

	files := []string{}
	entries, err := templatesFS.ReadDir(tmplFolder)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}

	for _, e := range entries {
		data, err := templatesFS.ReadFile(filepath.Join(tmplFolder, e.Name()))
		if err != nil {
			return nil, err
		}

		rendered, err := r.applyTemplate(rc, data)
		if err != nil {
			return nil, err
		}
		files = append(files, rendered)
	}

	return files, nil
}

// applyTemplate applies the current template to the specified render context.
func (r *Renderer) applyTemplate(rc any, iriTemplate []byte) (string, error) {
	funcs := ctrlcommon.GetTemplateFuncMap()
	tmpl, err := template.New("internalreleaseimage").Funcs(funcs).Parse(string(iriTemplate))
	if err != nil {
		return "", fmt.Errorf("failed to parse template : %w", err)
	}

	buf := new(bytes.Buffer)
	if err := tmpl.Execute(buf, rc); err != nil {
		return "", fmt.Errorf("failed to execute template: %w", err)
	}

	return buf.String(), nil
}

package internalreleaseimage

import (
	"bytes"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/clarketm/json"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"

	configv1 "github.com/openshift/api/config/v1"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
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

	// Suffix of the name for the InternalReleaseImage machine configs.
	machineConfigNameSuffix = "-internalreleaseimage"
	// Format of the name for the InternalReleaseImage machine configs.
	machineConfigNameFmt = "02-%s" + machineConfigNameSuffix
)

// The IRI registry is the distribution binary shipped in the docker-registry payload
// image, which openshift/image-registry builds against openshift/docker-distribution
// (a fork of distribution v3). It validates REGISTRY_HTTP_TLS_MINIMUMTLS and
// REGISTRY_HTTP_TLS_CIPHERSUITES against the hardcoded tables mirrored below, and
// returns a fatal error from ListenAndServe on any value it does not recognise --
// with Restart=on-failure in the unit, an unrecognised value crash-loops the registry
// on every master. An unknown cipher name rejects the whole list, not just that entry.
//
// Source: registry/registry.go in openshift/docker-distribution (tlsVersions,
// cipherSuites). Note that distribution v3 dropped the tls1.0 and tls1.1 values that
// v2.8 accepted, and dropped the RC4 and AES_128_CBC_SHA256 suites.
const (
	registryTLSVersion12 = "tls1.2"
	registryTLSVersion13 = "tls1.3"
)

// registryMinTLSVersions is the set of values accepted for the registry's minimum
// TLS version. OpenShift's Old profile asks for TLS 1.0, which is not in this set.
var registryMinTLSVersions = map[string]bool{
	registryTLSVersion12: true,
	registryTLSVersion13: true,
}

// registryCipherSuites is the set of cipher suite names the registry accepts that
// also have an effect at TLS 1.2. The registry additionally accepts the three TLS 1.3
// suite names, but those are deliberately excluded: Go ignores TLS 1.3 suites in
// tls.Config.CipherSuites, so sending them at tls1.2 would contribute nothing while
// making an otherwise-empty list look non-empty.
var registryCipherSuites = map[string]bool{
	"TLS_ECDHE_ECDSA_WITH_AES_128_CBC_SHA":          true,
	"TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256":       true,
	"TLS_ECDHE_ECDSA_WITH_AES_256_CBC_SHA":          true,
	"TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384":       true,
	"TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305_SHA256": true,
	"TLS_ECDHE_RSA_WITH_3DES_EDE_CBC_SHA":           true,
	"TLS_ECDHE_RSA_WITH_AES_128_CBC_SHA":            true,
	"TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256":         true,
	"TLS_ECDHE_RSA_WITH_AES_256_CBC_SHA":            true,
	"TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384":         true,
	"TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305_SHA256":   true,
	"TLS_RSA_WITH_3DES_EDE_CBC_SHA":                 true,
	"TLS_RSA_WITH_AES_128_CBC_SHA":                  true,
	"TLS_RSA_WITH_AES_128_GCM_SHA256":               true,
	"TLS_RSA_WITH_AES_256_CBC_SHA":                  true,
	"TLS_RSA_WITH_AES_256_GCM_SHA384":               true,
}

// Renderer takes care of generating the required ignition (by role) for
// the InternalReleaseImage machine config resources. It can also create
// a MachineConfig instance when required.
type Renderer struct {
	role                         string
	iriSecret                    *corev1.Secret
	iriRegistryCredentialsSecret *corev1.Secret
	cconfig                      *mcfgv1.ControllerConfig
	tlsProfile                   *configv1.TLSSecurityProfile
}

// NewRendererByRole creates a new Renderer instance for generating
// the machine config for the given role.
func NewRendererByRole(role string, iriSecret, iriRegistryCredentialsSecret *corev1.Secret, cconfig *mcfgv1.ControllerConfig, tlsProfile *configv1.TLSSecurityProfile) *Renderer {
	return &Renderer{
		role:                         role,
		iriSecret:                    iriSecret,
		iriRegistryCredentialsSecret: iriRegistryCredentialsSecret,
		cconfig:                      cconfig,
		tlsProfile:                   tlsProfile,
	}
}

// NewSimpleRenderer creates a minimal Renderer that only knows its role.
// Use this when secrets and ControllerConfig are not needed (e.g., rendering
// a disabled service configuration).
func NewSimpleRenderer(role string) *Renderer {
	return &Renderer{role: role}
}

// GetMachineConfigName returns the name of the MachineConfig instance.
func (r *Renderer) GetMachineConfigName() string {
	return fmt.Sprintf(machineConfigNameFmt, r.role)
}

// createEmptyMachineConfig creates an empty MachineConfig (without any ignition configured).
func (r *Renderer) createEmptyMachineConfig() (*mcfgv1.MachineConfig, error) {
	mc, err := ctrlcommon.MachineConfigFromIgnConfig(r.role, r.GetMachineConfigName(), ctrlcommon.NewIgnConfig())
	if err != nil {
		return nil, err
	}

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

	units, err := r.renderTemplateFolder(rc, filepath.Join(r.role, "units"))
	if err != nil {
		return err
	}
	files, err := r.renderTemplateFolder(rc, filepath.Join(r.role, "files"))
	if err != nil {
		return err
	}

	return r.transpileAndSetIgnition(mc, files, units)
}

// renderContext is a type used to hold the configuration required
// for current the template rendering.
type renderContext struct {
	RegistryEnabled     bool
	DockerRegistryImage string
	IriTLSKey           string
	IriTLSCert          string
	RootCA              string
	IriHtpasswd         string
	TLSMinVersion       string
	TLSCipherSuites     []string
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

	// iriRegistryCredentialsSecret is always non-nil here: the IRI controller
	// fetches it and fails loudly if not found (auth is mandatory).
	iriHtpasswd := string(r.iriRegistryCredentialsSecret.Data["htpasswd"])

	tlsMinVersion, tlsCipherSuites, err := registryTLSFromProfile(r.tlsProfile)
	if err != nil {
		return nil, err
	}
	klog.V(4).Infof("IRI registry TLS profile: %s, minimum version: %s, cipher suites: %q",
		tlsProfileName(r.tlsProfile), tlsMinVersion, tlsCipherSuites)

	return &renderContext{
		RegistryEnabled:     true,
		DockerRegistryImage: r.cconfig.Spec.Images[templatectrl.DockerRegistryKey],
		IriTLSKey:           iriTLSKey,
		IriTLSCert:          iriTLSCert,
		RootCA:              string(r.cconfig.Spec.RootCAData),
		IriHtpasswd:         iriHtpasswd,
		TLSMinVersion:       tlsMinVersion,
		TLSCipherSuites:     tlsCipherSuites,
	}, nil
}

// registryTLSFromProfile converts an OpenShift TLSSecurityProfile to the values for
// the registry's REGISTRY_HTTP_TLS_MINIMUMTLS and REGISTRY_HTTP_TLS_CIPHERSUITES
// environment variables.
//
// The registry validates both against its own hardcoded tables and refuses to start
// on any value it does not recognise, so the profile cannot be passed through
// verbatim. Values outside those tables are either clamped (minimum version) or
// dropped (cipher suites); see registryMinTLSVersions and registryCipherSuites.
//
// A nil cipherSuites result means "do not set REGISTRY_HTTP_TLS_CIPHERSUITES", and
// is only ever returned for TLS 1.3, where the suites are fixed by the protocol. An
// empty result is never returned for TLS 1.2: the registry reads an empty list as
// "use my defaults", which is broader than any profile that produced it, so that
// case is an error instead.
func registryTLSFromProfile(profile *configv1.TLSSecurityProfile) (minVersion string, cipherSuites []string, err error) {
	tlsVersion, ciphers := ctrlcommon.GetSecurityProfileCiphers(profile)

	minVersion = openShiftTLSVersionToRegistryVersion(tlsVersion)
	if !registryMinTLSVersions[minVersion] {
		// The registry only accepts tls1.2 and tls1.3. Clamping upward keeps the
		// cluster's only registry serving, and errs towards a stronger configuration
		// than requested rather than a weaker one.
		klog.Warningf("IRI registry does not support a minimum TLS version of %s (TLS profile %s); using %s instead",
			minVersion, tlsProfileName(profile), registryTLSVersion12)
		minVersion = registryTLSVersion12
	}

	// Cipher suites are fixed by the protocol from TLS 1.3 onwards, and the registry
	// ignores REGISTRY_HTTP_TLS_CIPHERSUITES entirely above tls1.2.
	if minVersion != registryTLSVersion12 {
		return minVersion, nil, nil
	}

	for _, c := range ciphers {
		if registryCipherSuites[c] {
			cipherSuites = append(cipherSuites, c)
		}
	}
	if len(cipherSuites) == 0 {
		return "", nil, fmt.Errorf("TLS profile %s specifies no cipher suite usable by the IRI registry at %s (requested: %v)",
			tlsProfileName(profile), minVersion, ciphers)
	}

	return minVersion, cipherSuites, nil
}

// tlsProfileName returns a human-readable name for the profile, for use in messages.
func tlsProfileName(profile *configv1.TLSSecurityProfile) string {
	if profile == nil {
		return "<unset>"
	}
	return string(profile.Type)
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

// RenderDisabledRegistryService renders the iri-registry systemd unit with
// RegistryEnabled=false (producing a no-op ExecStart=/bin/true service) and
// sets the resulting ignition on the specified MachineConfig.
// This method does not require secrets or ControllerConfig.
func (r *Renderer) RenderDisabledRegistryService(mc *mcfgv1.MachineConfig) error {
	rc := &renderContext{
		RegistryEnabled: false,
	}

	units, err := r.renderTemplateFolder(rc, filepath.Join("master", "units"))
	if err != nil {
		return err
	}

	return r.transpileAndSetIgnition(mc, nil, units)
}

// transpileAndSetIgnition transpiles the rendered files and units to an Ignition
// config and sets it on the specified MachineConfig.
func (r *Renderer) transpileAndSetIgnition(mc *mcfgv1.MachineConfig, files, units []string) error {
	ignCfg, err := ctrlcommon.TranspileCoreOSConfigToIgn(files, units)
	if err != nil {
		return fmt.Errorf("error transpiling CoreOS config to Ignition: %w", err)
	}

	rawIgn, err := json.Marshal(ignCfg)
	if err != nil {
		return err
	}

	mc.Spec.Config.Raw = rawIgn
	return nil
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
	// Rendering a []string directly yields "[a b c]", which YAML reads as a single
	// scalar rather than a sequence, so the cipher suite list needs an explicit join.
	// Extend a local copy rather than the shared map, which every MCO template uses.
	funcs["join"] = strings.Join

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

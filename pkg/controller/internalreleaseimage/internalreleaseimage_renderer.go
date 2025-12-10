package internalreleaseimage

import (
	"bytes"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"text/template"

	"github.com/clarketm/json"
	ign3types "github.com/coreos/ignition/v2/config/v3_5/types"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

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
	role      string
	iri       *mcfgv1alpha1.InternalReleaseImage
	iriSecret *corev1.Secret
	cconfig   *mcfgv1.ControllerConfig
}

// NewRendererByRole creates a new Renderer instance for generating
// the machine config for the given role.
func NewRendererByRole(role string, iri *mcfgv1alpha1.InternalReleaseImage, iriSecret *corev1.Secret, cconfig *mcfgv1.ControllerConfig) *Renderer {
	return &Renderer{
		role:      role,
		iri:       iri,
		iriSecret: iriSecret,
		cconfig:   cconfig,
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
	return &renderContext{
		DockerRegistryImage: r.cconfig.Spec.Images[templatectrl.DockerRegistryKey],
		IriTLSKey:           iriTLSKey,
		IriTLSCert:          iriTLSCert,
		RootCA:              string(r.cconfig.Spec.RootCAData),
	}, nil
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

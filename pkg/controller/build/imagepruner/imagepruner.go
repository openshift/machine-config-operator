package imagepruner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"

	"github.com/containers/image/v5/types"
	"github.com/opencontainers/go-digest"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	"github.com/openshift/machine-config-operator/pkg/controller/template"
	"github.com/openshift/machine-config-operator/pkg/secrets"
)

// ImagePruner defines the interface for inspecting and deleting container images.
type ImagePruner interface {
	// InspectImage inspects the given image using the provided secret and ControllerConfig.
	// It returns image inspection information, its digest, or an error.
	InspectImage(context.Context, string, *corev1.Secret, *mcfgv1.ControllerConfig) (*types.ImageInspectInfo, *digest.Digest, error)
	// DeleteImage deletes the given image using the provided secret and ControllerConfig.
	// It returns an error if the deletion fails.
	DeleteImage(context.Context, string, *corev1.Secret, *mcfgv1.ControllerConfig) error
}

// imagePrunerImpl holds the real ImagePruner implementation, utilizing an ImageInspectorDeleter.
type imagePrunerImpl struct {
	images ImageInspectorDeleter
}

// NewImagePruner constructs a new real ImagePruner implementation with a real ImageInspectorDeleter.
func NewImagePruner() ImagePruner {
	return &imagePrunerImpl{
		images: NewImageInspectorDeleter(),
	}
}

// InspectImage inspects the given image using the provided secret. It also accepts a
// ControllerConfig so that certificates may be placed on the filesystem for authentication.
func (i *imagePrunerImpl) InspectImage(ctx context.Context, pullspec string, secret *corev1.Secret, cc *mcfgv1.ControllerConfig) (*types.ImageInspectInfo, *digest.Digest, error) {
	sysCtx, err := i.prepareSystemContext(secret, cc)
	if err != nil {
		return nil, nil, fmt.Errorf("could not prepare for image inspection: %w", err)
	}

	defer func() {
		if err := i.cleanup(sysCtx); err != nil {
			klog.Warningf("Unable to clean up after inspection of %s: %s", pullspec, err)
		}
	}()

	info, digest, err := i.images.ImageInspect(ctx, sysCtx, pullspec)
	if err != nil {
		return nil, nil, fmt.Errorf("could not inspect image: %w", err)
	}

	return info, digest, nil
}

// DeleteImage deletes the given image using the provided secret. It also accepts a
// ControllerConfig so that certificates may be placed on the filesystem for authentication.
func (i *imagePrunerImpl) DeleteImage(ctx context.Context, pullspec string, secret *corev1.Secret, cc *mcfgv1.ControllerConfig) error {
	sysCtx, err := i.prepareSystemContext(secret, cc)
	if err != nil {
		return fmt.Errorf("could not prepare for image deletion: %w", err)
	}

	defer func() {
		if err := i.cleanup(sysCtx); err != nil {
			klog.Warningf("Unable to clean up after deletion of %s: %s", pullspec, err)
		}
	}()

	if err := i.images.DeleteImage(ctx, sysCtx, pullspec); err != nil {
		return fmt.Errorf("could not delete image: %w", err)
	}

	return nil
}

// prepareSystemContext prepares to perform the requested operation by first creating the
// certificate directory and then writing the authfile to the appropriate path.
func (i *imagePrunerImpl) prepareSystemContext(secret *corev1.Secret, cc *mcfgv1.ControllerConfig) (*types.SystemContext, error) {
	// Make a deep copy of the ControllerConfig because the write process mutates
	// the ControllerConfig in-place.
	certsDir, err := i.prepareCerts(cc.DeepCopy())
	if err != nil {
		return nil, fmt.Errorf("could not prepare certs: %w", err)
	}

	authfilePath, err := i.prepareAuthfile(secret)
	if err != nil {
		return nil, fmt.Errorf("could not get authfile path for secret %s: %w", secret.Name, err)
	}

	return &types.SystemContext{
		AuthFilePath:             authfilePath,
		DockerPerHostCertDirPath: certsDir,
	}, nil
}

// cleanup cleans up after an operation by removing the temporary certificates directory
// and the temporary authfile.
func (i *imagePrunerImpl) cleanup(sysCtx *types.SystemContext) error {
	if err := os.RemoveAll(sysCtx.DockerPerHostCertDirPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("could not clean up certs directory %s: %w", sysCtx.DockerPerHostCertDirPath, err)
	}

	if err := os.RemoveAll(sysCtx.AuthFilePath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("could not clean up authfile directory %s: %w", sysCtx.AuthFilePath, err)
	}

	return nil
}

// prepareCerts prepares the certificates by first creating a temporary directory for them
// and then writing the certs from the ControllerConfig to that directory.
func (i *imagePrunerImpl) prepareCerts(cc *mcfgv1.ControllerConfig) (string, error) {
	certsDir, err := os.MkdirTemp("", "imagepruner-certs-dir")
	if err != nil {
		return "", fmt.Errorf("could not create temp dir: %w", err)
	}

	if err := i.writeCerts(certsDir, cc); err != nil {
		return "", fmt.Errorf("could not write certs: %w", err)
	}

	return certsDir, nil
}

// writeCerts extracts the certificates from the ControllerConfig and writes them
// to the appropriate directory, which defaults to /etc/docker/certs.d.
func (i *imagePrunerImpl) writeCerts(certsDir string, cc *mcfgv1.ControllerConfig) error {
	template.UpdateControllerConfigCerts(cc)

	for _, irb := range cc.Spec.ImageRegistryBundleData {
		if err := i.writeCertFromImageRegistryBundle(certsDir, irb); err != nil {
			return fmt.Errorf("could not write image registry bundle from ImageRegistryBundleData: %w", err)
		}
	}

	for _, irb := range cc.Spec.ImageRegistryBundleUserData {
		if err := i.writeCertFromImageRegistryBundle(certsDir, irb); err != nil {
			return fmt.Errorf("could not write image registry bundle from ImageRegistryBundleUserData: %w", err)
		}
	}

	return nil
}

// writeCertFromImageRegistryBundle writes a certificate from an image registry bundle
// to the specified certificates directory, creating necessary subdirectories.
func (i *imagePrunerImpl) writeCertFromImageRegistryBundle(certsDir string, irb mcfgv1.ImageRegistryBundle) error {
	caFile := strings.ReplaceAll(irb.File, "..", ":")

	certDir := filepath.Join(certsDir, caFile)

	if err := os.MkdirAll(certDir, 0o755); err != nil {
		return fmt.Errorf("could not create cert dir %q: %w", certDir, err)
	}

	certFile := filepath.Join(certDir, "ca.crt")

	if err := os.WriteFile(certFile, irb.Data, 0o644); err != nil {
		return fmt.Errorf("could not write cert file %q: %w", certFile, err)
	}

	return nil
}

// prepareAuthfile creates a temporary directory and writes the Docker secret
// (authfile) into a file named "authfile.json" within that directory.
// It returns the path to the created authfile.
func (i *imagePrunerImpl) prepareAuthfile(secret *corev1.Secret) (string, error) {
	authfileDir, err := os.MkdirTemp("", "imagepruner-authfile")
	if err != nil {
		return "", fmt.Errorf("could not create temp dir for authfile: %w", err)
	}

	authfilePath := filepath.Join(authfileDir, "authfile.json")

	is, err := secrets.NewImageRegistrySecret(secret)
	if err != nil {
		return "", fmt.Errorf("could not create an ImageRegistrySecret for '%s/%s': %w", secret.Namespace, secret.Name, err)
	}

	secretBytes, err := is.JSONBytes(corev1.SecretTypeDockerConfigJson)
	if err != nil {
		return "", fmt.Errorf("could not normalize secret '%s/%s' to %s: %w", secret.Namespace, secret.Name, corev1.SecretTypeDockerConfigJson, err)
	}

	if err := os.WriteFile(authfilePath, secretBytes, 0o644); err != nil {
		return "", fmt.Errorf("could not write temp authfile %q for secret %q: %w", authfilePath, secret.Name, err)
	}

	return authfilePath, nil
}

// writeAuthfile ensures that the image registry secret is in the dockerconfigjson format
// and writes it to the specified path.
func (i *imagePrunerImpl) writeAuthfile(secret *corev1.Secret, authfilePath string) error {
	is, err := secrets.NewImageRegistrySecret(secret)
	if err != nil {
		return fmt.Errorf("could not create an ImageRegistrySecret for '%s/%s': %w", secret.Namespace, secret.Name, err)
	}

	secretBytes, err := is.JSONBytes(corev1.SecretTypeDockerConfigJson)
	if err != nil {
		return fmt.Errorf("could not normalize secret '%s/%s' to %s: %w", secret.Namespace, secret.Name, corev1.SecretTypeDockerConfigJson, err)
	}

	if err := os.WriteFile(authfilePath, secretBytes, 0o644); err != nil {
		return fmt.Errorf("could not write temp authfile %q for secret %q: %w", authfilePath, secret.Name, err)
	}

	return nil
}

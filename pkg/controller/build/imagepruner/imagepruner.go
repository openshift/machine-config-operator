package imagepruner

import (
	"context"
	"fmt"

	"github.com/openshift/machine-config-operator/pkg/imageutils"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"

	"github.com/containers/image/v5/types"
	"github.com/opencontainers/go-digest"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
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
	sysCtx, err := imageutils.NewSysContextFromControllerConfig(secret, cc)
	if err != nil {
		return nil, nil, fmt.Errorf("could not prepare for image inspection: %w", err)
	}

	defer func() {
		if err := sysCtx.Cleanup(); err != nil {
			klog.Warningf("Unable to clean up after inspection of %s: %s", pullspec, err)
		}
	}()

	info, digest, err := i.images.ImageInspect(ctx, sysCtx.SysContext, pullspec)
	if err != nil {
		return nil, nil, fmt.Errorf("could not inspect image: %w", err)
	}

	return info, digest, nil
}

// DeleteImage deletes the given image using the provided secret. It also accepts a
// ControllerConfig so that certificates may be placed on the filesystem for authentication.
func (i *imagePrunerImpl) DeleteImage(ctx context.Context, pullspec string, secret *corev1.Secret, cc *mcfgv1.ControllerConfig) error {
	sysCtx, err := imageutils.NewSysContextFromControllerConfig(secret, cc)
	if err != nil {
		return fmt.Errorf("could not prepare for image deletion: %w", err)
	}

	defer func() {
		if err := sysCtx.Cleanup(); err != nil {
			klog.Warningf("Unable to clean up after deletion of %s: %s", pullspec, err)
		}
	}()

	if err := i.images.DeleteImage(ctx, sysCtx.SysContext, pullspec); err != nil {
		return fmt.Errorf("could not delete image: %w", err)
	}

	return nil
}

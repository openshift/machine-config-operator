package webhook

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/klog/v2"
	"k8s.io/kubectl/pkg/scheme"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	machinecfgv1 "github.com/openshift/api/machineconfiguration/v1"
	mcfglistersv1alpha1 "github.com/openshift/client-go/machineconfiguration/listers/machineconfiguration/v1alpha1"
)

var _ admission.CustomValidator = (*machineConfigPoolValidatorHandler)(nil)

type admissionConfig struct {
	imageSetLister mcfglistersv1alpha1.PinnedImageSetLister
}

type admissionHandler struct {
	*admissionConfig
	decoder *admission.Decoder
}

// InjectDecoder injects the decoder.
func (a *admissionHandler) InjectDecoder(d *admission.Decoder) error {
	a.decoder = d
	return nil
}

// implements type Handler interface.
// https://godoc.org/github.com/kubernetes-sigs/controller-runtime/pkg/webhook/admission#Handler
type machineConfigPoolValidatorHandler struct {
	*admissionHandler
}

// NewMachineConfigPoolValidator returns a new machineConfigPoolValidatorHandler.
func NewMachineConfigPoolValidator(imageSetLister mcfglistersv1alpha1.PinnedImageSetLister) (*admission.Webhook, error) {
	return createMachineConfigPoolValidator(imageSetLister), nil
}

func createMachineConfigPoolValidator(imageSetLister mcfglistersv1alpha1.PinnedImageSetLister) *admission.Webhook {
	admissionConfig := &admissionConfig{
		imageSetLister: imageSetLister,
	}

	return admission.WithCustomValidator(scheme.Scheme, &machinecfgv1.MachineConfigPool{}, &machineConfigPoolValidatorHandler{
		admissionHandler: &admissionHandler{
			admissionConfig: admissionConfig,
		},
	})
}

func (h *machineConfigPoolValidatorHandler) validateMachineConfigPool(m, oldM *machinecfgv1.MachineConfigPool) (bool, []string, field.ErrorList) {
	var warnings []string
	err := validatePinnedImageSets(h.admissionConfig.imageSetLister, m, oldM)
	if len(err) > 0 {
		return false, warnings, err
	}
	return true, warnings, nil
}

func validatePinnedImageSets(imageSetLister mcfglistersv1alpha1.PinnedImageSetLister, m, _ *machinecfgv1.MachineConfigPool) field.ErrorList {
	var errs field.ErrorList
	for _, imageSet := range m.Spec.PinnedImageSets {
		_, err := imageSetLister.Get(m.Name)
		if apierrors.IsNotFound(err) {
			errs = append(errs, field.Invalid(field.NewPath("spec", "pinnedImageSets"), imageSet.Name, fmt.Sprintf("pinnedImageSet not found: %s", imageSet.Name)))
		} else if err != nil {
			errs = append(errs, field.InternalError(field.NewPath("spec", "pinnedImageSets"), err))
		}
	}

	return errs
}

// Handle handles HTTP requests for admission webhook servers.
func (h *machineConfigPoolValidatorHandler) ValidateCreate(_ context.Context, obj runtime.Object) (admission.Warnings, error) {
	mcp, ok := obj.(*machinecfgv1.MachineConfigPool)
	if !ok {
		return nil, apierrors.NewBadRequest(fmt.Sprintf("expected a MachineConfigPool but got a %T", obj))
	}

	ok, warnings, errs := h.validateMachineConfigPool(mcp, nil)
	if !ok {
		return warnings, errs.ToAggregate()
	}

	return warnings, nil
}

// Handle handles HTTP requests for admission webhook servers.
func (h *machineConfigPoolValidatorHandler) ValidateUpdate(_ context.Context, oldObj, obj runtime.Object) (admission.Warnings, error) {
	mcp, ok := obj.(*machinecfgv1.MachineConfigPool)
	if !ok {
		klog.Infof("ValidateUpdate called, not a MachineConfigPool")
		return nil, apierrors.NewBadRequest(fmt.Sprintf("expected a MachineConfigPool but got a %T", obj))
	}

	mcpOld, ok := oldObj.(*machinecfgv1.MachineConfigPool)
	if !ok {
		return nil, apierrors.NewBadRequest(fmt.Sprintf("expected a MachineConfigPool but got a %T", oldObj))
	}
	if equality.Semantic.DeepEqual(oldObj, obj) {
		return nil, nil
	}

	klog.V(4).Infof("Validate webhook called for MachineConfigPool: %s", mcp.GetName())

	ok, warnings, errs := h.validateMachineConfigPool(mcp, mcpOld)
	if !ok {
		klog.Infof("ValidateUpdate called, not ok: %v", warnings)
		return warnings, errs.ToAggregate()
	}

	return warnings, nil
}

// Handle handles HTTP requests for admission webhook servers.
func (h *machineConfigPoolValidatorHandler) ValidateDelete(_ context.Context, obj runtime.Object) (admission.Warnings, error) {
	mcp, ok := obj.(*machinecfgv1.MachineConfigPool)
	if !ok {
		return nil, apierrors.NewBadRequest(fmt.Sprintf("expected a MachineConfigPool but got a %T", obj))
	}

	klog.V(4).Infof("Validate delete webhook called for MachineConfigPool: %s", mcp.GetName())

	ok, warnings, errs := h.validateMachineConfigPool(mcp, nil)
	if !ok {
		return warnings, errs.ToAggregate()
	}

	return warnings, nil
}

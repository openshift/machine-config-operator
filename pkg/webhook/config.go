package webhook

import (
	machinecfgv1 "github.com/openshift/api/machineconfiguration/v1"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
)

const (
	machineConfigPoolValidatingHookPath = "/validate-machineconfiguration-openshift-io-v1-machineconfigpools"

	webhookConfigurationName       = "machine-config-pool"
	defaultWebhookServiceNamespace = "openshift-machine-config-operator"
	defaultWebhookServicePort      = 443
	// Name and port of the webhook service pointing to the machineset-controller container
	defaultWebhookServiceName = "machine-config-controller-webhook"
)

var (
	// webhookFailurePolicy is ignore
	webhookFailurePolicy = admissionregistrationv1.Ignore
	webhookSideEffects   = admissionregistrationv1.SideEffectClassNone
)

// NewMachineConfigPoolValidatingWebhookConfiguration creates a validation webhook configuration with configured MachineConfigPool webhooks
func NewMachineConfigPoolValidatingWebhookConfiguration() *admissionregistrationv1.ValidatingWebhookConfiguration {
	validatingWebhookConfiguration := &admissionregistrationv1.ValidatingWebhookConfiguration{
		ObjectMeta: metav1.ObjectMeta{
			Name: webhookConfigurationName,
			Annotations: map[string]string{
				"service.beta.openshift.io/inject-cabundle": "true",
			},
		},
		Webhooks: []admissionregistrationv1.ValidatingWebhook{
			NewMachineConfigPoolValidatingWebhook(),
		},
	}

	validatingWebhookConfiguration.SetGroupVersionKind(admissionregistrationv1.SchemeGroupVersion.WithKind("ValidatingWebhookConfiguration"))
	return validatingWebhookConfiguration
}

// NewMachineConfigPoolValidatingWebhook returns validating webhooks for machine config pool which ensures the pinned image sets referenced exist.
func NewMachineConfigPoolValidatingWebhook() admissionregistrationv1.ValidatingWebhook {
	serviceReference := admissionregistrationv1.ServiceReference{
		Namespace: defaultWebhookServiceNamespace,
		Name:      defaultWebhookServiceName,
		Path:      ptr.To[string](machineConfigPoolValidatingHookPath),
		Port:      ptr.To[int32](defaultWebhookServicePort),
	}
	return admissionregistrationv1.ValidatingWebhook{
		AdmissionReviewVersions: []string{"v1"},
		Name:                    "validation.machineconfigpool.machineconfiguration.openshift.io",
		FailurePolicy:           &webhookFailurePolicy,
		SideEffects:             &webhookSideEffects,
		ClientConfig: admissionregistrationv1.WebhookClientConfig{
			Service: &serviceReference,
		},
		Rules: []admissionregistrationv1.RuleWithOperations{
			{
				Rule: admissionregistrationv1.Rule{
					APIGroups:   []string{machinecfgv1.GroupName},
					APIVersions: []string{machinecfgv1.SchemeGroupVersion.Version},
					Resources:   []string{"machineconfigpools"},
				},
				Operations: []admissionregistrationv1.OperationType{
					admissionregistrationv1.Create,
					admissionregistrationv1.Update,
				},
			},
		},
	}
}

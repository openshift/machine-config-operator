package imagebuilder

import (
	"context"
	"errors"
	"fmt"

	mcfgv1alpha1 "github.com/openshift/api/machineconfiguration/v1alpha1"
	mcfgclientset "github.com/openshift/client-go/machineconfiguration/clientset/versioned"
	"github.com/openshift/machine-config-operator/pkg/controller/build/buildrequest"
	"github.com/openshift/machine-config-operator/pkg/controller/build/utils"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	clientset "k8s.io/client-go/kubernetes"
)

type baseImageBuilder struct {
	kubeclient clientset.Interface
	mcfgclient mcfgclientset.Interface
	mosb       *mcfgv1alpha1.MachineOSBuild
	mosc       *mcfgv1alpha1.MachineOSConfig
	builder    buildrequest.Builder
}

func newBaseImageBuilder(kubeclient clientset.Interface, mcfgclient mcfgclientset.Interface, mosb *mcfgv1alpha1.MachineOSBuild, mosc *mcfgv1alpha1.MachineOSConfig, builder buildrequest.Builder) *baseImageBuilder {
	b := &baseImageBuilder{
		kubeclient: kubeclient,
		mcfgclient: mcfgclient,
		builder:    builder,
	}

	if mosb != nil {
		b.mosb = mosb.DeepCopy()
	}

	if mosc != nil {
		b.mosc = mosc.DeepCopy()
	}

	return b
}

func (b *baseImageBuilder) succeededConditions() []metav1.Condition {
	return []metav1.Condition{
		{
			Type:    string(mcfgv1alpha1.MachineOSBuildPrepared),
			Status:  metav1.ConditionFalse,
			Reason:  "Prepared",
			Message: "Build Prepared and Pending",
		},
		{
			Type:    string(mcfgv1alpha1.MachineOSBuilding),
			Status:  metav1.ConditionFalse,
			Reason:  "Building",
			Message: "Image Build In Progress",
		},
		{
			Type:    string(mcfgv1alpha1.MachineOSBuildFailed),
			Status:  metav1.ConditionFalse,
			Reason:  "Failed",
			Message: "Build Failed",
		},
		{
			Type:    string(mcfgv1alpha1.MachineOSBuildInterrupted),
			Status:  metav1.ConditionFalse,
			Reason:  "Interrupted",
			Message: "Build Interrupted",
		},
		{
			Type:    string(mcfgv1alpha1.MachineOSBuildSucceeded),
			Status:  metav1.ConditionTrue,
			Reason:  "Ready",
			Message: "Build Ready",
		},
	}
}

func (b *baseImageBuilder) pendingConditions() []metav1.Condition {
	return []metav1.Condition{
		{
			Type:    string(mcfgv1alpha1.MachineOSBuildPrepared),
			Status:  metav1.ConditionTrue,
			Reason:  "Prepared",
			Message: "Build Prepared and Pending",
		},
		{
			Type:    string(mcfgv1alpha1.MachineOSBuilding),
			Status:  metav1.ConditionFalse,
			Reason:  "Building",
			Message: "Image Build In Progress",
		},
		{
			Type:    string(mcfgv1alpha1.MachineOSBuildFailed),
			Status:  metav1.ConditionFalse,
			Reason:  "Failed",
			Message: "Build Failed",
		},
		{
			Type:    string(mcfgv1alpha1.MachineOSBuildInterrupted),
			Status:  metav1.ConditionFalse,
			Reason:  "Interrupted",
			Message: "Build Interrupted",
		},
		{
			Type:    string(mcfgv1alpha1.MachineOSBuildSucceeded),
			Status:  metav1.ConditionFalse,
			Reason:  "Ready",
			Message: "Build Ready",
		},
	}
}

func (b *baseImageBuilder) runningConditions() []metav1.Condition {
	return []metav1.Condition{
		{
			Type:    string(mcfgv1alpha1.MachineOSBuildPrepared),
			Status:  metav1.ConditionFalse,
			Reason:  "Prepared",
			Message: "Build Prepared and Pending",
		},
		{
			Type:    string(mcfgv1alpha1.MachineOSBuilding),
			Status:  metav1.ConditionTrue,
			Reason:  "Building",
			Message: "Image Build In Progress",
		},
		{
			Type:    string(mcfgv1alpha1.MachineOSBuildFailed),
			Status:  metav1.ConditionFalse,
			Reason:  "Failed",
			Message: "Build Failed",
		},
		{
			Type:    string(mcfgv1alpha1.MachineOSBuildInterrupted),
			Status:  metav1.ConditionFalse,
			Reason:  "Interrupted",
			Message: "Build Interrupted",
		},
		{
			Type:    string(mcfgv1alpha1.MachineOSBuildSucceeded),
			Status:  metav1.ConditionFalse,
			Reason:  "Ready",
			Message: "Build Ready",
		},
	}
}

func (b *baseImageBuilder) failedConditions() []metav1.Condition {
	return []metav1.Condition{
		{
			Type:    string(mcfgv1alpha1.MachineOSBuildPrepared),
			Status:  metav1.ConditionFalse,
			Reason:  "Prepared",
			Message: "Build Prepared and Pending",
		},
		{
			Type:    string(mcfgv1alpha1.MachineOSBuilding),
			Status:  metav1.ConditionFalse,
			Reason:  "Building",
			Message: "Image Build In Progress",
		},
		{
			Type:    string(mcfgv1alpha1.MachineOSBuildFailed),
			Status:  metav1.ConditionTrue,
			Reason:  "Failed",
			Message: "Build Failed",
		},
		{
			Type:    string(mcfgv1alpha1.MachineOSBuildInterrupted),
			Status:  metav1.ConditionFalse,
			Reason:  "Interrupted",
			Message: "Build Interrupted",
		},
		{
			Type:    string(mcfgv1alpha1.MachineOSBuildSucceeded),
			Status:  metav1.ConditionFalse,
			Reason:  "Ready",
			Message: "Build Ready",
		},
	}
}

type kubeObject interface {
	metav1.Object
	GroupVersionKind() schema.GroupVersionKind
}

func (b *baseImageBuilder) getMachineOSBuildStatus(ctx context.Context, obj kubeObject, buildStatus mcfgv1alpha1.BuildProgress, conditions []metav1.Condition) (mcfgv1alpha1.MachineOSBuildStatus, error) {
	now := metav1.Now()

	out := mcfgv1alpha1.MachineOSBuildStatus{}

	if buildStatus == mcfgv1alpha1.MachineOSBuildSucceeded {
		pullspec, err := b.getFinalImagePullspec(ctx)
		if err != nil {
			return out, err
		}

		out.FinalImagePushspec = pullspec
		out.BuildEnd = &now
	}

	if buildStatus == mcfgv1alpha1.MachineOSBuildFailed {
		out.BuildEnd = &now
	}

	out.Conditions = conditions
	out.BuilderReference = &mcfgv1alpha1.MachineOSBuilderReference{
		ImageBuilderType: mcfgv1alpha1.PodBuilder,
		// TODO: Should we clear this whenever the build is complete?
		PodImageBuilder: &mcfgv1alpha1.ObjectReference{
			Name:      obj.GetName(),
			Group:     obj.GroupVersionKind().Group,
			Namespace: obj.GetNamespace(),
			Resource:  obj.GetResourceVersion(),
		},
	}

	return out, nil

}

func (b *baseImageBuilder) addMachineOSBuildNameToError(err error) error {
	buildName, buildNameErr := b.getMachineOSBuildName()
	if buildNameErr != nil {
		return errors.Join(err, fmt.Errorf("could not get MachineOSBuild name: %w", buildNameErr))
	}

	return fmt.Errorf("MachineOSBuild %q encountered an error: %w", buildName, err)
}

func (b *baseImageBuilder) clean(ctx context.Context, ib ImageBuilder) error {
	return errors.Join(ib.Stop(ctx), b.getCleaner().Clean(ctx))
}

func (b *baseImageBuilder) getCleaner() Cleaner {
	if b.mosb != nil {
		return NewCleaner(b.kubeclient, b.mcfgclient, b.mosb, b.mosc)
	}

	return NewCleanerFromBuilder(b.kubeclient, b.mcfgclient, b.builder)
}

func (b *baseImageBuilder) getDigestConfigMapName() (string, error) {
	if b.mosb != nil {
		return utils.GetDigestConfigMapName(b.mosb), nil
	}

	mosbName, err := b.builder.MachineOSBuild()
	if err != nil {
		return "", err
	}

	// TODO: De-duplicate this.
	return fmt.Sprintf("digest-%s", mosbName), nil
}

func (b *baseImageBuilder) getFinalImagePullspec(ctx context.Context) (string, error) {
	name, err := b.getDigestConfigMapName()
	if err != nil {
		return "", fmt.Errorf("could not get digest configmab name: %w", err)
	}

	digestConfigMap, err := b.kubeclient.CoreV1().ConfigMaps(ctrlcommon.MCONamespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return "", err
	}

	sha, err := utils.ParseImagePullspec(b.mosc.Spec.BuildInputs.RenderedImagePushspec, digestConfigMap.Data["digest"])
	if err != nil {
		return "", fmt.Errorf("could not create digested image pullspec from the pullspec %q and the digest %q: %w", b.mosc.Status.CurrentImagePullspec, digestConfigMap.Data["digest"], err)
	}

	return sha, nil
}

func (b *baseImageBuilder) getMachineOSBuildName() (string, error) {
	if b.mosb != nil {
		return b.mosb.Name, nil
	}

	return b.builder.MachineOSBuild()
}

func (b *baseImageBuilder) getBuilderName() string {
	if b.mosb != nil {
		return utils.GetBuildPodName(b.mosb)
	}

	return b.builder.GetObject().GetName()
}

func (b *baseImageBuilder) prepareForBuild(ctx context.Context) (buildrequest.Builder, error) {
	preparer := NewPreparer(b.kubeclient, b.mcfgclient, b.mosb, b.mosc)

	br, err := preparer.Prepare(ctx)
	if err != nil {
		return nil, err
	}

	builder := br.Builder()

	_, ok := builder.GetObject().(*corev1.Pod)
	if !ok {
		return nil, fmt.Errorf("builder object %s is not a Pod", builder.GetObject().GetName())
	}

	return builder, nil
}

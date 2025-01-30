package imagebuilder

import (
	"context"
	"errors"
	"fmt"

	mcfgv1alpha1 "github.com/openshift/api/machineconfiguration/v1alpha1"
	mcfgclientset "github.com/openshift/client-go/machineconfiguration/clientset/versioned"
	tektonclientset "github.com/tektoncd/pipeline/pkg/client/clientset/versioned"
	"github.com/openshift/machine-config-operator/pkg/controller/build/buildrequest"
	"github.com/openshift/machine-config-operator/pkg/controller/build/utils"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	clientset "k8s.io/client-go/kubernetes"
)

// Holds the common objects and methods needed to implement an ImageBuilder.
type baseImageBuilder struct {
	kubeclient   clientset.Interface
	mcfgclient   mcfgclientset.Interface
	tektonclient tektonclientset.Interface
	mosb         *mcfgv1alpha1.MachineOSBuild
	mosc         *mcfgv1alpha1.MachineOSConfig
	builder      buildrequest.Builder
	buildrequest buildrequest.BuildRequest
}

// Constructs a baseImageBuilder, deep-copying objects as needed.
func newBaseImageBuilder(kubeclient clientset.Interface, mcfgclient mcfgclientset.Interface, tektonclient tektonclientset.Interface, mosb *mcfgv1alpha1.MachineOSBuild, mosc *mcfgv1alpha1.MachineOSConfig, builder buildrequest.Builder) *baseImageBuilder {
	b := &baseImageBuilder{
		kubeclient: kubeclient,
		mcfgclient: mcfgclient,
		tektonclient: tektonclient,
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

// Constructs a baseImageBuilder and also instantiates a Cleaner instance based upon the object state.
func newBaseImageBuilderWithCleaner(kubeclient clientset.Interface, mcfgclient mcfgclientset.Interface, tektonclient tektonclientset.Interface, mosb *mcfgv1alpha1.MachineOSBuild, mosc *mcfgv1alpha1.MachineOSConfig, builder buildrequest.Builder) (*baseImageBuilder, Cleaner) {
	b := newBaseImageBuilder(kubeclient, mcfgclient, tektonclient, mosb, mosc, builder)
	return b, &cleanerImpl{
		baseImageBuilder: b,
	}
}

// Represents the successful conditions for a MachineOSBuild.
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

// Represents the pending conditions for a MachineOSBuild.
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

// Represents the running conditions for a MachineOSBuild.
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

// Represents the failure conditions for a MachineOSBuild.
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

// Represents the interrupted conditions for a MachineOSBuild.
func (b *baseImageBuilder) interruptedConditions() []metav1.Condition {
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
			Status:  metav1.ConditionTrue,
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

// Represents the initial MachineOSBuild state (all conditions false).
func (b *baseImageBuilder) initialConditions() []metav1.Condition {
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
			Status:  metav1.ConditionFalse,
			Reason:  "Ready",
			Message: "Build Ready",
		},
	}
}

// Represents a builder object that has a GroupVersionKind method on it; which
// anything that has metav1.TypeMeta instance included should have..
type kubeObject interface {
	metav1.Object
	GroupVersionKind() schema.GroupVersionKind
}

// Computes the MachineOSBuild status given the build status as well as the
// conditions. Also fetches the final image pullspec from the digestfile
// ConfigMap.
func (b *baseImageBuilder) getMachineOSBuildStatus(ctx context.Context, obj kubeObject, buildStatus mcfgv1alpha1.BuildProgress, conditions []metav1.Condition) (mcfgv1alpha1.MachineOSBuildStatus, error) {
	now := metav1.Now()

	out := mcfgv1alpha1.MachineOSBuildStatus{}

	out.BuildStart = &now

	if buildStatus == mcfgv1alpha1.MachineOSBuildSucceeded || buildStatus == mcfgv1alpha1.MachineOSBuildFailed || buildStatus == mcfgv1alpha1.MachineOSBuildInterrupted {
		out.BuildEnd = &now
	}

	if buildStatus == mcfgv1alpha1.MachineOSBuildSucceeded {
		pullspec, err := b.getFinalImagePullspec(ctx)
		if err != nil {
			return out, err
		}

		out.FinalImagePushspec = pullspec
	}

	out.Conditions = conditions
	out.BuilderReference = &mcfgv1alpha1.MachineOSBuilderReference{
		ImageBuilderType: b.mosc.Spec.BuildInputs.ImageBuilder.ImageBuilderType,
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

// Attaches the MachineOSBuild name onto an error, if possible.
func (b *baseImageBuilder) addMachineOSBuildNameToError(err error) error {
	buildName, buildNameErr := b.getMachineOSBuildName()
	if buildNameErr != nil {
		return errors.Join(err, fmt.Errorf("could not get MachineOSBuild name: %w", buildNameErr))
	}

	return fmt.Errorf("imagebuilder for MachineOSBuild %q encountered an error: %w", buildName, err)
}

// Gets the digestfile ConfigMap name either directly from the MachineOSBuild
// or by getting the MachineOSBuild name from the Builder and computing it.
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

// Gets the final image pullspec from the digestfile ConfigMap.
func (b *baseImageBuilder) getFinalImagePullspec(ctx context.Context) (string, error) {
	name, err := b.getDigestConfigMapName()
	if err != nil {
		return "", fmt.Errorf("could not get digest configmap name: %w", err)
	}

	digestConfigMap, err := b.kubeclient.CoreV1().ConfigMaps(ctrlcommon.MCONamespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("could not get final image digest configmap %q: %w", name, err)
	}

	sha, err := utils.ParseImagePullspec(b.mosc.Spec.BuildInputs.RenderedImagePushspec, digestConfigMap.Data["digest"])
	if err != nil {
		return "", fmt.Errorf("could not create digested image pullspec from the pullspec %q and the digest %q: %w", b.mosc.Status.CurrentImagePullspec, digestConfigMap.Data["digest"], err)
	}

	return sha, nil
}

// Gets the name of the MachineOSBuild name either directly from the
// MachineOSBuild or from the Builder object.
func (b *baseImageBuilder) getMachineOSBuildName() (string, error) {
	if b.mosb != nil {
		return b.mosb.Name, nil
	}

	return b.builder.MachineOSBuild()
}

// Gets the name of the MachineOSConfig name either directly from the
// MachineOSConfig or from the Builder object.
func (b *baseImageBuilder) getMachineOSConfigName() (string, error) {
	if b.mosc != nil {
		return b.mosc.Name, nil
	}

	return b.builder.MachineOSBuild()
}

// Gets the name of the builder execution unit by
// either looking for the MachineOSBuild name and computing it or by getting it
// directly from the Builder object.
func (b *baseImageBuilder) getBuilderName() string {
	if b.mosb != nil {
		return utils.GetBuildName(b.mosb)
	}

	return b.builder.GetObject().GetName()
}

// Prepares to run a given build by instantiating and running the preparer. It
// then returns a Builder object.
func (b *baseImageBuilder) prepareForBuild(ctx context.Context) (buildrequest.Builder, error) {
	preparer := NewPreparer(b.kubeclient, b.mcfgclient, b.mosb, b.mosc)

	br, err := preparer.Prepare(ctx)
	if err != nil {
		return nil, fmt.Errorf("could not prepare for MachineOSBuild %q: %w", b.mosb.Name, err)
	}

	b.buildrequest = br
	return br.Builder(b.kubeclient)
}

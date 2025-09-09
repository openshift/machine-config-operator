package imagebuilder

import (
	"context"
	"errors"
	"fmt"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	mcfgclientset "github.com/openshift/client-go/machineconfiguration/clientset/versioned"
	"github.com/openshift/machine-config-operator/pkg/controller/build/buildrequest"
	"github.com/openshift/machine-config-operator/pkg/controller/build/constants"
	"github.com/openshift/machine-config-operator/pkg/controller/build/utils"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	clientset "k8s.io/client-go/kubernetes"
)

// Holds the common objects and methods needed to implement an ImageBuilder.
type baseImageBuilder struct {
	kubeclient   clientset.Interface
	mcfgclient   mcfgclientset.Interface
	mosb         *mcfgv1.MachineOSBuild
	mosc         *mcfgv1.MachineOSConfig
	builder      buildrequest.Builder
	buildrequest buildrequest.BuildRequest
}

// Constructs a baseImageBuilder, deep-copying objects as needed.
func newBaseImageBuilder(kubeclient clientset.Interface, mcfgclient mcfgclientset.Interface, mosb *mcfgv1.MachineOSBuild, mosc *mcfgv1.MachineOSConfig, builder buildrequest.Builder) *baseImageBuilder {
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

// Constructs a baseImageBuilder and also instantiates a Cleaner instance based upon the object state.
func newBaseImageBuilderWithCleaner(kubeclient clientset.Interface, mcfgclient mcfgclientset.Interface, mosb *mcfgv1.MachineOSBuild, mosc *mcfgv1.MachineOSConfig, builder buildrequest.Builder) (*baseImageBuilder, Cleaner) {
	b := newBaseImageBuilder(kubeclient, mcfgclient, mosb, mosc, builder)
	return b, &cleanerImpl{
		baseImageBuilder: b,
	}
}

// Represents a builder object that has a GroupVersionKind method on it; which
// anything that has metav1.TypeMeta instance included should have..
type kubeObject interface {
	metav1.Object
	GroupVersionKind() schema.GroupVersionKind
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

	return b.builder.MachineOSConfig()
}

// Gets the UID of the builder by either checking the MOSB annotation or
// getting it directly from the Builder object.
func (b *baseImageBuilder) getBuilderUID() (string, error) {
	if b.mosb != nil {
		return b.mosb.GetAnnotations()[constants.BuildTypeUIDAnnotationKey], nil
	}

	return b.builder.BuilderUID()
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

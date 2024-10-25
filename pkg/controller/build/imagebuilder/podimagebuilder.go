package imagebuilder

import (
	"context"
	"errors"
	"fmt"

	mcfgv1alpha1 "github.com/openshift/api/machineconfiguration/v1alpha1"

	corev1 "k8s.io/api/core/v1"

	mcfgclientset "github.com/openshift/client-go/machineconfiguration/clientset/versioned"
	"github.com/openshift/machine-config-operator/pkg/controller/build/buildrequest"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"
)

// Implements ImageBuilder assuming that the underlying Builder is a Pod.
type podImageBuilder struct {
	*baseImageBuilder
	cleaner Cleaner
}

func newPodImageBuilder(kubeclient clientset.Interface, mcfgclient mcfgclientset.Interface, mosb *mcfgv1alpha1.MachineOSBuild, mosc *mcfgv1alpha1.MachineOSConfig, builder buildrequest.Builder) *podImageBuilder {
	b, c := newBaseImageBuilderWithCleaner(kubeclient, mcfgclient, mosb, mosc, builder)
	return &podImageBuilder{
		baseImageBuilder: b,
		cleaner:          c,
	}
}

// Instantiates a PodImageBuilder using the MachineOSBuild and MachineOSConfig objects.
func NewPodImageBuilder(kubeclient clientset.Interface, mcfgclient mcfgclientset.Interface, mosb *mcfgv1alpha1.MachineOSBuild, mosc *mcfgv1alpha1.MachineOSConfig) ImageBuilder {
	return newPodImageBuilder(kubeclient, mcfgclient, mosb, mosc, nil)
}

// Instantiates an ImageBuildObserver using the MachineOSBuild and MachineOSConfig objects.
func NewPodImageBuildObserver(kubeclient clientset.Interface, mcfgclient mcfgclientset.Interface, mosb *mcfgv1alpha1.MachineOSBuild, mosc *mcfgv1alpha1.MachineOSConfig) ImageBuildObserver {
	return newPodImageBuilder(kubeclient, mcfgclient, mosb, mosc, nil)
}

// Instantiates a Cleaner using only the MachineOSBuild object.
func NewPodImageBuildCleaner(kubeclient clientset.Interface, mcfgclient mcfgclientset.Interface, mosb *mcfgv1alpha1.MachineOSBuild) Cleaner {
	return newPodImageBuilder(kubeclient, mcfgclient, mosb, nil, nil)
}

// Instantiates a Cleaner using only the Builder object.
func NewPodImageBuildCleanerFromBuilder(kubeclient clientset.Interface, mcfgclient mcfgclientset.Interface, builder buildrequest.Builder) Cleaner {
	return newPodImageBuilder(kubeclient, mcfgclient, nil, nil, builder)
}

// Runs the preparer and creates the build pod.
func (p *podImageBuilder) Start(ctx context.Context) error {
	if _, err := p.start(ctx); err != nil {
		return p.addMachineOSBuildNameToError(fmt.Errorf("could not start pod: %w", err))
	}

	return nil
}

func (p *podImageBuilder) start(ctx context.Context) (*corev1.Pod, error) {
	builder, err := p.prepareForBuild(ctx)
	if err != nil {
		return nil, err
	}

	mosbName, err := p.getMachineOSBuildName()
	if err != nil {
		return nil, err
	}

	buildPod := builder.GetObject().(*corev1.Pod)

	bp, err := p.kubeclient.CoreV1().Pods(ctrlcommon.MCONamespace).Create(ctx, buildPod, metav1.CreateOptions{})
	if err == nil {
		klog.Infof("Build pod %q created for MachineOSBuild %q", bp.Name, mosbName)
		return bp, nil
	}

	if k8serrors.IsAlreadyExists(err) {
		return p.getBuildPodStrict(ctx)
	}

	return nil, fmt.Errorf("could not create build pod: %w", err)
}

// Gets the build pod, returning any errors in the process.
func (p *podImageBuilder) getBuildPodStrict(ctx context.Context) (*corev1.Pod, error) {
	// TODO: Use a lister for this instead.
	return p.kubeclient.CoreV1().Pods(ctrlcommon.MCONamespace).Get(ctx, p.getBuilderName(), metav1.GetOptions{})
}

// Gets the build pod but returns nil if it is not found.
func (p *podImageBuilder) getBuildPod(ctx context.Context) (*corev1.Pod, error) {
	pod, err := p.getBuildPodStrict(ctx)
	if err == nil {
		return pod, nil
	}

	if k8serrors.IsNotFound(err) {
		return nil, nil
	}

	return nil, err
}

// Gets the MachineOSBuildStatus for the currently running build.
func (p *podImageBuilder) MachineOSBuildStatus(ctx context.Context) (mcfgv1alpha1.MachineOSBuildStatus, error) {
	status, err := p.machineOSBuildStatus(ctx)
	if err != nil {
		return status, p.addMachineOSBuildNameToError(fmt.Errorf("could not get MachineOSBuildStatus: %w", err))
	}

	return status, nil
}

func (p *podImageBuilder) getStatus(ctx context.Context) (*corev1.Pod, mcfgv1alpha1.BuildProgress, []metav1.Condition, error) {
	pod, err := p.getBuildPodStrict(ctx)
	if err != nil {
		return nil, "", nil, err
	}

	status, conditions, err := p.mapPodStatusToBuildStatus(pod)
	if err != nil {
		return nil, "", nil, fmt.Errorf("could not map build pod status to buildstatus: %w", err)
	}

	klog.Infof("Build pod %q phase %q mapped to MachineOSBuild progress %q", pod.Name, pod.Status.Phase, status)

	return pod, status, conditions, nil
}

func (p *podImageBuilder) machineOSBuildStatus(ctx context.Context) (mcfgv1alpha1.MachineOSBuildStatus, error) {
	pod, status, conditions, err := p.getStatus(ctx)
	if err != nil {
		return mcfgv1alpha1.MachineOSBuildStatus{}, err
	}

	return p.getMachineOSBuildStatus(ctx, pod, status, conditions)
}

// Gets only the build progress field for a currently running build.
func (p *podImageBuilder) Status(ctx context.Context) (mcfgv1alpha1.BuildProgress, error) {
	_, status, _, err := p.getStatus(ctx)
	if err != nil {
		return status, p.addMachineOSBuildNameToError(fmt.Errorf("could not get BuildProgress: %w", err))
	}

	return status, nil
}

// Interrogates the pod object and maps each phase to a MachineOSBuild status.
func (p *podImageBuilder) mapPodStatusToBuildStatus(pod *corev1.Pod) (mcfgv1alpha1.BuildProgress, []metav1.Condition, error) {
	switch pod.Status.Phase {
	case corev1.PodPending:
		return mcfgv1alpha1.MachineOSBuildPrepared, p.pendingConditions(), nil
	case corev1.PodRunning:
		return mcfgv1alpha1.MachineOSBuilding, p.runningConditions(), nil
	case corev1.PodSucceeded:
		return mcfgv1alpha1.MachineOSBuildSucceeded, p.succeededConditions(), nil
	case corev1.PodFailed:
		return mcfgv1alpha1.MachineOSBuildFailed, p.failedConditions(), nil
	}

	return "", []metav1.Condition{}, fmt.Errorf("unknown pod phase %q", pod.Status.Phase)
}

// Stops the running build by deleting the build pod.
func (p *podImageBuilder) Stop(ctx context.Context) error {
	if err := p.stop(ctx); err != nil {
		return p.addMachineOSBuildNameToError(fmt.Errorf("could not stop build pod: %w", err))
	}

	return nil
}

func (p *podImageBuilder) stop(ctx context.Context) error {
	mosbName, err := p.getMachineOSBuildName()
	if err != nil {
		return fmt.Errorf("could stop build pod: %w", err)
	}

	buildPodName := p.getBuilderName()

	err = p.kubeclient.CoreV1().Pods(ctrlcommon.MCONamespace).Delete(ctx, buildPodName, metav1.DeleteOptions{})
	if err == nil {
		klog.Infof("Deleted build pod %s for MachineOSBuild %s", buildPodName, mosbName)
		return nil
	}

	if k8serrors.IsNotFound(err) {
		return nil
	}

	return fmt.Errorf("could not delete build pod %s for MachineOSBuild %s", buildPodName, mosbName)
}

// Stops the running build by calling Stop() and also removes all of the
// ephemeral objects that were created for the build.
func (p *podImageBuilder) Clean(ctx context.Context) error {
	err := errors.Join(p.Stop(ctx), p.cleaner.Clean(ctx))
	if err != nil {
		return fmt.Errorf("could not clean build objects: %w", err)
	}

	return nil
}

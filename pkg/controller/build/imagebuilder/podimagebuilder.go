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

// Instantiates an ImageBuildObserver which infers the MachineOSBuild state
// from the provided builder object (which is a wrapped pod from a a lister /
// informer).
func NewPodImageBuildObserverFromBuilder(kubeclient clientset.Interface, mcfgclient mcfgclientset.Interface, mosb *mcfgv1alpha1.MachineOSBuild, mosc *mcfgv1alpha1.MachineOSConfig, builder buildrequest.Builder) ImageBuildObserver {
	return newPodImageBuilder(kubeclient, mcfgclient, mosb, mosc, builder)
}

// Instantiates a Cleaner using only the MachineOSBuild object.
func NewPodImageBuildCleaner(kubeclient clientset.Interface, mcfgclient mcfgclientset.Interface, mosb *mcfgv1alpha1.MachineOSBuild) Cleaner {
	return newPodImageBuilder(kubeclient, mcfgclient, mosb, nil, nil)
}

// Instantiates a Cleaner using only the Builder object.
func NewPodImageBuildCleanerFromBuilder(kubeclient clientset.Interface, mcfgclient mcfgclientset.Interface, builder buildrequest.Builder) Cleaner {
	return newPodImageBuilder(kubeclient, mcfgclient, nil, nil, builder)
}

// Gets the build pod from the API server and wraps it in the Builder interface
// before returning it.
func (p *podImageBuilder) Get(ctx context.Context) (buildrequest.Builder, error) {
	pod, err := p.getBuildPodStrict(ctx)
	if err != nil {
		return nil, err
	}

	builder, err := buildrequest.NewBuilder(pod)
	if err != nil {
		return nil, err
	}

	return builder, nil
}

// Runs the preparer and creates the build pod.
func (p *podImageBuilder) Start(ctx context.Context) error {
	if _, err := p.start(ctx); err != nil {
		return p.addMachineOSBuildNameToError(fmt.Errorf("could not start build pod: %w", err))
	}

	return nil
}

func (p *podImageBuilder) start(ctx context.Context) (*corev1.Pod, error) {
	builder, err := p.prepareForBuild(ctx)
	if err != nil {
		return nil, err
	}

	if err := p.validateBuilderType(builder); err != nil {
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

	pod, err := p.getBuildPodStrict(ctx)
	if err != nil {
		return nil, fmt.Errorf("could not get build pod for interrogation: %w", err)
	}

	if pod.GetDeletionTimestamp() != nil {
		// If the pod already exists and the deletion timestamp is not nil, that
		// means the preexisting pod is being deleted while a new replacement pod
		// is trying to take its place.
		klog.Infof("Preexisting build pod %q found, which is marked for deletion", pod.Name)
	}

	return nil, err
}

// Gets the build pod, returning any errors in the process.
func (p *podImageBuilder) getBuildPodStrict(ctx context.Context) (*corev1.Pod, error) {
	name := p.getBuilderName()
	if name == "" {
		return nil, fmt.Errorf("imagebuilder missing name for MachineOSBuild or builder")
	}

	// TODO: Use a lister for this instead.
	pod, err := p.kubeclient.CoreV1().Pods(ctrlcommon.MCONamespace).Get(ctx, p.getBuilderName(), metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("could not get build pod %q: %w", name, err)
	}

	return pod, nil
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

// Determines whether the given build pod exists in the API server.
func (p *podImageBuilder) Exists(ctx context.Context) (bool, error) {
	pod, err := p.getBuildPodStrict(ctx)
	if err == nil && pod != nil {
		return true, nil
	}

	if k8serrors.IsNotFound(err) {
		return false, nil
	}

	return false, p.addMachineOSBuildNameToError(fmt.Errorf("could not determine if build pod exists: %w", err))
}

// Gets the MachineOSBuildStatus for the currently running build.
func (p *podImageBuilder) MachineOSBuildStatus(ctx context.Context) (mcfgv1alpha1.MachineOSBuildStatus, error) {
	status, err := p.machineOSBuildStatus(ctx)
	if err != nil {
		return status, p.addMachineOSBuildNameToError(fmt.Errorf("could not get MachineOSBuildStatus: %w", err))
	}

	return status, nil
}

// Gets the build pod from either the provided builder (if present) or the API server.
func (p *podImageBuilder) getBuildPodFromBuilderOrAPI(ctx context.Context) (*corev1.Pod, error) {
	if p.builder != nil {
		klog.V(4).Infof("Using provided build pod")

		if err := p.validateBuilderType(p.builder); err != nil {
			return nil, fmt.Errorf("could not get build pod from builder: %w", err)
		}

		pod := p.builder.GetObject().(*corev1.Pod)
		return pod, nil
	}

	klog.V(4).Infof("Using build pod from API")
	pod, err := p.getBuildPodStrict(ctx)
	if err != nil {
		return nil, fmt.Errorf("could not get build pod from API: %w", err)
	}

	return pod, nil
}

func (p *podImageBuilder) getStatus(ctx context.Context) (*corev1.Pod, mcfgv1alpha1.BuildProgress, []metav1.Condition, error) {
	pod, err := p.getBuildPodFromBuilderOrAPI(ctx)
	if err != nil {
		return nil, "", nil, err
	}

	status, conditions := p.mapPodStatusToBuildStatus(pod)

	klog.V(4).Infof("Build pod %q phase %q mapped to MachineOSBuild progress %q", pod.Name, pod.Status.Phase, status)

	return pod, status, conditions, nil
}

func (p *podImageBuilder) machineOSBuildStatus(ctx context.Context) (mcfgv1alpha1.MachineOSBuildStatus, error) {
	pod, status, conditions, err := p.getStatus(ctx)
	if err != nil {
		return mcfgv1alpha1.MachineOSBuildStatus{}, err
	}

	buildStatus, err := p.getMachineOSBuildStatus(ctx, pod, status, conditions)
	if err != nil {
		return mcfgv1alpha1.MachineOSBuildStatus{}, err
	}

	return buildStatus, nil
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
func (p *podImageBuilder) mapPodStatusToBuildStatus(pod *corev1.Pod) (mcfgv1alpha1.BuildProgress, []metav1.Condition) {
	// If the pod is being deleted and it was not in either a successful or
	// failed state, then the MachineOSBuild should be considered "interrupted".
	if pod.DeletionTimestamp != nil && pod.Status.Phase != corev1.PodSucceeded && pod.Status.Phase != corev1.PodFailed {
		return mcfgv1alpha1.MachineOSBuildInterrupted, p.interruptedConditions()
	}

	switch pod.Status.Phase {
	case corev1.PodPending:
		return mcfgv1alpha1.MachineOSBuildPrepared, p.pendingConditions()
	case corev1.PodRunning:
		return p.getBuildStatusFromRunningPod(pod)
	case corev1.PodSucceeded:
		return mcfgv1alpha1.MachineOSBuildSucceeded, p.succeededConditions()
	case corev1.PodFailed:
		return mcfgv1alpha1.MachineOSBuildFailed, p.failedConditions()
	}

	return "", p.initialConditions()
}

// If a pod container's exit code is not zero (0), it indicates a failure.
func (p *podImageBuilder) getBuildStatusFromRunningPod(pod *corev1.Pod) (mcfgv1alpha1.BuildProgress, []metav1.Condition) {
	for _, containerStatus := range pod.Status.ContainerStatuses {
		if containerStatus.State.Terminated != nil && containerStatus.State.Terminated.ExitCode != 0 {
			return mcfgv1alpha1.MachineOSBuildFailed, p.failedConditions()
		}
	}

	return mcfgv1alpha1.MachineOSBuilding, p.runningConditions()
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
		return fmt.Errorf("could not get MachineOSBuild name: %w", err)
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

// Validates that a builders' concrete type is a pod.
func (p *podImageBuilder) validateBuilderType(builder buildrequest.Builder) error {
	_, ok := builder.GetObject().(*corev1.Pod)
	if ok {
		return nil
	}

	return fmt.Errorf("invalid type %T from builder, expected %T", p.builder, &corev1.Pod{})
}

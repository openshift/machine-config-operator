package imagebuilder

import (
	"context"
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

type podImageBuilder struct {
	*baseImageBuilder
}

func NewPodImageBuilder(kubeclient clientset.Interface, mcfgclient mcfgclientset.Interface, mosb *mcfgv1alpha1.MachineOSBuild, mosc *mcfgv1alpha1.MachineOSConfig) ImageBuilder {
	return &podImageBuilder{
		baseImageBuilder: newBaseImageBuilder(kubeclient, mcfgclient, mosb, mosc, nil),
	}
}

func NewPodImageBuildObserver(kubeclient clientset.Interface, mcfgclient mcfgclientset.Interface, mosb *mcfgv1alpha1.MachineOSBuild, mosc *mcfgv1alpha1.MachineOSConfig) ImageBuildObserver {
	return &podImageBuilder{
		baseImageBuilder: newBaseImageBuilder(kubeclient, mcfgclient, mosb, mosc, nil),
	}
}

func NewPodImageBuildCleaner(kubeclient clientset.Interface, mcfgclient mcfgclientset.Interface, mosb *mcfgv1alpha1.MachineOSBuild) Cleaner {
	return &podImageBuilder{
		baseImageBuilder: newBaseImageBuilder(kubeclient, mcfgclient, mosb, nil, nil),
	}
}

func NewPodImageBuildCleanerFromBuilder(kubeclient clientset.Interface, mcfgclient mcfgclientset.Interface, builder buildrequest.Builder) Cleaner {
	return &podImageBuilder{
		baseImageBuilder: newBaseImageBuilder(kubeclient, mcfgclient, nil, nil, builder),
	}
}

func (p *podImageBuilder) Start(ctx context.Context) error {
	_, err := p.start(ctx)
	return err
}

func (p *podImageBuilder) start(ctx context.Context) (*corev1.Pod, error) {
	builder, err := p.prepareForBuild(ctx)
	if err != nil {
		return nil, err
	}

	buildPod := builder.GetObject().(*corev1.Pod)

	bp, err := p.kubeclient.CoreV1().Pods(ctrlcommon.MCONamespace).Create(ctx, buildPod, metav1.CreateOptions{})
	if err == nil {
		klog.Infof("Build pod %q created for MachineOSBuild %q", bp.Name, p.mosb.Name)
		return bp, nil
	}

	if k8serrors.IsAlreadyExists(err) {
		return p.getBuildPodStrict(ctx)
	}

	return nil, fmt.Errorf("could not create build pod: %w", err)
}

func (p *podImageBuilder) getBuildPodStrict(ctx context.Context) (*corev1.Pod, error) {
	// TODO: Use a lister for this instead.
	return p.kubeclient.CoreV1().Pods(ctrlcommon.MCONamespace).Get(ctx, p.getBuilderName(), metav1.GetOptions{})
}

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

func (p *podImageBuilder) MachineOSBuildStatus(ctx context.Context) (mcfgv1alpha1.MachineOSBuildStatus, error) {
	status, err := p.machineOSBuildStatus(ctx)
	if err != nil {
		return mcfgv1alpha1.MachineOSBuildStatus{}, fmt.Errorf("could not get status for MachineOSBuild %q: %w", p.mosb.Name, err)
	}

	return status, nil
}

func (p *podImageBuilder) machineOSBuildStatus(ctx context.Context) (mcfgv1alpha1.MachineOSBuildStatus, error) {
	pod, err := p.getBuildPodStrict(ctx)
	if err != nil {
		return mcfgv1alpha1.MachineOSBuildStatus{}, err
	}

	buildStatus, conditions, err := p.mapPodStatusToBuildStatus(pod)
	if err != nil {
		return mcfgv1alpha1.MachineOSBuildStatus{}, err
	}

	return p.getMachineOSBuildStatus(ctx, pod, buildStatus, conditions)
}

func (p *podImageBuilder) Status(ctx context.Context) (mcfgv1alpha1.BuildProgress, error) {
	status, err := p.status(ctx)
	if err != nil {
		return "", fmt.Errorf("could not get status for MachineOSBuild %q: %w", p.mosb.Name, nil)
	}

	return status, nil
}

func (p *podImageBuilder) status(ctx context.Context) (mcfgv1alpha1.BuildProgress, error) {
	pod, err := p.getBuildPodStrict(ctx)
	if err != nil {
		return "", err
	}

	status, _, err := p.mapPodStatusToBuildStatus(pod)
	return status, err
}

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

func (p *podImageBuilder) Stop(ctx context.Context) error {
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

func (p *podImageBuilder) Clean(ctx context.Context) error {
	return p.clean(ctx, p)
}

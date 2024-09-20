package imagebuilder

import (
	"context"
	"errors"
	"fmt"

	mcfgv1alpha1 "github.com/openshift/api/machineconfiguration/v1alpha1"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"

	mcfgclientset "github.com/openshift/client-go/machineconfiguration/clientset/versioned"
	"github.com/openshift/machine-config-operator/pkg/controller/build/buildrequest"
	"github.com/openshift/machine-config-operator/pkg/controller/build/constants"
	"github.com/openshift/machine-config-operator/pkg/controller/build/utils"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"
)

type podImageBuilder struct {
	kubeclient clientset.Interface
	mcfgclient mcfgclientset.Interface
	mosb       *mcfgv1alpha1.MachineOSBuild
	mosc       *mcfgv1alpha1.MachineOSConfig
	builder    buildrequest.Builder
}

func NewPodImageBuilder(kubeclient clientset.Interface, mcfgclient mcfgclientset.Interface, mosb *mcfgv1alpha1.MachineOSBuild, mosc *mcfgv1alpha1.MachineOSConfig) ImageBuilder {
	return &podImageBuilder{
		kubeclient: kubeclient,
		mcfgclient: mcfgclient,
		mosb:       mosb.DeepCopy(),
		mosc:       mosc.DeepCopy(),
	}
}

func NewPodImageBuildObserver(kubeclient clientset.Interface, mcfgclient mcfgclientset.Interface, mosb *mcfgv1alpha1.MachineOSBuild, mosc *mcfgv1alpha1.MachineOSConfig) ImageBuildObserver {
	return &podImageBuilder{
		kubeclient: kubeclient,
		mcfgclient: mcfgclient,
		mosb:       mosb.DeepCopy(),
		mosc:       mosc.DeepCopy(),
	}
}

func NewPodImageBuildCleaner(kubeclient clientset.Interface, mcfgclient mcfgclientset.Interface, mosb *mcfgv1alpha1.MachineOSBuild) Cleaner {
	return &podImageBuilder{
		kubeclient: kubeclient,
		mcfgclient: mcfgclient,
		mosb:       mosb.DeepCopy(),
	}
}

func NewPodImageBuildCleanerFromBuilder(kubeclient clientset.Interface, mcfgclient mcfgclientset.Interface, builder buildrequest.Builder) Cleaner {
	return &podImageBuilder{
		kubeclient: kubeclient,
		mcfgclient: mcfgclient,
		builder:    builder,
	}
}

func (p *podImageBuilder) getBuildPodName() string {
	if p.mosb != nil {
		return utils.GetBuildPodName(p.mosb)
	}

	return p.builder.GetObject().GetName()
}

func (p *podImageBuilder) Start(ctx context.Context) error {
	_, err := p.start(ctx)
	return err
}

func (p *podImageBuilder) start(ctx context.Context) (*corev1.Pod, error) {
	preparer := NewPreparer(p.kubeclient, p.mcfgclient, p.mosb, p.mosc)

	br, err := preparer.Prepare(ctx)
	if err != nil {
		return nil, err
	}

	builder := br.Builder()

	buildPod, ok := builder.GetObject().(*corev1.Pod)
	if !ok {
		return nil, fmt.Errorf("builder object %s is not expected type", builder.GetObject().GetName())
	}

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

func getMachineConfigPoolSelectorFromMachineOSConfigOrMachineOSBuild(mosb *mcfgv1alpha1.MachineOSBuild, mosc *mcfgv1alpha1.MachineOSConfig) (*labels.Requirement, error) {
	if mosc != nil {
		return labels.NewRequirement(constants.TargetMachineConfigPoolLabelKey, selection.Equals, []string{mosc.Spec.MachineConfigPool.Name})
	}

	val, err := utils.GetRequiredLabelValueFromObject(mosb, constants.TargetMachineConfigPoolLabelKey)
	if err != nil {
		return nil, err
	}

	return labels.NewRequirement(constants.TargetMachineConfigPoolLabelKey, selection.Equals, []string{val})
}

func (p *podImageBuilder) getBuildPodStrict(ctx context.Context) (*corev1.Pod, error) {
	// TODO: Use a lister for this instead.
	return p.kubeclient.CoreV1().Pods(ctrlcommon.MCONamespace).Get(ctx, p.getBuildPodName(), metav1.GetOptions{})
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
	pod, err := p.getBuildPodStrict(ctx)
	if err != nil {
		return mcfgv1alpha1.MachineOSBuildStatus{}, err
	}

	status, err := p.getMachineOSBuildStatus(ctx, pod)
	if err != nil {
		return mcfgv1alpha1.MachineOSBuildStatus{}, fmt.Errorf("could not get status from %s: %w", pod.Name, err)
	}

	return status, nil
}

func (p *podImageBuilder) getMachineOSBuildStatus(ctx context.Context, pod *corev1.Pod) (mcfgv1alpha1.MachineOSBuildStatus, error) {
	out := mcfgv1alpha1.MachineOSBuildStatus{}

	buildStatus, conditions, err := p.mapPodStatusToBuildStatus(pod)
	if err != nil {
		return out, err
	}

	now := metav1.Now()

	if buildStatus == mcfgv1alpha1.MachineOSBuildSucceeded {
		pullspec, err := p.getFinalImagePullspec(ctx)
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
			Name:      pod.Name,
			Group:     pod.GroupVersionKind().Group,
			Namespace: pod.Namespace,
			Resource:  pod.ResourceVersion,
		},
	}

	return out, nil
}

func (p *podImageBuilder) Status(ctx context.Context) (mcfgv1alpha1.BuildProgress, error) {
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

func (p *podImageBuilder) getMachineOSBuildName() (string, error) {
	if p.mosb != nil {
		return p.mosb.Name, nil
	}

	return p.builder.MachineOSBuild()
}

func (p *podImageBuilder) Stop(ctx context.Context) error {
	mosbName, err := p.getMachineOSBuildName()
	if err != nil {
		return fmt.Errorf("could stop build pod: %w", err)
	}

	buildPodName := p.getBuildPodName()

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
	return errors.Join(p.Stop(ctx), p.getCleaner().Clean(ctx))
}

func (p *podImageBuilder) getCleaner() Cleaner {
	if p.mosb != nil {
		return NewCleaner(p.kubeclient, p.mcfgclient, p.mosb, p.mosc)
	}

	return NewCleanerFromBuilder(p.kubeclient, p.mcfgclient, p.builder)
}

func (p *podImageBuilder) getDigestConfigMapName() (string, error) {
	if p.mosb != nil {
		return utils.GetDigestConfigMapName(p.mosb), nil
	}

	mosbName, err := p.builder.MachineOSBuild()
	if err != nil {
		return "", err
	}

	// TODO: De-duplicate this.
	return fmt.Sprintf("digest-%s", mosbName), nil
}

func (p *podImageBuilder) getFinalImagePullspec(ctx context.Context) (string, error) {
	name, err := p.getDigestConfigMapName()
	if err != nil {
		return "", fmt.Errorf("could not get digest configmap name: %w", err)
	}

	digestConfigMap, err := p.kubeclient.CoreV1().ConfigMaps(ctrlcommon.MCONamespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return "", err
	}

	sha, err := utils.ParseImagePullspec(p.mosc.Spec.BuildInputs.RenderedImagePushspec, digestConfigMap.Data["digest"])
	if err != nil {
		return "", fmt.Errorf("could not create digested image pullspec from the pullspec %q and the digest %q: %w", p.mosc.Status.CurrentImagePullspec, digestConfigMap.Data["digest"], err)
	}

	return sha, nil
}

func (p *podImageBuilder) succeededConditions() []metav1.Condition {
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

func (p *podImageBuilder) pendingConditions() []metav1.Condition {
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

func (p *podImageBuilder) runningConditions() []metav1.Condition {
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

func (p *podImageBuilder) failedConditions() []metav1.Condition {
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

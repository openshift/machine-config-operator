package imagebuilder

import (
	"context"
	"errors"
	"fmt"

	mcfgv1alpha1 "github.com/openshift/api/machineconfiguration/v1alpha1"
	mcfgclientset "github.com/openshift/client-go/machineconfiguration/clientset/versioned"
	"github.com/openshift/machine-config-operator/pkg/controller/build/buildrequest"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"
)

// I AM NOT COMPLETELY IMPLEMENTED YET!!!
type jobImageBuilder struct {
	*baseImageBuilder
	cleaner Cleaner
}

func newJobImageBuilder(kubeclient clientset.Interface, mcfgclient mcfgclientset.Interface, mosb *mcfgv1alpha1.MachineOSBuild, mosc *mcfgv1alpha1.MachineOSConfig, builder buildrequest.Builder) *jobImageBuilder {
	b, c := newBaseImageBuilderWithCleaner(kubeclient, mcfgclient, mosb, mosc, builder)
	return &jobImageBuilder{
		baseImageBuilder: b,
		cleaner:          c,
	}
}

func NewJobImageBuilder(kubeclient clientset.Interface, mcfgclient mcfgclientset.Interface, mosb *mcfgv1alpha1.MachineOSBuild, mosc *mcfgv1alpha1.MachineOSConfig) ImageBuilder {
	return newJobImageBuilder(kubeclient, mcfgclient, mosb, mosc, nil)
}

func NewJobImageBuildObserver(kubeclient clientset.Interface, mcfgclient mcfgclientset.Interface, mosb *mcfgv1alpha1.MachineOSBuild, mosc *mcfgv1alpha1.MachineOSConfig) ImageBuildObserver {
	return newJobImageBuilder(kubeclient, mcfgclient, mosb, mosc, nil)
}

func NewJobImageBuildCleaner(kubeclient clientset.Interface, mcfgclient mcfgclientset.Interface, mosb *mcfgv1alpha1.MachineOSBuild) Cleaner {
	return newJobImageBuilder(kubeclient, mcfgclient, mosb, nil, nil)
}

func NewJobImageBuildCleanerFromBuilder(kubeclient clientset.Interface, mcfgclient mcfgclientset.Interface, builder buildrequest.Builder) Cleaner {
	return newJobImageBuilder(kubeclient, mcfgclient, nil, nil, builder)
}

func (j *jobImageBuilder) Start(ctx context.Context) error {
	if _, err := j.start(ctx); err != nil {
		return j.addMachineOSBuildNameToError(fmt.Errorf("could not start job: %w", err))
	}

	return nil
}

func (j *jobImageBuilder) start(ctx context.Context) (*batchv1.Job, error) {
	builder, err := j.prepareForBuild(ctx)
	if err != nil {
		return nil, err
	}

	buildJob := j.podToJob(builder.GetObject().(*corev1.Pod))

	bp, err := j.kubeclient.BatchV1().Jobs(ctrlcommon.MCONamespace).Create(ctx, buildJob, metav1.CreateOptions{})
	if err == nil {
		klog.Infof("Build job %q created for MachineOSBuild %q", bp.Name, j.mosb.Name)
		return bp, nil
	}

	if k8serrors.IsAlreadyExists(err) {
		return j.getBuildJobStrict(ctx)
	}

	return nil, fmt.Errorf("could not create build pod: %w", err)
}

func (j *jobImageBuilder) podToJob(pod *corev1.Pod) *batchv1.Job {
	return &batchv1.Job{
		ObjectMeta: pod.ObjectMeta,
		Spec: batchv1.JobSpec{
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{},
				Spec:       pod.Spec,
			},
		},
	}
}

func (j *jobImageBuilder) mapJobStatusToBuildStatuc(_ *batchv1.Job) (mcfgv1alpha1.BuildProgress, []metav1.Condition, error) { //nolint:unparam // This is a stub
	return mcfgv1alpha1.MachineOSBuildFailed, j.failedConditions(), nil
}

func (j *jobImageBuilder) getBuildJobStrict(ctx context.Context) (*batchv1.Job, error) {
	// TODO: Use a lister for this instead.
	return j.kubeclient.BatchV1().Jobs(ctrlcommon.MCONamespace).Get(ctx, j.getBuilderName(), metav1.GetOptions{})
}

func (j *jobImageBuilder) Stop(ctx context.Context) error {
	if err := j.stop(ctx); err != nil {
		return j.addMachineOSBuildNameToError(fmt.Errorf("could not stop job: %w", err))
	}

	return nil
}

func (j *jobImageBuilder) stop(ctx context.Context) error {
	mosbName, err := j.getMachineOSBuildName()
	if err != nil {
		return fmt.Errorf("could not stop build job: %w", err)
	}

	buildJobName := j.getBuilderName()

	err = j.kubeclient.BatchV1().Jobs(ctrlcommon.MCONamespace).Delete(ctx, buildJobName, metav1.DeleteOptions{})
	if err == nil {
		klog.Infof("Deleted build job %s for MachineOSBuild %s", buildJobName, mosbName)
		return nil
	}

	if k8serrors.IsNotFound(err) {
		return nil
	}

	return fmt.Errorf("could not delete build job %s for MachineOSBuild %s", buildJobName, mosbName)
}

func (j *jobImageBuilder) Clean(ctx context.Context) error {
	err := errors.Join(j.Stop(ctx), j.cleaner.Clean(ctx))
	if err != nil {
		return j.addMachineOSBuildNameToError(fmt.Errorf("could clean up job: %w", err))
	}

	return nil
}

// Implement Me
func (j *jobImageBuilder) Status(_ context.Context) (mcfgv1alpha1.BuildProgress, error) { //nolint:unparam // This is a stub
	return mcfgv1alpha1.MachineOSBuildSucceeded, nil
}

// Implement Me
func (j *jobImageBuilder) MachineOSBuildStatus(_ context.Context) (mcfgv1alpha1.MachineOSBuildStatus, error) {
	return mcfgv1alpha1.MachineOSBuildStatus{}, nil
}

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
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clientset "k8s.io/client-go/kubernetes"
	tektonclientset "github.com/tektoncd/pipeline/pkg/client/clientset/versioned"
	"k8s.io/klog/v2"
)

// Implements ImageBuilder assuming that the underlying Builder is a Job.
type jobImageBuilder struct {
	*baseImageBuilder
	cleaner Cleaner
}

func newJobImageBuilder(kubeclient clientset.Interface, mcfgclient mcfgclientset.Interface, tektonclient tektonclientset.Interface, mosb *mcfgv1alpha1.MachineOSBuild, mosc *mcfgv1alpha1.MachineOSConfig, builder buildrequest.Builder) *jobImageBuilder {
	b, c := newBaseImageBuilderWithCleaner(kubeclient, mcfgclient, tektonclient, mosb, mosc, builder)
	return &jobImageBuilder{
		baseImageBuilder: b,
		cleaner:          c,
	}
}

// Instantiates a ImageBuildObserver using the MachineOSBuild and MachineOSConfig objects.
func NewJobImageBuilder(kubeclient clientset.Interface, mcfgclient mcfgclientset.Interface, tektonclient tektonclientset.Interface, mosb *mcfgv1alpha1.MachineOSBuild, mosc *mcfgv1alpha1.MachineOSConfig) ImageBuilder {
	return newJobImageBuilder(kubeclient, mcfgclient, tektonclient, mosb, mosc, nil)
}

// Instantiates an ImageBuildObserver using the MachineOSBuild and MachineOSConfig objects.
func NewJobImageBuildObserver(kubeclient clientset.Interface, mcfgclient mcfgclientset.Interface, tektonclient tektonclientset.Interface, mosb *mcfgv1alpha1.MachineOSBuild, mosc *mcfgv1alpha1.MachineOSConfig) ImageBuildObserver {
	return newJobImageBuilder(kubeclient, mcfgclient, tektonclient, mosb, mosc, nil)
}

// Instantiates an ImageBuildObserver which infers the MachineOSBuild state
// from the provided builder object
func NewJobImageBuildObserverFromBuilder(kubeclient clientset.Interface, mcfgclient mcfgclientset.Interface, tektonclient tektonclientset.Interface, mosb *mcfgv1alpha1.MachineOSBuild, mosc *mcfgv1alpha1.MachineOSConfig, builder buildrequest.Builder) ImageBuildObserver {
	return newJobImageBuilder(kubeclient, mcfgclient, tektonclient, mosb, mosc, builder)
}

// Instantiates a Cleaner using only the MachineOSBuild object.
func NewJobImageBuildCleaner(kubeclient clientset.Interface, mcfgclient mcfgclientset.Interface, tektonclient tektonclientset.Interface, mosb *mcfgv1alpha1.MachineOSBuild) Cleaner {
	return newJobImageBuilder(kubeclient, mcfgclient, tektonclient, mosb, nil, nil)
}

// Instantiates a Cleaner using only the Builder object.
func NewJobImageBuildCleanerFromBuilder(kubeclient clientset.Interface, mcfgclient mcfgclientset.Interface, tektonclient tektonclientset.Interface, builder buildrequest.Builder) Cleaner {
	return newJobImageBuilder(kubeclient, mcfgclient, tektonclient, nil, nil, builder)
}

// Gets the build job from the API server and wraps it in the Builder interface
// before returning it.
func (j *jobImageBuilder) Get(ctx context.Context) (buildrequest.Builder, error) {
	job, err := j.getBuildJobStrict(ctx)
	if err != nil {
		return nil, err
	}

	builder, err := buildrequest.NewBuilder(job)
	if err != nil {
		return nil, err
	}

	return builder, nil
}

// Runs the preparer and creates the build job.
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

	if err := j.validateBuilderType(builder); err != nil {
		return nil, err
	}

	mosbName, err := j.getMachineOSBuildName()
	if err != nil {
		return nil, err
	}

	buildJob := builder.GetObject().(*batchv1.Job)

	bj, err := j.kubeclient.BatchV1().Jobs(ctrlcommon.MCONamespace).Create(ctx, buildJob, metav1.CreateOptions{})
	if err == nil {
		klog.Infof("Build job %q created for MachineOSBuild %q", bj.Name, mosbName)
		return bj, nil
	}

	if k8serrors.IsAlreadyExists(err) {
		return j.getBuildJobStrict(ctx)
	}

	return nil, fmt.Errorf("could not create build job: %w", err)
}

// Gets the build job, returning any errors in the process.
func (j *jobImageBuilder) getBuildJobStrict(ctx context.Context) (*batchv1.Job, error) {
	if j.getBuilderName() == "" {
		return nil, fmt.Errorf("imagebuilder missing name for MachineOSBuild or builder")
	}

	return j.kubeclient.BatchV1().Jobs(ctrlcommon.MCONamespace).Get(ctx, j.getBuilderName(), metav1.GetOptions{})
}

// Gets the build job but returns nil if it is not found.
func (j *jobImageBuilder) getBuildJob(ctx context.Context) (*batchv1.Job, error) {
	job, err := j.getBuildJobStrict(ctx)
	if err == nil {
		return job, nil
	}

	if k8serrors.IsNotFound(err) {
		return nil, nil
	}

	return nil, err
}

// Determines whether the given build job exists in the API server.
func (j *jobImageBuilder) Exists(ctx context.Context) (bool, error) {
	job, err := j.getBuildJobStrict(ctx)
	if err == nil && job != nil {
		return true, nil
	}

	if k8serrors.IsNotFound(err) {
		return false, nil
	}

	return false, j.addMachineOSBuildNameToError(fmt.Errorf("could not determine if build pod exists: %w", err))
}

// Gets the MachineOSBuildStatus for the currently running build.
func (j *jobImageBuilder) MachineOSBuildStatus(ctx context.Context) (mcfgv1alpha1.MachineOSBuildStatus, error) {
	status, err := j.machineOSBuildStatus(ctx)
	if err != nil {
		return status, j.addMachineOSBuildNameToError(fmt.Errorf("could not get MachineOSBuildStatus: %w", err))
	}

	return status, nil
}

// Gets the build job from either the provided builder (if present) or the API server.
func (j *jobImageBuilder) getBuildJobFromBuilderOrAPI(ctx context.Context) (*batchv1.Job, error) {
	if j.builder != nil {
		klog.V(4).Infof("Using provided build job")

		if err := j.validateBuilderType(j.builder); err != nil {
			return nil, fmt.Errorf("could not get build job from builder: %w", err)
		}

		job := j.builder.GetObject().(*batchv1.Job)
		return job, nil
	}

	klog.V(4).Infof("Using build job from API")
	job, err := j.getBuildJobStrict(ctx)
	if err != nil {
		return nil, fmt.Errorf("could not get build pod from API: %w", err)
	}

	return job, nil
}

func (j *jobImageBuilder) getStatus(ctx context.Context) (*batchv1.Job, mcfgv1alpha1.BuildProgress, []metav1.Condition, error) {
	job, err := j.getBuildJobFromBuilderOrAPI(ctx)
	if err != nil {
		return nil, "", nil, err
	}

	status, conditions := j.mapJobStatusToBuildStatus(job)

	klog.Infof("Build job %q status %+v mapped to MachineOSBuild progress %q", job.Name, job.Status, status)

	return job, status, conditions, nil
}

func (j *jobImageBuilder) machineOSBuildStatus(ctx context.Context) (mcfgv1alpha1.MachineOSBuildStatus, error) {
	job, status, conditions, err := j.getStatus(ctx)
	if err != nil {
		return mcfgv1alpha1.MachineOSBuildStatus{}, err
	}

	buildStatus, err := j.getMachineOSBuildStatus(ctx, job, status, conditions)
	if err != nil {
		return mcfgv1alpha1.MachineOSBuildStatus{}, err
	}

	return buildStatus, nil
}

// Gets only the build progress field for a currently running build.
func (j *jobImageBuilder) Status(ctx context.Context) (mcfgv1alpha1.BuildProgress, error) {
	_, status, _, err := j.getStatus(ctx)
	if err != nil {
		return status, j.addMachineOSBuildNameToError(fmt.Errorf("could not get BuildProgress: %w", err))
	}

	return status, nil
}

func (j *jobImageBuilder) mapJobStatusToBuildStatus(job *batchv1.Job) (mcfgv1alpha1.BuildProgress, []metav1.Condition) {
	// If the job is being deleted and it was not in either a successful or failed state
	// then the MachineOSBuild should be considered "interrupted"
	if job.DeletionTimestamp != nil && job.Status.Succeeded == 0 && job.Status.Failed == 0 {
		return mcfgv1alpha1.MachineOSBuildInterrupted, j.interruptedConditions()
	}

	if job.Status.Active == 0 && job.Status.Succeeded == 0 && job.Status.Failed == 0 && job.Status.UncountedTerminatedPods == nil {
		return mcfgv1alpha1.MachineOSBuildPrepared, j.pendingConditions()
	}
	// The build job is still running till it succeeds or maxes out it retries on failures
	if job.Status.Active >= 0 && job.Status.Failed >= 0 && job.Status.Failed < 4 && job.Status.Succeeded == 0 {
		return mcfgv1alpha1.MachineOSBuilding, j.runningConditions()
	}
	if job.Status.Succeeded > 0 {
		return mcfgv1alpha1.MachineOSBuildSucceeded, j.succeededConditions()
	}
	// Only return failed if there have been 4 pod failures as the backoffLimit is set to 3
	if job.Status.Failed > 3 {
		return mcfgv1alpha1.MachineOSBuildFailed, j.failedConditions()

	}

	return "", j.initialConditions()
}

// Stops the running build by deleting the build job.
func (j *jobImageBuilder) Stop(ctx context.Context) error {
	if err := j.stop(ctx); err != nil {
		return j.addMachineOSBuildNameToError(fmt.Errorf("could not stop build job: %w", err))
	}

	return nil
}

func (j *jobImageBuilder) stop(ctx context.Context) error {
	mosbName, err := j.getMachineOSBuildName()
	if err != nil {
		return fmt.Errorf("could not stop build job: %w", err)
	}

	buildJobName := j.getBuilderName()

	propagationPolicy := metav1.DeletePropagationForeground
	err = j.kubeclient.BatchV1().Jobs(ctrlcommon.MCONamespace).Delete(ctx, buildJobName, metav1.DeleteOptions{PropagationPolicy: &propagationPolicy})
	if err == nil {
		klog.Infof("Deleted build job %s for MachineOSBuild %s", buildJobName, mosbName)
		return nil
	}

	if k8serrors.IsNotFound(err) {
		return nil
	}

	return fmt.Errorf("could not delete build job %s for MachineOSBuild %s", buildJobName, mosbName)
}

// Stops the running build by calling Stop() and also removes all of the
// ephemeral objects that were created for the build.
func (j *jobImageBuilder) Clean(ctx context.Context) error {
	err := errors.Join(j.Stop(ctx), j.cleaner.Clean(ctx))
	if err != nil {
		return j.addMachineOSBuildNameToError(fmt.Errorf("could not clean up build job objects: %w", err))
	}

	return nil
}

// Validates that a builders' concrete type is a job.
func (j *jobImageBuilder) validateBuilderType(builder buildrequest.Builder) error {
	_, ok := builder.GetObject().(*batchv1.Job)
	if ok {
		return nil
	}

	return fmt.Errorf("invalid type %T from builder, expected %T", j.builder, &batchv1.Job{})
}

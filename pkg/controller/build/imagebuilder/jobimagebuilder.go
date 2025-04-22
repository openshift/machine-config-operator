package imagebuilder

import (
	"context"
	"errors"
	"fmt"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	mcfgclientset "github.com/openshift/client-go/machineconfiguration/clientset/versioned"
	"github.com/openshift/machine-config-operator/pkg/apihelpers"
	"github.com/openshift/machine-config-operator/pkg/controller/build/buildrequest"
	"github.com/openshift/machine-config-operator/pkg/controller/build/constants"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	batchv1 "k8s.io/api/batch/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"
)

// Implements ImageBuilder assuming that the underlying Builder is a Job.
type jobImageBuilder struct {
	*baseImageBuilder
	cleaner Cleaner
}

func newJobImageBuilder(kubeclient clientset.Interface, mcfgclient mcfgclientset.Interface, mosb *mcfgv1.MachineOSBuild, mosc *mcfgv1.MachineOSConfig, builder buildrequest.Builder) *jobImageBuilder {
	b, c := newBaseImageBuilderWithCleaner(kubeclient, mcfgclient, mosb, mosc, builder)
	return &jobImageBuilder{
		baseImageBuilder: b,
		cleaner:          c,
	}
}

// Instantiates a ImageBuildObserver using the MachineOSBuild and MachineOSConfig objects.
func NewJobImageBuilder(kubeclient clientset.Interface, mcfgclient mcfgclientset.Interface, mosb *mcfgv1.MachineOSBuild, mosc *mcfgv1.MachineOSConfig) ImageBuilder {
	return newJobImageBuilder(kubeclient, mcfgclient, mosb, mosc, nil)
}

// Instantiates an ImageBuildObserver using the MachineOSBuild and MachineOSConfig objects.
func NewJobImageBuildObserver(kubeclient clientset.Interface, mcfgclient mcfgclientset.Interface, mosb *mcfgv1.MachineOSBuild, mosc *mcfgv1.MachineOSConfig) ImageBuildObserver {
	return newJobImageBuilder(kubeclient, mcfgclient, mosb, mosc, nil)
}

// Instantiates an ImageBuildObserver which infers the MachineOSBuild state
// from the provided builder object
func NewJobImageBuildObserverFromBuilder(kubeclient clientset.Interface, mcfgclient mcfgclientset.Interface, mosb *mcfgv1.MachineOSBuild, mosc *mcfgv1.MachineOSConfig, builder buildrequest.Builder) ImageBuildObserver {
	return newJobImageBuilder(kubeclient, mcfgclient, mosb, mosc, builder)
}

// Instantiates a Cleaner using only the MachineOSBuild object.
func NewJobImageBuildCleaner(kubeclient clientset.Interface, mcfgclient mcfgclientset.Interface, mosb *mcfgv1.MachineOSBuild) Cleaner {
	return newJobImageBuilder(kubeclient, mcfgclient, mosb, nil, nil)
}

// Instantiates a Cleaner using only the Builder object.
func NewJobImageBuildCleanerFromBuilder(kubeclient clientset.Interface, mcfgclient mcfgclientset.Interface, builder buildrequest.Builder) Cleaner {
	return newJobImageBuilder(kubeclient, mcfgclient, nil, nil, builder)
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

		// Set the job UID as an annotation in the MOSB
		if j.mosb != nil {
			metav1.SetMetaDataAnnotation(&j.mosb.ObjectMeta, constants.JobUIDAnnotationKey, string(bj.UID))
			// Update the MOSB with the new annotations
			_, err := j.mcfgclient.MachineconfigurationV1().MachineOSBuilds().Update(ctx, j.mosb, metav1.UpdateOptions{})
			if err != nil && !k8serrors.IsNotFound(err) {
				return nil, fmt.Errorf("could not update MachineOSBuild %s with job UID annotation: %w", mosbName, err)
			}
		}

		// Set the owner reference of the configmaps and secrets created to be the Job
		// Set blockOwnerDeletion and Controller to false as Job ownership doesn't work when set to true
		oref := metav1.NewControllerRef(bj, batchv1.SchemeGroupVersion.WithKind("Job"))
		falseBool := false
		oref.BlockOwnerDeletion = &falseBool
		oref.Controller = &falseBool

		cms, err := j.buildrequest.ConfigMaps()
		if err != nil {
			return nil, err
		}
		for _, cm := range cms {
			cm.SetOwnerReferences([]metav1.OwnerReference{*oref})
			if _, err := j.kubeclient.CoreV1().ConfigMaps(ctrlcommon.MCONamespace).Update(ctx, cm, metav1.UpdateOptions{}); err != nil {
				return nil, err
			}
		}

		secrets, err := j.buildrequest.Secrets()
		if err != nil {
			return nil, err
		}
		for _, secret := range secrets {
			secret.SetOwnerReferences([]metav1.OwnerReference{*oref})
			if _, err := j.kubeclient.CoreV1().Secrets(ctrlcommon.MCONamespace).Update(ctx, secret, metav1.UpdateOptions{}); err != nil {
				return nil, err
			}
		}
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
func (j *jobImageBuilder) MachineOSBuildStatus(ctx context.Context) (mcfgv1.MachineOSBuildStatus, error) {
	status, err := j.machineOSBuildStatus(ctx)
	if err != nil {
		return status, j.addMachineOSBuildNameToError(fmt.Errorf("could not get MachineOSBuildStatus: %w", err))
	}

	return status, nil
}

// Gets the build job from either the provided builder (if present) or the API server.
func (j *jobImageBuilder) getBuildJobFromBuilderOrAPI(ctx context.Context) (*batchv1.Job, error) {
	if j.builder != nil {
		if err := j.validateBuilderType(j.builder); err != nil {
			return nil, fmt.Errorf("could not get build job from builder: %w", err)
		}

		job := j.builder.GetObject().(*batchv1.Job)

		// Ensure that the job UID matches the jobUID annotation in the MOSB so that
		// we know that we are using the correct job to set the status of the MOSB
		if jobIsForMOSB(job, j.mosb) {
			klog.V(4).Infof("Using provided build job %s", string(job.UID))
			return job, nil
		}
	}

	job, err := j.getBuildJobStrict(ctx)
	if err != nil {
		return nil, fmt.Errorf("could not get build job from API: %w", err)
	}

	klog.V(4).Infof("Using build job from API %s", string(job.UID))
	return job, nil
}

func (j *jobImageBuilder) getStatus(ctx context.Context) (*batchv1.Job, mcfgv1.BuildProgress, []metav1.Condition, error) {
	job, err := j.getBuildJobFromBuilderOrAPI(ctx)
	if err != nil {
		return nil, "", nil, err
	}

	status, conditions := MapJobStatusToBuildStatus(job)

	klog.Infof("Build job %q status %+v mapped to MachineOSBuild progress %q", job.Name, job.Status, status)

	return job, status, conditions, nil
}

func (j *jobImageBuilder) machineOSBuildStatus(ctx context.Context) (mcfgv1.MachineOSBuildStatus, error) {
	job, status, conditions, err := j.getStatus(ctx)
	if err != nil {
		return mcfgv1.MachineOSBuildStatus{}, err
	}

	buildStatus, err := j.getMachineOSBuildStatus(ctx, job, status, conditions)
	if err != nil {
		return mcfgv1.MachineOSBuildStatus{}, err
	}

	return buildStatus, nil
}

// Gets only the build progress field for a currently running build.
func (j *jobImageBuilder) Status(ctx context.Context) (mcfgv1.BuildProgress, error) {
	_, status, _, err := j.getStatus(ctx)
	if err != nil {
		return status, j.addMachineOSBuildNameToError(fmt.Errorf("could not get BuildProgress: %w", err))
	}

	return status, nil
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
		return fmt.Errorf("could not get MachineOSBuild name to stop build job: %w", err)
	}

	buildJob, err := j.getBuildJobFromBuilderOrAPI(ctx)
	if k8serrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("could not get Job to stop build job: %w", err)
	}

	// Ensure that the job being deleted is for the MOSB we are currently reconciling
	if !jobIsForMOSB(buildJob, j.mosb) {
		klog.Infof("Build job %q with UID %s is not owned by MachineOSBuild %q, will not delete", buildJob.Name, buildJob.UID, mosbName)
		return nil
	}
	propagationPolicy := metav1.DeletePropagationForeground
	err = j.kubeclient.BatchV1().Jobs(ctrlcommon.MCONamespace).Delete(ctx, buildJob.Name, metav1.DeleteOptions{PropagationPolicy: &propagationPolicy})
	if err == nil {
		klog.Infof("Deleted build job %s for MachineOSBuild %s", buildJob.Name, mosbName)
		return nil
	}

	if k8serrors.IsNotFound(err) {
		return nil
	}

	return fmt.Errorf("could not delete build job %s for MachineOSBuild %s", buildJob.Name, mosbName)
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

// Maps a given batchv1.Job to a given MachineOSBuild status. Exported so that it can be used in e2e tests.
func MapJobStatusToBuildStatus(job *batchv1.Job) (mcfgv1.BuildProgress, []metav1.Condition) {
	// If the job is being deleted and it was not in either a successful or failed state
	// then the MachineOSBuild should be considered "interrupted"
	if job.DeletionTimestamp != nil && job.Status.Succeeded == 0 && job.Status.Failed < constants.JobMaxRetries+1 {
		return mcfgv1.MachineOSBuildInterrupted, apihelpers.MachineOSBuildInterruptedConditions()
	}

	if job.Status.Active == 0 && job.Status.Succeeded == 0 && job.Status.Failed == 0 && job.Status.UncountedTerminatedPods == nil {
		return mcfgv1.MachineOSBuildPrepared, apihelpers.MachineOSBuildPendingConditions()
	}
	// The build job is still running till it succeeds or maxes out it retries on failures
	if job.Status.Active >= 0 && job.Status.Failed >= 0 && job.Status.Failed < constants.JobMaxRetries+1 && job.Status.Succeeded == 0 {
		return mcfgv1.MachineOSBuilding, apihelpers.MachineOSBuildRunningConditions()
	}
	if job.Status.Succeeded > 0 {
		return mcfgv1.MachineOSBuildSucceeded, apihelpers.MachineOSBuildSucceededConditions()
	}
	// Only return failed if there have been 4 pod failures as the backoffLimit is set to 3
	if job.Status.Failed > constants.JobMaxRetries {
		return mcfgv1.MachineOSBuildFailed, apihelpers.MachineOSBuildFailedConditions()

	}

	return "", apihelpers.MachineOSBuildInitialConditions()
}

// Returns true if the provided job UID matches the job UID annotation in the provided MachineOSBuild
func jobIsForMOSB(job *batchv1.Job, mosb *mcfgv1.MachineOSBuild) bool {
	if mosb == nil {
		return false
	}

	if string(job.UID) != mosb.GetAnnotations()[constants.JobUIDAnnotationKey] {
		return false
	}

	return true
}

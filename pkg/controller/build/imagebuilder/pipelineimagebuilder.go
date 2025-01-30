package imagebuilder

import (
	"context"
	"errors"
	"fmt"

	mcfgv1alpha1 "github.com/openshift/api/machineconfiguration/v1alpha1"
	mcfgclientset "github.com/openshift/client-go/machineconfiguration/clientset/versioned"
	"github.com/openshift/machine-config-operator/pkg/controller/build/buildrequest"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	tektonv1beta1 "github.com/tektoncd/pipeline/pkg/apis/pipeline/v1beta1"
	tektonclientset "github.com/tektoncd/pipeline/pkg/client/clientset/versioned"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"
	"knative.dev/pkg/apis"
)

// Implements ImageBuilder assuming that the underlying Builder is a Pipeline.
type pipelineImageBuilder struct {
	*baseImageBuilder
	cleaner      Cleaner
}

func newPipelineImageBuilder(kubeclient clientset.Interface, mcfgclient mcfgclientset.Interface, tektonclient tektonclientset.Interface, mosb *mcfgv1alpha1.MachineOSBuild, mosc *mcfgv1alpha1.MachineOSConfig, builder buildrequest.Builder) *pipelineImageBuilder {
	b, c := newBaseImageBuilderWithCleaner(kubeclient, mcfgclient, tektonclient, mosb, mosc, builder)
	return &pipelineImageBuilder{
		baseImageBuilder: b,
		cleaner:          c,
	}
}

// Instantiates a ImageBuildObserver using the MachineOSBuild and MachineOSConfig objects.
func NewPipelineImageBuilder(kubeclient clientset.Interface, mcfgclient mcfgclientset.Interface, tektonclient tektonclientset.Interface, mosb *mcfgv1alpha1.MachineOSBuild, mosc *mcfgv1alpha1.MachineOSConfig) ImageBuilder {
	return newPipelineImageBuilder(kubeclient, mcfgclient, tektonclient, mosb, mosc, nil)
}

// Instantiates an ImageBuildObserver using the MachineOSBuild and MachineOSConfig objects.
func NewPipelineImageBuildObserver(kubeclient clientset.Interface, mcfgclient mcfgclientset.Interface, tektonclient tektonclientset.Interface, mosb *mcfgv1alpha1.MachineOSBuild, mosc *mcfgv1alpha1.MachineOSConfig) ImageBuildObserver {
	return newPipelineImageBuilder(kubeclient, mcfgclient, tektonclient, mosb, mosc, nil)
}

// Instantiates an ImageBuildObserver which infers the MachineOSBuild state
// from the provided builder object
func NewPipelineImageBuildObserverFromBuilder(kubeclient clientset.Interface, mcfgclient mcfgclientset.Interface, tektonclient tektonclientset.Interface, mosb *mcfgv1alpha1.MachineOSBuild, mosc *mcfgv1alpha1.MachineOSConfig, builder buildrequest.Builder) ImageBuildObserver {
	return newPipelineImageBuilder(kubeclient, mcfgclient, tektonclient, mosb, mosc, builder)
}

// Instantiates a Cleaner using only the MachineOSBuild object.
func NewPipelineImageBuildCleaner(kubeclient clientset.Interface, mcfgclient mcfgclientset.Interface, tektonclient tektonclientset.Interface, mosb *mcfgv1alpha1.MachineOSBuild) Cleaner {
	return newPipelineImageBuilder(kubeclient, mcfgclient, tektonclient, mosb, nil, nil)
}

// Instantiates a Cleaner using only the Builder object.
func NewPipelineImageBuildCleanerFromBuilder(kubeclient clientset.Interface, mcfgclient mcfgclientset.Interface, tektonclient tektonclientset.Interface, builder buildrequest.Builder) Cleaner {
	return newPipelineImageBuilder(kubeclient, mcfgclient, tektonclient, nil, nil, builder)
}

// Gets the build pipeline from the API server and wraps it in the Builder interface
// before returning it.
func (p *pipelineImageBuilder) Get(ctx context.Context) (buildrequest.Builder, error) {
	pipeline, err := p.getBuildPipelineStrict(ctx)
	if err != nil {
		return nil, err
	}

	builder, err := buildrequest.NewBuilder(pipeline)
	if err != nil {
		return nil, err
	}

	return builder, nil
}

// Runs the preparer and creates the build pipeline.
func (p *pipelineImageBuilder) Start(ctx context.Context) error {
	if _, err := p.start(ctx); err != nil {
		return p.addMachineOSBuildNameToError(fmt.Errorf("could not start pipeline: %w", err))
	}

	return nil
}

func (p *pipelineImageBuilder) start(ctx context.Context) (*tektonv1beta1.PipelineRun, error) {
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

	buildPipelineRun := builder.GetObject().(*tektonv1beta1.PipelineRun)

	bp, err := p.tektonclient.TektonV1beta1().PipelineRuns(ctrlcommon.MCONamespace).Create(ctx, buildPipelineRun, metav1.CreateOptions{})
	if err == nil {
		klog.Infof("Build PipelineRun %q created for MachineOSBuild %q", bp.Name, mosbName)
		return bp, nil
	}

	if k8serrors.IsAlreadyExists(err) {
		return p.getBuildPipelineStrict(ctx)
	}

	return nil, fmt.Errorf("could not create build pipeline: %w", err)
}

// Gets the build job, returning any errors in the process.
func (p *pipelineImageBuilder) getBuildPipelineStrict(ctx context.Context) (*tektonv1beta1.PipelineRun, error) {
	if p.getBuilderName() == "" {
		return nil, fmt.Errorf("imagebuilder missing name for MachineOSBuild or builder")
	}

	return p.tektonclient.TektonV1beta1().PipelineRuns(ctrlcommon.MCONamespace).Get(ctx, p.getBuilderName(), metav1.GetOptions{})
}

// Gets the build job but returns nil if it is not found.
func (p *pipelineImageBuilder) getBuildPipeline(ctx context.Context) (*tektonv1beta1.PipelineRun, error) {
	pipeline, err := p.getBuildPipelineStrict(ctx)
	if err == nil {
		return pipeline, nil
	}

	if k8serrors.IsNotFound(err) {
		return nil, nil
	}

	return nil, err
}

// Determines whether the given build pipelinerun exists in the API server.
func (p *pipelineImageBuilder) Exists(ctx context.Context) (bool, error) {
	pipeline, err := p.getBuildPipelineStrict(ctx)
	if err == nil && pipeline != nil {
		return true, nil
	}

	if k8serrors.IsNotFound(err) {
		return false, nil
	}

	return false, p.addMachineOSBuildNameToError(fmt.Errorf("could not determine if build pipeline exists: %w", err))
}

// Gets the MachineOSBuildStatus for the currently running build.
func (p *pipelineImageBuilder) MachineOSBuildStatus(ctx context.Context) (mcfgv1alpha1.MachineOSBuildStatus, error) {
	status, err := p.machineOSBuildStatus(ctx)
	if err != nil {
		return status, p.addMachineOSBuildNameToError(fmt.Errorf("could not get MachineOSBuildStatus: %w", err))
	}

	return status, nil
}

// Gets the build pipeline from either the provided builder (if present) or the API server.
func (p *pipelineImageBuilder) getBuildPipelineFromBuilderOrAPI(ctx context.Context) (*tektonv1beta1.PipelineRun, error) {
	if p.builder != nil {
		klog.V(4).Infof("Using provided build pipeline run")

		if err := p.validateBuilderType(p.builder); err != nil {
			return nil, fmt.Errorf("could not get build pipeline run from builder: %w", err)
		}

		pipelineRun := p.builder.GetObject().(*tektonv1beta1.PipelineRun)
		return pipelineRun, nil
	}

	klog.V(4).Infof("Using build pipeline run from API")
	pipelineRun, err := p.getBuildPipelineStrict(ctx)
	if err != nil {
		return nil, fmt.Errorf("could not get build pod from API: %w", err)
	}

	return pipelineRun, nil
}

func (p *pipelineImageBuilder) getStatus(ctx context.Context) (*tektonv1beta1.PipelineRun, mcfgv1alpha1.BuildProgress, []metav1.Condition, error) {
	pipelineRun, err := p.getBuildPipelineFromBuilderOrAPI(ctx)
	if err != nil {
		return nil, "", nil, err
	}

	status, conditions := p.mapPipelineStatusToBuildStatus(pipelineRun)

	klog.Infof("Build pipelineRun %q status %+v mapped to MachineOSBuild progress %q", pipelineRun.Name, pipelineRun.Status, status)

	return pipelineRun, status, conditions, nil
}

func (p *pipelineImageBuilder) machineOSBuildStatus(ctx context.Context) (mcfgv1alpha1.MachineOSBuildStatus, error) {
	pipelineRun, status, conditions, err := p.getStatus(ctx)
	if err != nil {
		return mcfgv1alpha1.MachineOSBuildStatus{}, err
	}

	buildStatus, err := p.getMachineOSBuildStatus(ctx, pipelineRun, status, conditions)
	if err != nil {
		return mcfgv1alpha1.MachineOSBuildStatus{}, err
	}

	return buildStatus, nil
}

// Gets only the build progress field for a currently running build.
func (p *pipelineImageBuilder) Status(ctx context.Context) (mcfgv1alpha1.BuildProgress, error) {
	_, status, _, err := p.getStatus(ctx)
	if err != nil {
		return status, p.addMachineOSBuildNameToError(fmt.Errorf("could not get BuildProgress: %w", err))
	}

	return status, nil
}

func (p *pipelineImageBuilder) mapPipelineStatusToBuildStatus(pipelineRun *tektonv1beta1.PipelineRun) (mcfgv1alpha1.BuildProgress, []metav1.Condition) {
	// Check if the PipelineRun is being deleted and not yet succeeded or failed
	if pipelineRun.DeletionTimestamp != nil && !pipelineRun.Status.GetCondition(apis.ConditionSucceeded).IsTrue() && !pipelineRun.Status.GetCondition(apis.ConditionSucceeded).IsFalse() {
		return mcfgv1alpha1.MachineOSBuildInterrupted, p.interruptedConditions()
	}

	if pipelineRun.Status.StartTime == nil && pipelineRun.Status.CompletionTime == nil {
		return mcfgv1alpha1.MachineOSBuildPrepared, p.pendingConditions()
	}
	// The build job is still running till it succeeds or maxes out it retries on failures
	if pipelineRun.HasStarted() {
		return mcfgv1alpha1.MachineOSBuilding, p.runningConditions()
	}
	// Check if the PipelineRun has succeeded
	if pipelineRun.IsDone() {
		return mcfgv1alpha1.MachineOSBuildSucceeded, p.succeededConditions()
	}
	// Check if the PipelineRun has failed
	if pipelineRun.Status.GetCondition(apis.ConditionSucceeded).IsFalse() {
		return mcfgv1alpha1.MachineOSBuildFailed, p.failedConditions()
	}

	return "", p.initialConditions()
}

// Stops the running build by deleting the build job.
func (p *pipelineImageBuilder) Stop(ctx context.Context) error {
	if err := p.stop(ctx); err != nil {
		return p.addMachineOSBuildNameToError(fmt.Errorf("could not stop build pipelineRun: %w", err))
	}

	return nil
}

func (p *pipelineImageBuilder) stop(ctx context.Context) error {
	mosbName, err := p.getMachineOSBuildName()
	if err != nil {
		return fmt.Errorf("could not stop build pipeline: %w", err)
	}

	buildPipelineRunName := p.getBuilderName()

	propagationPolicy := metav1.DeletePropagationForeground
	err = p.tektonclient.TektonV1().PipelineRuns(ctrlcommon.MCONamespace).Delete(ctx, buildPipelineRunName, metav1.DeleteOptions{PropagationPolicy: &propagationPolicy})
	if err == nil {
		klog.Infof("Deleted build pipeline %s for MachineOSBuild %s", buildPipelineRunName, mosbName)
		return nil
	}

	if k8serrors.IsNotFound(err) {
		return nil
	}

	return fmt.Errorf("could not delete build pipeline %s for MachineOSBuild %s", buildPipelineRunName, mosbName)
}

// Stops the running build by calling Stop() and also removes all of the
// ephemeral objects that were created for the build.
func (p *pipelineImageBuilder) Clean(ctx context.Context) error {
	err := errors.Join(p.Stop(ctx), p.cleaner.Clean(ctx))
	if err != nil {
		return p.addMachineOSBuildNameToError(fmt.Errorf("could not clean up build pipeline objects: %w", err))
	}

	return nil
}

// Validates that a builders' concrete type is a pipeline run.
func (p *pipelineImageBuilder) validateBuilderType(builder buildrequest.Builder) error {
	_, ok := builder.GetObject().(*tektonv1beta1.PipelineRun)
	if ok {
		return nil
	}

	return fmt.Errorf("invalid type %T from builder, expected %T", p.builder, &tektonv1beta1.PipelineRun{})
}

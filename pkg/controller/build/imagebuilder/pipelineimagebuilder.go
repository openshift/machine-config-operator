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
	"github.com/openshift/machine-config-operator/pkg/controller/build/utils"
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
	tektonclient tektonclientset.Interface
}

// Gets the final image pullspec.
func (p *pipelineImageBuilder) getFinalImagePullspec(ctx context.Context) (string, error) {
	pipelineRun, err := p.tektonclient.TektonV1beta1().PipelineRuns(ctrlcommon.MCONamespace).Get(ctx, p.getBuilderName(), metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("could not get pipelineRun:", err)
	}
	sha, err := utils.ParseImagePullspec(string(p.mosc.Spec.RenderedImagePushSpec), pipelineRun.Status.PipelineResults[0].Value.StringVal)
	if err != nil {
		return "", fmt.Errorf("could not create digested image pullspec from the pullspec %q and the digest %q: %w", p.mosc.Status.CurrentImagePullSpec, pipelineRun.Status.PipelineResults[0].Value.StringVal, err)
	}
	return sha, nil
}

func newPipelineImageBuilder(kubeclient clientset.Interface, mcfgclient mcfgclientset.Interface, tektonclient tektonclientset.Interface, mosb *mcfgv1.MachineOSBuild, mosc *mcfgv1.MachineOSConfig, builder buildrequest.Builder) *pipelineImageBuilder {
	b, c := newBaseImageBuilderWithCleaner(kubeclient, mcfgclient, mosb, mosc, builder)
	return &pipelineImageBuilder{
		baseImageBuilder: b,
		cleaner:          c,
		tektonclient:     tektonclient,
	}
}

// Instantiates a ImageBuildObserver using the MachineOSBuild and MachineOSConfig objects.
func NewPipelineImageBuilder(kubeclient clientset.Interface, mcfgclient mcfgclientset.Interface, tektonclient tektonclientset.Interface, mosb *mcfgv1.MachineOSBuild, mosc *mcfgv1.MachineOSConfig) ImageBuilder {
	return newPipelineImageBuilder(kubeclient, mcfgclient, tektonclient, mosb, mosc, nil)
}

// Instantiates an ImageBuildObserver using the MachineOSBuild and MachineOSConfig objects.
func NewPipelineImageBuildObserver(kubeclient clientset.Interface, mcfgclient mcfgclientset.Interface, tektonclient tektonclientset.Interface, mosb *mcfgv1.MachineOSBuild, mosc *mcfgv1.MachineOSConfig) ImageBuildObserver {
	return newPipelineImageBuilder(kubeclient, mcfgclient, tektonclient, mosb, mosc, nil)
}

// Instantiates an ImageBuildObserver which infers the MachineOSBuild state
// from the provided builder object
func NewPipelineImageBuildObserverFromBuilder(kubeclient clientset.Interface, mcfgclient mcfgclientset.Interface, tektonclient tektonclientset.Interface, mosb *mcfgv1.MachineOSBuild, mosc *mcfgv1.MachineOSConfig, builder buildrequest.Builder) ImageBuildObserver {
	return newPipelineImageBuilder(kubeclient, mcfgclient, tektonclient, mosb, mosc, builder)
}

// Instantiates a Cleaner using only the MachineOSBuild object.
func NewPipelineImageBuildCleaner(kubeclient clientset.Interface, mcfgclient mcfgclientset.Interface, tektonclient tektonclientset.Interface, mosb *mcfgv1.MachineOSBuild) Cleaner {
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
	if err != nil {
		if k8serrors.IsAlreadyExists(err) {
			return p.getBuildPipelineStrict(ctx)
		}

		return nil, fmt.Errorf("could not create build pipeline: %w", err)
	}

	klog.Infof("Build PipelineRun %q created for MachineOSBuild %q", bp.Name, mosbName)

	// Set the job UID as an annotation in the MOSB
	if p.mosb != nil {
		metav1.SetMetaDataAnnotation(&p.mosb.ObjectMeta, constants.BuildTypeUIDAnnotationKey, string(bp.UID))
		// Update the MOSB with the new annotations
		_, err := p.mcfgclient.MachineconfigurationV1().MachineOSBuilds().Update(ctx, p.mosb, metav1.UpdateOptions{})
		if err != nil && !k8serrors.IsNotFound(err) {
			return nil, fmt.Errorf("could not update MachineOSBuild %s with pipelineRun UID annotation: %w", mosbName, err)
		}
	}

	// Set the owner reference of the configmaps and secrets created to be the Job
	// Set blockOwnerDeletion and Controller to false as Job ownership doesn't work when set to true
	oref := metav1.NewControllerRef(bp, tektonv1beta1.SchemeGroupVersion.WithKind("PipelineRun"))
	falseBool := false
	oref.BlockOwnerDeletion = &falseBool
	oref.Controller = &falseBool

	cms, err := p.buildrequest.ConfigMaps()
	if err != nil {
		return nil, err
	}
	for _, cm := range cms {
		cm.SetOwnerReferences([]metav1.OwnerReference{*oref})
		if _, err := p.kubeclient.CoreV1().ConfigMaps(ctrlcommon.MCONamespace).Update(ctx, cm, metav1.UpdateOptions{}); err != nil {
			return nil, err
		}
	}

	secrets, err := p.buildrequest.Secrets()
	if err != nil {
		return nil, err
	}
	for _, secret := range secrets {
		secret.SetOwnerReferences([]metav1.OwnerReference{*oref})
		if _, err := p.kubeclient.CoreV1().Secrets(ctrlcommon.MCONamespace).Update(ctx, secret, metav1.UpdateOptions{}); err != nil {
			return nil, err
		}
	}
	return bp, nil
}

// Gets the build Pipeline, returning any errors in the process.
func (p *pipelineImageBuilder) getBuildPipelineStrict(ctx context.Context) (*tektonv1beta1.PipelineRun, error) {
	if p.getBuilderName() == "" {
		return nil, fmt.Errorf("imagebuilder missing name for MachineOSBuild or builder")
	}

	return p.tektonclient.TektonV1beta1().PipelineRuns(ctrlcommon.MCONamespace).Get(ctx, p.getBuilderName(), metav1.GetOptions{})
}

// Gets the build pipeline but returns nil if it is not found.
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
func (p *pipelineImageBuilder) MachineOSBuildStatus(ctx context.Context) (mcfgv1.MachineOSBuildStatus, error) {
	status, err := p.machineOSBuildStatus(ctx)
	if err != nil {
		return status, p.addMachineOSBuildNameToError(fmt.Errorf("could not get MachineOSBuildStatus: %w", err))
	}

	return status, nil
}

// Gets the build pipeline from either the provided builder (if present) or the API server.
func (p *pipelineImageBuilder) getBuildPipelineFromBuilderOrAPI(ctx context.Context) (*tektonv1beta1.PipelineRun, error) {
	if p.builder != nil {
		if err := p.validateBuilderType(p.builder); err != nil {
			return nil, fmt.Errorf("could not get build pipeline run from builder: %w", err)
		}

		pipelineRun := p.builder.GetObject().(*tektonv1beta1.PipelineRun)

		// Ensure that the pipelineRun UID matches the pipelineRunUID annotation in the MOSB so that
		// we know that we are using the correct pipelineRun to set the status of the MOSB
		if pipelineRunIsForMOSB(pipelineRun, p.mosb) {
			klog.V(4).Infof("Using provided build pipelineRun %s", string(pipelineRun.UID))
			return pipelineRun, nil
		}
	}

	pipelineRun, err := p.getBuildPipelineStrict(ctx)
	if err != nil {
		return nil, fmt.Errorf("could not get build pipelineRun from API: %w", err)
	}

	klog.V(4).Infof("Using build pipelineRun from API %s", string(pipelineRun.UID))
	return pipelineRun, nil
}

func (p *pipelineImageBuilder) getStatus(ctx context.Context) (*tektonv1beta1.PipelineRun, mcfgv1.BuildProgress, []metav1.Condition, error) {
	pipelineRun, err := p.getBuildPipelineFromBuilderOrAPI(ctx)
	if err != nil {
		return nil, "", nil, err
	}

	status, conditions := MapPipelineRunStatusToBuildStatus(pipelineRun)

	klog.Infof("Build pipelineRun %q status %+v mapped to MachineOSBuild progress %q", pipelineRun.Name, pipelineRun.Status, status)

	return pipelineRun, status, conditions, nil
}

// Computes the MachineOSBuild status given the build status as well as the
// conditions. Also fetches the final image pullspec from the digestfile
// ConfigMap.
func (p *pipelineImageBuilder) getMachineOSBuildStatus(ctx context.Context, obj kubeObject, buildStatus mcfgv1.BuildProgress, conditions []metav1.Condition) (mcfgv1.MachineOSBuildStatus, error) {
	now := metav1.Now()

	out := mcfgv1.MachineOSBuildStatus{}

	out.BuildStart = &now

	if buildStatus == mcfgv1.MachineOSBuildSucceeded || buildStatus == mcfgv1.MachineOSBuildFailed || buildStatus == mcfgv1.MachineOSBuildInterrupted {
		out.BuildEnd = &now
	}

	// In this scenario, the build is in a terminal state, but we don't know
	// when it started since the machine-os-builder pod may have been offline.
	// In this case, we should get the creation timestamp from the builder
	// object and use that as the start time instead of now since the buildEnd
	// must be after the buildStart time.
	if out.BuildStart == &now && out.BuildEnd == &now {
		jobCreationTimestamp := obj.GetCreationTimestamp()
		out.BuildStart = &jobCreationTimestamp
	}

	if buildStatus == mcfgv1.MachineOSBuildSucceeded {
		pullspec, err := p.getFinalImagePullspec(ctx)
		if err != nil {
			return out, err
		}

		out.DigestedImagePushSpec = mcfgv1.ImageDigestFormat(pullspec)
	}

	out.Conditions = conditions
	out.Builder = &mcfgv1.MachineOSBuilderReference{
		ImageBuilderType: mcfgv1.PipelineBuilder,
		// TODO: Should we clear this whenever the build is complete?
		Pipeline: &mcfgv1.ObjectReference{
			Name:      obj.GetName(),
			Group:     tektonv1beta1.SchemeGroupVersion.Group,
			Namespace: obj.GetNamespace(),
			Resource:  "pipelines",
		},
	}
	return out, nil
}

func (p *pipelineImageBuilder) machineOSBuildStatus(ctx context.Context) (mcfgv1.MachineOSBuildStatus, error) {
	pipelineRun, status, conditions, err := p.getStatus(ctx)
	if err != nil {
		return mcfgv1.MachineOSBuildStatus{}, err
	}

	buildStatus, err := p.getMachineOSBuildStatus(ctx, pipelineRun, status, conditions)
	if err != nil {
		return mcfgv1.MachineOSBuildStatus{}, err
	}

	return buildStatus, nil
}

// Gets only the build progress field for a currently running build.
func (p *pipelineImageBuilder) Status(ctx context.Context) (mcfgv1.BuildProgress, error) {
	_, status, _, err := p.getStatus(ctx)
	if err != nil {
		return status, p.addMachineOSBuildNameToError(fmt.Errorf("could not get BuildProgress: %w", err))
	}

	return status, nil
}

// Stops the running build by deleting the build pipelineRun.
func (p *pipelineImageBuilder) Stop(ctx context.Context) error {
	if err := p.stop(ctx); err != nil {
		return p.addMachineOSBuildNameToError(fmt.Errorf("could not stop build pipelineRun: %w", err))
	}

	return nil
}

func (p *pipelineImageBuilder) stop(ctx context.Context) error {
	mosbName, err := p.getMachineOSBuildName()
	if err != nil {
		return fmt.Errorf("could not get MachineOSBuild name to stop build PipelineRun: %w", err)
	}

	buildPipelineRun, err := p.getBuildPipelineFromBuilderOrAPI(ctx)
	if k8serrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("could not get PipelineRun to stop build pipelineRun: %w", err)
	}

	// Ensure that the pipelineRun being deleted is for the MOSB we are currently reconciling
	if !pipelineRunIsForMOSB(buildPipelineRun, p.mosb) {
		klog.Infof("Build pipelineRun %q with UID %s is not owned by MachineOSBuild %q, will not delete", buildPipelineRun.Name, buildPipelineRun.UID, mosbName)
		return nil
	}
	propagationPolicy := metav1.DeletePropagationForeground
	err = p.tektonclient.TektonV1beta1().PipelineRuns(ctrlcommon.MCONamespace).Delete(ctx, buildPipelineRun.Name, metav1.DeleteOptions{PropagationPolicy: &propagationPolicy})
	if err == nil {
		klog.Infof("Deleted build pipeline %s for MachineOSBuild %s", buildPipelineRun.Name, mosbName)
		return nil
	}

	if k8serrors.IsNotFound(err) {
		return nil
	}

	return fmt.Errorf("could not delete build pipelineRun %s for MachineOSBuild %s", buildPipelineRun.Name, mosbName)
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

// Maps a given tektonv1beta1.PipelineRun to a given MachineOSBuild status. Exported so that it can be used in e2e tests.
// TODO(rsaini): Retry logic in Pipelines (PipelineRun.Status.PipelineSpec.Tasks[*].Retries)
func MapPipelineRunStatusToBuildStatus(pipelineRun *tektonv1beta1.PipelineRun) (mcfgv1.BuildProgress, []metav1.Condition) {
	// If the pipelineRun is being deleted and it was not in either a successful or failed state
	// then the MachineOSBuild should be considered "interrupted"
	if (pipelineRun.DeletionTimestamp != nil && !pipelineRun.IsDone()) || pipelineRun.IsCancelled() {
		return mcfgv1.MachineOSBuildInterrupted, apihelpers.MachineOSBuildInterruptedConditions()
	}

	if pipelineRun.IsPending() {
		return mcfgv1.MachineOSBuildPrepared, apihelpers.MachineOSBuildPendingConditions()
	}

	if pipelineRun.Status.GetCondition(apis.ConditionSucceeded).IsTrue() {
		return mcfgv1.MachineOSBuildSucceeded, apihelpers.MachineOSBuildSucceededConditions()
	}

	if pipelineRun.IsDone() && pipelineRun.Status.GetCondition(apis.ConditionSucceeded).IsFalse() {
		return mcfgv1.MachineOSBuildFailed, apihelpers.MachineOSBuildFailedConditions()
	}

	if pipelineRun.HasStarted() {
		return mcfgv1.MachineOSBuilding, apihelpers.MachineOSBuildRunningConditions()
	}

	return "", apihelpers.MachineOSBuildInitialConditions()
}

// Returns true if the provided pipelineRun UID matches the BuildTypeUID annotation in the provided MachineOSBuild
func pipelineRunIsForMOSB(pipelineRun *tektonv1beta1.PipelineRun, mosb *mcfgv1.MachineOSBuild) bool {
	if mosb == nil {
		return false
	}

	if string(pipelineRun.UID) != mosb.GetAnnotations()[constants.BuildTypeUIDAnnotationKey] {
		return false
	}

	return true
}

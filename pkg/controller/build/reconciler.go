package build

import (
	"context"
	_ "embed"
	"fmt"
	"strings"
	"time"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	mcfgv1alpha1 "github.com/openshift/api/machineconfiguration/v1alpha1"
	mcfgclientset "github.com/openshift/client-go/machineconfiguration/clientset/versioned"
	"github.com/openshift/machine-config-operator/pkg/controller/build/buildrequest"
	"github.com/openshift/machine-config-operator/pkg/controller/build/constants"
	"github.com/openshift/machine-config-operator/pkg/controller/build/imagebuilder"
	"github.com/openshift/machine-config-operator/pkg/controller/build/utils"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	olmv1alpha1 "github.com/operator-framework/api/pkg/operators/v1alpha1"
	olmclientset "github.com/operator-framework/operator-lifecycle-manager/pkg/api/client/clientset/versioned"
	pipelineoperatorclientset "github.com/tektoncd/operator/pkg/client/clientset/versioned"
	tektonv1beta1 "github.com/tektoncd/pipeline/pkg/apis/pipeline/v1beta1"
	tektonclientset "github.com/tektoncd/pipeline/pkg/client/clientset/versioned"
	batchv1 "k8s.io/api/batch/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/wait"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"
	"knative.dev/pkg/apis"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

//go:embed buildrequest/assets/buildah-build-pipeline.sh
var buildahBuildPipelineScript string

const (
	addingVerb   string = "Adding"
	updatingVerb string = "Updating"
	deletingVerb string = "Deleting"
	syncingVerb  string = "Syncing"
)

type reconciler interface {
	AddMachineOSBuild(context.Context, *mcfgv1alpha1.MachineOSBuild) error
	UpdateMachineOSBuild(context.Context, *mcfgv1alpha1.MachineOSBuild, *mcfgv1alpha1.MachineOSBuild) error
	DeleteMachineOSBuild(context.Context, *mcfgv1alpha1.MachineOSBuild) error

	AddMachineOSConfig(context.Context, *mcfgv1alpha1.MachineOSConfig) error
	UpdateMachineOSConfig(context.Context, *mcfgv1alpha1.MachineOSConfig, *mcfgv1alpha1.MachineOSConfig) error
	DeleteMachineOSConfig(context.Context, *mcfgv1alpha1.MachineOSConfig) error

	AddJob(context.Context, *batchv1.Job) error
	UpdateJob(context.Context, *batchv1.Job, *batchv1.Job) error
	DeleteJob(context.Context, *batchv1.Job) error

	UpdateMachineConfigPool(context.Context, *mcfgv1.MachineConfigPool, *mcfgv1.MachineConfigPool) error
}

// Holds the implementation of the buildReconciler. The buildReconciler's job
// is to respond to incoming events in a specific way. By doing this, the
// reconciliation process has a clear entrypoint for each incoming event.
type buildReconciler struct {
	mcfgclient             mcfgclientset.Interface
	kubeclient             clientset.Interface
	pipelineoperatorclient pipelineoperatorclientset.Interface
	olmclient              olmclientset.Interface
	tektonclient	       tektonclientset.Interface
	*listers
}

// Instantiates a new reconciler instance. This returns an interface to
// disallow access to its private methods.
func newBuildReconciler(mcfgclient mcfgclientset.Interface, kubeclient clientset.Interface, pipelineoperatorclient pipelineoperatorclientset.Interface, olmclient olmclientset.Interface, tektonclient tektonclientset.Interface, l *listers) reconciler {
	return newBuildReconcilerAsStruct(mcfgclient, kubeclient, pipelineoperatorclient, olmclient, tektonclient, l)
}

func newBuildReconcilerAsStruct(mcfgclient mcfgclientset.Interface, kubeclient clientset.Interface, pipelineoperatorclient pipelineoperatorclientset.Interface, olmclient olmclientset.Interface, tektonclient tektonclientset.Interface, l *listers) *buildReconciler {
	return &buildReconciler{
		mcfgclient:             mcfgclient,
		kubeclient:             kubeclient,
		pipelineoperatorclient: pipelineoperatorclient,
		olmclient:              olmclient,
		tektonclient:		tektonclient,
		listers:                l,
	}
}

// Executes whenever a new MachineOSConfig is added.
func (b *buildReconciler) AddMachineOSConfig(ctx context.Context, mosc *mcfgv1alpha1.MachineOSConfig) error {
	return b.timeObjectOperation(mosc, addingVerb, func() error {
		if err := b.addMachineOSConfig(ctx, mosc); err != nil {
			return err
		}

		return b.syncMachineOSConfigs(ctx)
	})
}

// Executes whenever an existing MachineOSConfig is updated.
func (b *buildReconciler) UpdateMachineOSConfig(ctx context.Context, old, cur *mcfgv1alpha1.MachineOSConfig) error {
	return b.timeObjectOperation(cur, updatingVerb, func() error {
		return b.updateMachineOSConfig(ctx, old, cur)
	})
}

// Executes whenever a MachineOSConfig is updated. If the build inputs have
// changed, a new MachineOSBuild should be created.
func (b *buildReconciler) updateMachineOSConfig(ctx context.Context, old, cur *mcfgv1alpha1.MachineOSConfig) error {
	// If we have gained the rebuild annotation, we should delete the current MachineOSBuild associated with this MachineOSConfig.
	if !hasRebuildAnnotation(old) && hasRebuildAnnotation(cur) {
		if err := b.rebuildMachineOSConfig(ctx, cur); err != nil {
			return fmt.Errorf("could not rebuild MachineOSConfig %q: %w", cur.Name, err)
		}

		return nil
	}

	// Whenever the build inputs have changed, create a new MachineOSBuild.
	if !equality.Semantic.DeepEqual(old.Spec.BuildInputs, cur.Spec.BuildInputs) {
		klog.Infof("Detected MachineOSConfig change for %s", cur.Name)

		if cur.Spec.BuildInputs.ImageBuilder.ImageBuilderType == mcfgv1alpha1.PipelineBuilder {
			// Check and install pipeline
			err := checkAndInstallPipeline(ctx, b.kubeclient, b.pipelineoperatorclient, b.olmclient, b.tektonclient)
			if err != nil {
				return fmt.Errorf("error checking pipeline exists and installing: %v", err)
			}
		}

		return b.createNewMachineOSBuildOrReuseExisting(ctx, cur)
	}

	return b.syncMachineOSConfigs(ctx)
}

func checkAndInstallPipeline(ctx context.Context, kubeclient clientset.Interface, pipelineoperatorclient pipelineoperatorclientset.Interface, olmclient olmclientset.Interface, tektonclient tektonclientset.Interface) error {
	tektonNamespace := "openshift-pipelines"
	operatorsNamespace := "openshift-operators"
	tektonConfigName := "config"
	subscriptionName := "openshift-pipelines-operator"
	tektonPipelineName := "build-and-push-pipeline"
	tektonClusterTaskName := "buildah"
	var namespaceDNE, tektonconfigDNE, tektonPipelineDNE bool = false, false, false
	
	// Ensure "openshift-pipelines" Namespace exists
	_, err := kubeclient.CoreV1().Namespaces().Get(ctx, tektonNamespace, metav1.GetOptions{})
	if err != nil {
		if k8serrors.IsNotFound(err) {
			namespaceDNE = true
		} else {
			return fmt.Errorf("openshift-pipelines namespace get error %v", err)
		}
	}

	// Ensure TektonConfig exists
	_, err = pipelineoperatorclient.OperatorV1alpha1().TektonConfigs().Get(ctx, tektonConfigName, metav1.GetOptions{})
	if err != nil {
		if k8serrors.IsNotFound(err) {
			tektonconfigDNE = true
		} else {
			return fmt.Errorf("tektonConfig resource get error %v", err)
		}
	}
	if namespaceDNE || tektonconfigDNE {
		// Define the Subscription resource
		subscription := &olmv1alpha1.Subscription{
			TypeMeta: metav1.TypeMeta{
				APIVersion: "operators.coreos.com/v1alpha1",
				Kind:       "Subscription",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name:      subscriptionName,
				Namespace: "openshift-operators",
			},
			Spec: &olmv1alpha1.SubscriptionSpec{
				Channel:                "latest",
				Package:                "openshift-pipelines-operator-rh",
				CatalogSource:          "redhat-operators",
				CatalogSourceNamespace: "openshift-marketplace",
			},
		}
		_, err = olmclient.OperatorsV1alpha1().Subscriptions(operatorsNamespace).Create(ctx, subscription, metav1.CreateOptions{})
		if err != nil {
			if k8serrors.IsAlreadyExists(err) {
				klog.V(2).Infof("%v already exists", subscriptionName)
			} else {
				return fmt.Errorf("subscription resource create error %v", err)
			}
		}
	}
	interval := 1 * time.Minute
	timeout := 20 * time.Minute
	err = waitForTektonConfigReady(ctx, pipelineoperatorclient, tektonNamespace, tektonConfigName, interval, timeout)
	if err != nil {
		return err
	}

	/*
	// Ensure Buildah Task exists
	// Potential problem: ClusterTasks don't exist in V1 API hence the Buildah task may need to be installed into MCO Namespace
	_, err = tektonclient.TektonV1().Tasks(ctrlcommon.MCONamespace).Get(ctx, tektonClusterTaskName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("Error getting ClusterTask Buildah: %v", err)
	}
	*/

	// Ensure Buildah Pipeline exists
	_, err = tektonclient.TektonV1beta1().Pipelines(ctrlcommon.MCONamespace).Get(context.Background(), tektonPipelineName, metav1.GetOptions{})
	if err != nil { 
		if k8serrors.IsNotFound(err) {
			tektonPipelineDNE = true
			klog.V(2).Infof("Buildah pipeline DNE, creating")
		} else {
			return fmt.Errorf("Error getting Pipeline: %v", err)
		}
	}

	if tektonPipelineDNE {
		// TODO(rsaini) Define the pipeline "buildAndPush" here and create an API object here. Check if it already exists before
		pipeline := &tektonv1beta1.Pipeline{
			ObjectMeta: metav1.ObjectMeta{
				Name:      tektonPipelineName,
				Namespace: ctrlcommon.MCONamespace,
			},
			Spec: tektonv1beta1.PipelineSpec{
				Params: []tektonv1beta1.ParamSpec{
					tektonv1beta1.ParamSpec{Name: "logLevel", Type: tektonv1beta1.ParamTypeString, Description: "log level"},
					tektonv1beta1.ParamSpec{Name: "storageDriver", Type: tektonv1beta1.ParamTypeString, Description: "storage driver"},
					tektonv1beta1.ParamSpec{Name: "authfileBuild", Type: tektonv1beta1.ParamTypeString, Description: "authfileBuild"},
					tektonv1beta1.ParamSpec{Name: "authfilePush", Type: tektonv1beta1.ParamTypeString, Description: "authfilePush"},
					tektonv1beta1.ParamSpec{Name: "tag", Type: tektonv1beta1.ParamTypeString, Description: "Image URL"},
					tektonv1beta1.ParamSpec{Name: "containerFile", Type: tektonv1beta1.ParamTypeString, Description: "container file"},
					tektonv1beta1.ParamSpec{Name: "httpProxy", Type: tektonv1beta1.ParamTypeString, Description: "httpproxy", Default: &tektonv1beta1.ArrayOrString{Type: tektonv1beta1.ParamTypeString, StringVal: ""}},
					tektonv1beta1.ParamSpec{Name: "httpsProxy", Type: tektonv1beta1.ParamTypeString, Description: "httpsproxy", Default: &tektonv1beta1.ArrayOrString{Type: tektonv1beta1.ParamTypeString, StringVal: ""}},
					tektonv1beta1.ParamSpec{Name: "noProxy", Type: tektonv1beta1.ParamTypeString, Description: "noproxy", Default: &tektonv1beta1.ArrayOrString{Type: tektonv1beta1.ParamTypeString, StringVal: ""}},
					tektonv1beta1.ParamSpec{Name: "buildContext", Type: tektonv1beta1.ParamTypeString, Description: "context"},
					tektonv1beta1.ParamSpec{Name: "image", Type: tektonv1beta1.ParamTypeString, Description: "image"},
					tektonv1beta1.ParamSpec{Name: "machineConfig", Type: tektonv1beta1.ParamTypeString, Description: "machine config"},
					tektonv1beta1.ParamSpec{Name: "additionalTrustBundle", Type: tektonv1beta1.ParamTypeString, Description: "additional trust bundle"},
				},
				Results: []tektonv1beta1.PipelineResult{
					tektonv1beta1.PipelineResult{Name: "IMAGE_DIGEST", Type: tektonv1beta1.ResultsTypeString, Description: "Digest of the image just built", Value: tektonv1beta1.ArrayOrString{Type: tektonv1beta1.ParamTypeString, StringVal: "$(tasks.buildah-build.results.IMAGE_DIGEST)"}},
					tektonv1beta1.PipelineResult{Name: "IMAGE_URL", Type: tektonv1beta1.ResultsTypeString, Description: "Image repository where the built image would be pushed to", Value: tektonv1beta1.ArrayOrString{Type: tektonv1beta1.ParamTypeString, StringVal: "$(tasks.buildah-build.results.IMAGE_URL)"}},
				},
				Workspaces: []tektonv1beta1.PipelineWorkspaceDeclaration{
					tektonv1beta1.PipelineWorkspaceDeclaration{Name: "source"},
				},
				Tasks: []tektonv1beta1.PipelineTask{
					tektonv1beta1.PipelineTask{
						Name: "prepare-environment",
						TaskSpec: &tektonv1beta1.EmbeddedTask{
							TaskSpec: tektonv1beta1.TaskSpec{
								Workspaces: []tektonv1beta1.WorkspaceDeclaration{
									tektonv1beta1.WorkspaceDeclaration{
										Name: "source",
									},
								},
								Steps: []tektonv1beta1.Step{
									tektonv1beta1.Step{
										Name:  "setup-environment",
										Image: "$(params.image)",
										Script: buildahBuildPipelineScript,
									},
								},
							},
						},
						Workspaces: []tektonv1beta1.WorkspacePipelineTaskBinding{
							tektonv1beta1.WorkspacePipelineTaskBinding{Name: "source", Workspace: "source"},
						},
					},
					tektonv1beta1.PipelineTask{
						Name: "buildah-build",
						TaskRef: &tektonv1beta1.TaskRef{
							ResolverRef: tektonv1beta1.ResolverRef{
								Resolver: "cluster",
								Params: []tektonv1beta1.Param{
									{Name: "name", Value: tektonv1beta1.ArrayOrString{Type: tektonv1beta1.ParamTypeString, StringVal: tektonClusterTaskName}},
									{Name: "namespace", Value: tektonv1beta1.ArrayOrString{Type: tektonv1beta1.ParamTypeString, StringVal: tektonNamespace}},
									{Name: "kind", Value: tektonv1beta1.ArrayOrString{Type: tektonv1beta1.ParamTypeString, StringVal: "task"}},
								},
							},
						},
						Params: []tektonv1beta1.Param{
							tektonv1beta1.Param{Name: "IMAGE", Value: tektonv1beta1.ArrayOrString{Type: tektonv1beta1.ParamTypeString, StringVal: "$(params.tag)"}},
							tektonv1beta1.Param{Name: "STORAGE_DRIVER", Value: tektonv1beta1.ArrayOrString{Type: tektonv1beta1.ParamTypeString, StringVal: "$(params.storageDriver)"}},
							tektonv1beta1.Param{Name: "DOCKERFILE", Value: tektonv1beta1.ArrayOrString{Type: tektonv1beta1.ParamTypeString, StringVal: "$(params.containerFile)"}},
							tektonv1beta1.Param{Name: "CONTEXT", Value: tektonv1beta1.ArrayOrString{Type: tektonv1beta1.ParamTypeString, StringVal: "$(params.buildContext)"}},
							tektonv1beta1.Param{Name: "BUILD_EXTRA_ARGS", Value: tektonv1beta1.ArrayOrString{Type: tektonv1beta1.ParamTypeString, StringVal: "--authfile=$(params.authfileBuild) --log-level=$(params.logLevel)"}},
							tektonv1beta1.Param{Name: "BUILD_ARGS", Value: tektonv1beta1.ParamValue{Type: tektonv1beta1.ParamTypeArray, ArrayVal: []string{"HTTP_PROXY=$(params.httpProxy)", "HTTPS_PROXY=$(params.httpsProxy)", "NO_PROXY=$(params.noProxy)"}}},
							tektonv1beta1.Param{Name: "PUSH_EXTRA_ARGS", Value: tektonv1beta1.ArrayOrString{Type: tektonv1beta1.ParamTypeString, StringVal: "--authfile=$(params.authfilePush)"}},
						},
						Workspaces: []tektonv1beta1.WorkspacePipelineTaskBinding{
							tektonv1beta1.WorkspacePipelineTaskBinding{Name: "source", Workspace: "source"},
						},
					},
				},
			},
		}

		_, err = tektonclient.TektonV1beta1().Pipelines(ctrlcommon.MCONamespace).Create(context.Background(), pipeline, metav1.CreateOptions{})
		if err != nil {
			return fmt.Errorf("Error creating Pipeline: %v", err)
		}
	}

	return nil
}

// waitForTektonConfigReady waits for the TektonConfig's Ready condition to become True.
func waitForTektonConfigReady(ctx context.Context, client pipelineoperatorclientset.Interface, namespace, name string, interval, timeout time.Duration) error {
	return wait.PollUntilContextCancel(ctx, interval, true, func(ctx context.Context) (bool, error) {
		// Fetch the TektonConfig resource
		tektonConfig, err := client.OperatorV1alpha1().TektonConfigs().Get(ctx, name, metav1.GetOptions{})
		if err != nil { 
			if k8serrors.IsNotFound(err) {
				klog.V(2).Infof("trying to read tektonconfig in waitForTektonConfigReady")
				return false, nil
			} else {
				return false, fmt.Errorf("failed to fetch TektonConfig: %v", err)
			}
		}
		// Check if the Ready condition is True
		readyCondition := tektonConfig.Status.GetCondition(apis.ConditionReady)
		if readyCondition != nil && readyCondition.Status == "True" {
			return true, nil
		}
		return false, nil
	})
}

// Rebuilds the most current build associated with a MachineOSConfig whenever
// the rebuild annotation is applied. This is done by deleting the current
// MachineOSBuild and allowing the controller to replace it with a new one.
func (b *buildReconciler) rebuildMachineOSConfig(ctx context.Context, mosc *mcfgv1alpha1.MachineOSConfig) error {
	klog.Infof("MachineOSConfig %q has rebuild annotation (%q)", mosc.Name, constants.RebuildMachineOSConfigAnnotationKey)

	if !hasCurrentBuildAnnotation(mosc) {
		klog.Infof("MachineOSConfig %q does not have current build annotation (%q) set, skipping rebuild", mosc.Name, constants.CurrentMachineOSBuildAnnotationKey)
		return nil
	}

	mosbName := mosc.Annotations[constants.CurrentMachineOSBuildAnnotationKey]

	mosb, err := b.machineOSBuildLister.Get(mosbName)
	if err != nil {
		return ignoreErrIsNotFound(fmt.Errorf("cannot rebuild MachineOSConfig %q: %w", mosc.Name, err))
	}

	if err := b.deleteMachineOSBuild(ctx, mosb); err != nil {
		return fmt.Errorf("could not delete MachineOSBuild %q for MachineOSConfig %q: %w", mosb.Name, mosc.Name, err)
	}

	if err := b.createNewMachineOSBuildOrReuseExisting(ctx, mosc); err != nil {
		return fmt.Errorf("could not create new MachineOSBuild for MachineOSConfig %q: %w", mosc.Name, err)
	}

	klog.Infof("MachineOSConfig %q is now rebuilding", mosc.Name)

	return nil
}

// Runs whenever a new MachineOSConfig is added. Determines if a new
// MachineOSBuild should be created and then creates it, if needed.
func (b *buildReconciler) addMachineOSConfig(ctx context.Context, mosc *mcfgv1alpha1.MachineOSConfig) error {
	if mosc.Spec.BuildInputs.ImageBuilder.ImageBuilderType == mcfgv1alpha1.PipelineBuilder {
		// Check and install pipeline
		err := checkAndInstallPipeline(ctx, b.kubeclient, b.pipelineoperatorclient, b.olmclient, b.tektonclient)
		if err != nil {
			return fmt.Errorf("error checking pipeline exists and installing: %v", err)
		}
	}
	return b.syncMachineOSConfig(ctx, mosc)
}

// Executes whenever a MachineOSConfig is deleted. This deletes all
// MachineOSBuilds (and the underlying associated build objects).
func (b *buildReconciler) DeleteMachineOSConfig(ctx context.Context, mosc *mcfgv1alpha1.MachineOSConfig) error {
	return b.timeObjectOperation(mosc, deletingVerb, func() error {
		return b.deleteMachineOSConfig(ctx, mosc)
	})
}

// Performs the deletion reconciliation of the MachineOSConfig.
func (b *buildReconciler) deleteMachineOSConfig(ctx context.Context, mosc *mcfgv1alpha1.MachineOSConfig) error {
	klog.Infof("Removing MachineOSBuild(s) associated with non-existent MachineOSConfig %s", mosc.Name)

	mosbList, err := b.machineOSBuildLister.List(utils.MachineOSBuildForPoolSelector(mosc))
	if err != nil {
		return fmt.Errorf("could not list MachineOSBuilds for MachineOSConfig %s: %w", mosc.Name, err)
	}

	for _, mosb := range mosbList {
		if err := b.deleteMachineOSBuild(ctx, mosb); err != nil {
			return fmt.Errorf("could not delete MachineOSBuild %s for MachineOSConfig %s: %w", mosb.Name, mosc.Name, err)
		}
	}

	return nil
}

// Executes whenever a new build Job is detected and updates the MachineOSBuild
// with any status changes.
func (b *buildReconciler) AddJob(ctx context.Context, job *batchv1.Job) error {
	return b.timeObjectOperation(job, addingVerb, func() error {
		klog.Infof("Adding build job %q", job.Name)
		return b.syncAll(ctx)
	})
}

// Executes whenever a build Job is updated
func (b *buildReconciler) UpdateJob(ctx context.Context, oldJob, curJob *batchv1.Job) error {
	return b.timeObjectOperation(curJob, updatingVerb, func() error {
		return b.updateMachineOSBuildWithStatusIfNeeded(ctx, oldJob, curJob)
	})
}

// Executes whenever a build Job is deleted
func (b *buildReconciler) DeleteJob(ctx context.Context, job *batchv1.Job) error {
	return b.timeObjectOperation(job, deletingVerb, func() error {
		// Set the DeletionTimestamp so that we can set the build status to interrupted
		job.SetDeletionTimestamp(&metav1.Time{Time: time.Now()})
		err := b.updateMachineOSBuildWithStatus(ctx, job)
		if err != nil {
			return err
		}
		klog.Infof("Job %q deleted", job.Name)
		return b.syncAll(ctx)
	})
}

// Executes whenever a new MachineOSBuild is added. It starts executing the
// build in response to a new MachineOSBuild being created.
func (b *buildReconciler) AddMachineOSBuild(ctx context.Context, mosb *mcfgv1alpha1.MachineOSBuild) error {
	return b.timeObjectOperation(mosb, addingVerb, func() error {
		return b.addMachineOSBuild(ctx, mosb)
	})
}

// Executes whenever a MachineOSBuild is updated.
func (b *buildReconciler) UpdateMachineOSBuild(ctx context.Context, old, cur *mcfgv1alpha1.MachineOSBuild) error {
	return b.timeObjectOperation(cur, updatingVerb, func() error {
		if err := b.updateMachineOSBuild(ctx, old, cur); err != nil {
			return fmt.Errorf("could not update MachineOSBuild: %w", err)
		}

		return b.syncMachineOSBuilds(ctx)
	})
}

// Performs the reconciliation whenever the MachineOSBuild is updated, such as
// cleaning up the build artifacts upon success.
func (b *buildReconciler) updateMachineOSBuild(ctx context.Context, old, current *mcfgv1alpha1.MachineOSBuild) error {
	mosc, err := utils.GetMachineOSConfigForMachineOSBuild(current, b.utilListers())
	if err != nil {
		// If a MachineOSConfig is deleted before the MachineOSBuild is, we should
		// ignore any not found errors.
		return ignoreErrIsNotFound(fmt.Errorf("could not update MachineOSBuild %q: %w", current.Name, err))
	}

	oldState := ctrlcommon.NewMachineOSBuildState(old)
	curState := ctrlcommon.NewMachineOSBuildState(current)

	if !oldState.HasBuildConditions() && curState.HasBuildConditions() &&
		!oldState.IsInInitialState() && curState.IsInInitialState() {
		klog.Infof("Initial MachineOSBuild %q status update", current.Name)
		return nil
	}

	if !oldState.IsBuildFailure() && curState.IsBuildFailure() {
		klog.Infof("MachineOSBuild %s failed, leaving ephemeral objects in place for inspection", current.Name)
		mcp, err := b.machineConfigPoolLister.Get(mosc.Spec.MachineConfigPool.Name)
		if err != nil {
			return fmt.Errorf("could not get MachineConfigPool from MachineOSConfig %q: %w", mosc.Name, err)
		}

		// Just so I don't have to remove the mcp right now :P
		klog.Infof("Target MachineConfigPool %q", mcp.Name)

		// Implement code to degrade MCP
		return nil
	}

	// If the build was successful, clean up the build objects and propagate the
	// final image pushspec onto the MachineOSConfig object.
	if !oldState.IsBuildSuccess() && curState.IsBuildSuccess() {
		klog.Infof("MachineOSBuild %s succeeded, cleaning up all ephemeral objects used for the build", current.Name)
		if err := imagebuilder.NewJobImageBuilder(b.kubeclient, b.mcfgclient, b.tektonclient, current, mosc).Clean(ctx); err != nil {
			return err
		}

		if err := b.updateMachineOSConfigStatus(ctx, mosc, current); err != nil {
			return fmt.Errorf("could not update MachineOSConfig %q status for successful MachineOSBuild %q: %w", mosc.Name, current.Name, err)
		}
	}

	return nil
}

// Updates the status on the MachineOSConfig object from the supplied MachineOSBuild object.
func (b *buildReconciler) updateMachineOSConfigStatus(ctx context.Context, mosc *mcfgv1alpha1.MachineOSConfig, mosb *mcfgv1alpha1.MachineOSBuild) error {
	mosc, err := b.getMachineOSConfigForUpdate(mosc)
	if err != nil {
		return err
	}

	annoUpdateNeeded := false

	if hasRebuildAnnotation(mosc) {
		delete(mosc.Annotations, constants.RebuildMachineOSConfigAnnotationKey)
		annoUpdateNeeded = true
		klog.Infof("Cleared rebuild annotation (%q) on MachineOSConfig %q", constants.RebuildMachineOSConfigAnnotationKey, mosc.Name)
	}

	if !isCurrentBuildAnnotationEqual(mosc, mosb) {
		metav1.SetMetaDataAnnotation(&mosc.ObjectMeta, constants.CurrentMachineOSBuildAnnotationKey, mosb.Name)
		annoUpdateNeeded = true
		klog.Infof("Set current build on MachineOSConfig %q to MachineOSBuild %q", mosc.Name, mosb.Name)
	}

	if annoUpdateNeeded {
		updatedMosc, err := b.mcfgclient.MachineconfigurationV1alpha1().MachineOSConfigs().Update(ctx, mosc, metav1.UpdateOptions{})
		if err != nil {
			return fmt.Errorf("could not update annotations on MachineOSConfig %q: %w", mosc.Name, err)
		}

		klog.Infof("Updated annotations on MachineOSConfig %q", mosc.Name)

		mosc = updatedMosc
	}

	// Skip the status update if final image pushspec hasn't been set yet.
	if mosb.Status.FinalImagePushspec == "" {
		klog.Infof("MachineOSBuild %q has empty final image pushspec, skipping MachineOSConfig %q status update", mosb.Name, mosc.Name)
		return nil
	}

	// skip the status update if the current image pullspec equals the final image pushspec.
	if mosc.Status.CurrentImagePullspec == mosb.Status.FinalImagePushspec {
		klog.Infof("MachineOSConfig %q already has final image pushspec for MachineOSBuild %q", mosc.Name, mosb.Name)
		return nil
	}

	mosc.Status.CurrentImagePullspec = mosb.Status.FinalImagePushspec
	// TODO: Reconsider this.
	mosc.Status.ObservedGeneration += mosc.GetGeneration()

	_, err = b.mcfgclient.MachineconfigurationV1alpha1().MachineOSConfigs().UpdateStatus(ctx, mosc, metav1.UpdateOptions{})
	if err == nil {
		klog.Infof("Updated status on MachineOSConfig %s", mosc.Name)
	}

	return err
}

// Executes whenever a MachineOSBuild is deleted by cleaning up any remaining build artifacts that may be left behind.
func (b *buildReconciler) DeleteMachineOSBuild(ctx context.Context, mosb *mcfgv1alpha1.MachineOSBuild) error {
	return b.timeObjectOperation(mosb, deletingVerb, func() error {
		return b.deleteBuilderForMachineOSBuild(ctx, mosb)
	})
}

// Executes whenever a MachineConfigPool is updated .
func (b *buildReconciler) UpdateMachineConfigPool(ctx context.Context, oldMCP, curMCP *mcfgv1.MachineConfigPool) error {
	return b.timeObjectOperation(curMCP, updatingVerb, func() error {
		return b.updateMachineConfigPool(ctx, oldMCP, curMCP)
	})
}

// Performs the reconciliation whenever a MachineConfigPool is updated.
// Sepcifically, whenever a new rendered MachineConfig is applied, it will
// create a new MachineOSBuild in response.
func (b *buildReconciler) updateMachineConfigPool(ctx context.Context, oldMCP, curMCP *mcfgv1.MachineConfigPool) error {
	if oldMCP.Spec.Configuration.Name != curMCP.Spec.Configuration.Name {
		klog.Infof("Rendered config for pool %s changed from %s to %s", curMCP.Name, oldMCP.Spec.Configuration.Name, curMCP.Spec.Configuration.Name)
		if err := b.createNewMachineOSBuildOrReuseExistingForPoolChange(ctx, curMCP); err != nil {
			return fmt.Errorf("could not create or reuse existing MachineOSBuild for MachineConfigPool %q change: %w", curMCP.Name, err)
		}
	}

	// Not sure if we need to do this here yet or not.
	return b.syncAll(ctx)
}

// Adds a MachineOSBuild.
func (b *buildReconciler) addMachineOSBuild(ctx context.Context, mosb *mcfgv1alpha1.MachineOSBuild) error {
	return b.syncMachineOSBuild(ctx, mosb)
}

// Starts executing a build for a given MachineOSBuild.
func (b *buildReconciler) startBuild(ctx context.Context, mosb *mcfgv1alpha1.MachineOSBuild) error {
	mosc, err := utils.GetMachineOSConfigForMachineOSBuild(mosb, b.utilListers())
	if err != nil {
		return err
	}

	// If there are any other in-progress builds for this MachineOSConfig, stop them first.
	if err := b.deleteOtherBuildsForMachineOSConfig(ctx, mosb, mosc); err != nil {
		return fmt.Errorf("could not delete other non-terminal MachineOSBuilds for MachineOSConfig %s: %w", mosc.Name, err)
	}

	switch mosc.Spec.BuildInputs.ImageBuilder.ImageBuilderType {
	case mcfgv1alpha1.PodBuilder:
		// Next, create our new MachineOSBuild.
		if err := imagebuilder.NewJobImageBuilder(b.kubeclient, b.mcfgclient, b.tektonclient, mosb, mosc).Start(ctx); err != nil {
			return fmt.Errorf("imagebuilder could not start build for MachineOSBuild %q: %w", mosb.Name, err)
		}
	case mcfgv1alpha1.PipelineBuilder:
		if err := imagebuilder.NewPipelineImageBuilder(b.kubeclient, b.mcfgclient, b.tektonclient, mosb, mosc).Start(ctx); err != nil {
			return fmt.Errorf("imagebuilder could not start build for MachineOSBuild %q: %w", mosb.Name, err)
		}
	default:
		return fmt.Errorf("ImageBuilderType: %s is not supported", mosc.Spec.BuildInputs.ImageBuilder.ImageBuilderType)
	}

	klog.Infof("Started new build %s for MachineOSBuild", utils.GetBuildName(mosb))

	if err := b.updateMachineOSConfigStatus(ctx, mosc, mosb); err != nil {
		return fmt.Errorf("could not update MachineOSConfig %q status for MachineOSBuild %q: %w", mosc.Name, mosb.Name, err)
	}

	return nil
}

// Retrieves a deep-copy of the MachineOSConfig from the lister so that the cache is not mutated during the update.
func (b *buildReconciler) getMachineOSConfigForUpdate(mosc *mcfgv1alpha1.MachineOSConfig) (*mcfgv1alpha1.MachineOSConfig, error) {
	out, err := b.machineOSConfigLister.Get(mosc.Name)

	if err != nil {
		return nil, err
	}

	return out.DeepCopy(), nil
}

// Retrieves a deep-copy of the MachineOSBuild from the lister so that the cache is not mutated during the update.
func (b *buildReconciler) getMachineOSBuildForUpdate(mosb *mcfgv1alpha1.MachineOSBuild) (*mcfgv1alpha1.MachineOSBuild, error) {
	out, err := b.machineOSBuildLister.Get(mosb.Name)

	if err != nil {
		return nil, err
	}

	return out.DeepCopy(), nil
}

// Creates a MachineOSBuild in response to MachineConfigPool changes.
func (b *buildReconciler) createNewMachineOSBuildOrReuseExistingForPoolChange(ctx context.Context, mcp *mcfgv1.MachineConfigPool) error {
	mosc, err := utils.GetMachineOSConfigForMachineConfigPool(mcp, b.utilListers())

	if k8serrors.IsNotFound(err) {
		klog.Infof("No MachineOSConfig found for MachineConfigPool %s", mcp.Name)
		return nil
	}

	if err != nil {
		return err
	}

	if err := b.createNewMachineOSBuildOrReuseExisting(ctx, mosc.DeepCopy()); err != nil {
		return fmt.Errorf("could not create MachineOSBuild for MachineConfigPool %q change: %w", mcp.Name, err)
	}

	return nil
}

// Executes whenever a new MachineOSBuild is created.
func (b *buildReconciler) createNewMachineOSBuildOrReuseExisting(ctx context.Context, mosc *mcfgv1alpha1.MachineOSConfig) error {
	mcp, err := b.machineConfigPoolLister.Get(mosc.Spec.MachineConfigPool.Name)
	if err != nil {
		return fmt.Errorf("could not get MachineConfigPool %s for MachineOSConfig %s: %w", mosc.Spec.MachineConfigPool.Name, mosc.Name, err)
	}

	// TODO: Consider what we should do in the event of a degraded MachineConfigPool.
	if ctrlcommon.IsPoolAnyDegraded(mcp) {
		return fmt.Errorf("MachineConfigPool %s is degraded", mcp.Name)
	}

	// TODO: Consider using a ConfigMap lister to get this value instead of the API server.
	osImageURLs, err := ctrlcommon.GetOSImageURLConfig(ctx, b.kubeclient)
	if err != nil {
		return fmt.Errorf("could not get OSImageURLConfig: %w", err)
	}

	// Construct a new MachineOSBuild object which has the hashed name attached
	// to it.
	mosb, err := buildrequest.NewMachineOSBuild(buildrequest.MachineOSBuildOpts{
		MachineOSConfig:   mosc,
		MachineConfigPool: mcp,
		OSImageURLConfig:  osImageURLs,
	})

	if err != nil {
		return fmt.Errorf("could not instantiate new MachineOSBuild: %w", err)
	}

	existingMosb, err := b.machineOSBuildLister.Get(mosb.Name)
	if err != nil && !k8serrors.IsNotFound(err) {
		return fmt.Errorf("could not get MachineOSBuild: %w", err)
	}

	// If err is nil, it means a MachineOSBuild with this name already exists.
	// What likely happened is that a config change was rolled back to the
	// previous state. Rather than performing another build, we should get the
	// previously built image pullspec and adjust the MachineOSConfig to use that
	// image instead.
	if err == nil && existingMosb != nil {
		if err := b.reuseExistingMachineOSBuildIfPossible(ctx, mosc, existingMosb); err != nil {
			return fmt.Errorf("could not reuse existing MachineOSBuild %q for MachineOSConfig %q: %w", existingMosb.Name, mosc.Name, err)
		}

		return nil
	}

	// In this situation, we've determined that the MachineOSBuild does not
	// exist, so we need to create it.
	if k8serrors.IsNotFound(err) {
		_, err := b.mcfgclient.MachineconfigurationV1alpha1().MachineOSBuilds().Create(ctx, mosb, metav1.CreateOptions{})
		if err != nil {
			return fmt.Errorf("could not create new MachineOSBuild %q: %w", mosb.Name, err)
		}

		if err == nil {
			klog.Infof("New MachineOSBuild created: %s", mosb.Name)
		}
	}

	return nil
}

// Determines if a preexising MachineOSBuild can be reused and if possible, does it.
func (b *buildReconciler) reuseExistingMachineOSBuildIfPossible(ctx context.Context, mosc *mcfgv1alpha1.MachineOSConfig, existingMosb *mcfgv1alpha1.MachineOSBuild) error {
	existingMosbState := ctrlcommon.NewMachineOSBuildState(existingMosb)

	canBeReused := false

	// If the existing build is a success and has the image pushspec set, it can be reused.
	if existingMosbState.IsBuildSuccess() && existingMosb.Status.FinalImagePushspec != "" {
		klog.Infof("Existing MachineOSBuild %q found, reusing image %q by assigning to MachineOSConfig %q", existingMosb.Name, existingMosb.Status.FinalImagePushspec, mosc.Name)
		canBeReused = true
	}

	// If the existing build is in a transient state, it can be reused.
	if existingMosbState.IsInTransientState() {
		klog.Infof("Existing MachineOSBuild %q found in transient state, assigning to MachineOSConfig %q", existingMosb.Name, mosc.Name)
		canBeReused = true
	}

	if canBeReused {
		// Stop any other running builds.
		if err := b.deleteOtherBuildsForMachineOSConfig(ctx, existingMosb, mosc); err != nil {
			return fmt.Errorf("could not delete running builds for MachineOSConfig %q after reusing existing MachineOSBuild %q: %w", mosc.Name, existingMosb.Name, err)
		}

		// Update the MachineOSConfig to use the preexisting MachineOSBuild.
		if err := b.updateMachineOSConfigStatus(ctx, mosc, existingMosb); err != nil {
			return fmt.Errorf("could not update MachineOSConfig %q status to reuse preexisting MachineOSBuild %q: %w", mosc.Name, existingMosb.Name, err)
		}
	}

	return nil
}

// Gets the MachineOSBuild status from the provided metav1.Object which can be
// converted into a Builder.
func (b *buildReconciler) getMachineOSBuildStatusForBuilder(ctx context.Context, obj metav1.Object) (mcfgv1alpha1.MachineOSBuildStatus, *mcfgv1alpha1.MachineOSBuild, error) {
	builder, err := buildrequest.NewBuilder(obj)
	if err != nil {
		return mcfgv1alpha1.MachineOSBuildStatus{}, nil, fmt.Errorf("could not instantiate builder: %w", err)
	}

	mosc, mosb, err := b.getMachineOSConfigAndMachineOSBuildForBuilder(builder)
	if err != nil {
		return mcfgv1alpha1.MachineOSBuildStatus{}, nil, fmt.Errorf("could not get MachineOSConfig or MachineOSBuild for builder: %w", err)
	}

	observer := imagebuilder.NewJobImageBuildObserverFromBuilder(b.kubeclient, b.mcfgclient, b.tektonclient, mosb, mosc, builder)

	status, err := observer.MachineOSBuildStatus(ctx)
	if err != nil {
		return status, mosb, fmt.Errorf("could not get status for MachineOSBuild %q: %w", mosb.Name, err)
	}

	return status, mosb, nil
}

// Gets the status from both the old and current Builder objects before handing
// the decision off to setStatusOnMachineOSBuildIfNeeded.
func (b *buildReconciler) updateMachineOSBuildWithStatusIfNeeded(ctx context.Context, oldBuilder, curBuilder metav1.Object) error {
	oldStatus, _, err := b.getMachineOSBuildStatusForBuilder(ctx, oldBuilder)
	if err != nil {
		// If we can't find the MachineOSConfig, MachineOSBuild, or any of the
		// ephemeral build objects, it means that it was probably deleted. Instead
		// of trying to reconcile the status, we'll return nil here to avoid
		// requeueing another attempt.
		return ignoreErrIsNotFound(fmt.Errorf("could not get status for old builder: %w", err))
	}

	curStatus, mosb, err := b.getMachineOSBuildStatusForBuilder(ctx, curBuilder)
	if err != nil {
		// If we can't find the MachineOSConfig, MachineOSBuild, or any of the
		// ephemeral build objects, it means that it was probably deleted. Instead
		// of trying to reconcile the status, we'll return nil here to avoid
		// requeueing another attempt.
		return ignoreErrIsNotFound(fmt.Errorf("could not get status for current builder: %w", err))
	}

	mosbCreation := mosb.GetCreationTimestamp()
	builderCreation := curBuilder.GetCreationTimestamp()

	// It is possible that the build pod can be newer than the MachineOSBuild.
	// This is the case whenever the MachineOSBuild is deleted, the underlying
	// build objects (pod, ephemeral build objects, etc.) get deleted and then
	// recreated while the MachineOBuild gets created as well.
	//
	// When this happens, the MachineOSBuild can go into the "interrupted" state
	// and would require intervention to delete and retry enough times for a new
	// build to start.
	//
	// A better solution for this would be to update the BuilderReference status
	// field on the MachineOSBuild to include the ID of the build pod so that we
	// can reconcile that more effectively. Alternatively, using a generated name
	// for the builder pod would also ensure that we don't have to wait for one
	// to be deleted before another can be created.
	if builderCreation.Before(&mosbCreation) && curBuilder.GetDeletionTimestamp() != nil {
		klog.Infof("Builder %q has deletion timestamp and is newer than MachineOSBuild %q, skipping update", curBuilder.GetName(), mosb.GetName())
		return nil
	}

	if err := b.setStatusOnMachineOSBuildIfNeeded(ctx, mosb, oldStatus, curStatus); err != nil {
		return fmt.Errorf("could not set status on MachineOSBuild %q: %w", mosb.Name, err)
	}

	return nil
}

// Sets the status on the MachineOSBuild object after comparing the statuses according to very specific state transitions.
func (b *buildReconciler) setStatusOnMachineOSBuildIfNeeded(ctx context.Context, mosb *mcfgv1alpha1.MachineOSBuild, oldStatus, curStatus mcfgv1alpha1.MachineOSBuildStatus) error {
	// Compare the old status and the current status to determine if an update is
	// needed. This is handled according to very specific state transitions.
	isUpdateNeeded, reason := isMachineOSBuildStatusUpdateNeeded(oldStatus, curStatus)
	if !isUpdateNeeded {
		if reason != "" {
			klog.Infof("MachineOSBuild %q %s; skipping update because of invalid transition", mosb.Name, reason)
		}

		return nil
	}

	klog.Infof("MachineOSBuild %q %s; update needed", mosb.Name, reason)

	mosb, err := b.getMachineOSBuildForUpdate(mosb)
	if err != nil {
		return err
	}

	bs := ctrlcommon.NewMachineOSBuildState(mosb)

	bs.SetBuildConditions(curStatus.Conditions)

	bs.Build.Status.FinalImagePushspec = curStatus.FinalImagePushspec

	if bs.Build.Status.BuildStart == nil && curStatus.BuildStart != nil {
		bs.Build.Status.BuildStart = curStatus.BuildStart
	}

	if bs.Build.Status.BuildEnd == nil && curStatus.BuildEnd != nil {
		bs.Build.Status.BuildEnd = curStatus.BuildEnd
	}

	bs.Build.Status.BuilderReference = curStatus.BuilderReference

	_, err = b.mcfgclient.MachineconfigurationV1alpha1().MachineOSBuilds().UpdateStatus(ctx, bs.Build, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("could not update status on MachineOSBuild %q: %w", mosb.Name, err)
	}

	klog.Infof("Updated status on MachineOSBuild %s", bs.Build.Name)
	return nil
}

// Gets the status from the running builder and applies it to the MachineOSBuild.
func (b *buildReconciler) updateMachineOSBuildWithStatus(ctx context.Context, obj metav1.Object) error {
	curStatus, mosb, err := b.getMachineOSBuildStatusForBuilder(ctx, obj)
	if err != nil {
		// If we can't find the MachineOSConfig, MachineOSBuild, or any of the
		// ephemeral build objects, it means that it was probably deleted. Instead
		// of trying to reconcile the status, we'll return nil here to avoid
		// requeueing another attempt.
		return ignoreErrIsNotFound(fmt.Errorf("could not update MachineOSBuild with status: %w", err))
	}

	// Compare the status returned from the builder to the status on the
	// MachineOSBuild object from the lister to determine if an update is needed
	// since we don't have an older build status to compare it to.
	if err := b.setStatusOnMachineOSBuildIfNeeded(ctx, mosb, mosb.Status, curStatus); err != nil {
		return fmt.Errorf("unable to set status on MachineOSBuild %q: %w", mosb.Name, err)
	}

	return nil
}

// Resolves the MachineOSBuild for a given builder.
func (b *buildReconciler) getMachineOSBuildForBuilder(builder buildrequest.Builder) (*mcfgv1alpha1.MachineOSBuild, error) {
	mosbName, err := builder.MachineOSBuild()
	if err != nil {
		return nil, fmt.Errorf("could not get MachineOSBuild name from builder %q: %w", builder.GetName(), err)
	}

	mosb, err := b.machineOSBuildLister.Get(mosbName)
	if err != nil {
		return nil, fmt.Errorf("could not get MachineOSBuild %s for builder %s: %w", mosbName, builder.GetObject().GetName(), err)
	}

	return mosb.DeepCopy(), nil
}

// Resolves both the MachineOSConfig and MachienOSBuild for a given Builder.
func (b *buildReconciler) getMachineOSConfigAndMachineOSBuildForBuilder(builder buildrequest.Builder) (*mcfgv1alpha1.MachineOSConfig, *mcfgv1alpha1.MachineOSBuild, error) {
	mosb, err := b.getMachineOSBuildForBuilder(builder)
	if err != nil {
		return nil, nil, err
	}

	mosc, err := b.getMachineOSConfigForBuilder(builder)
	if err != nil {
		return nil, nil, err
	}

	return mosc, mosb, nil
}

// Resolves the MachineOSConfig for a given builder.
func (b *buildReconciler) getMachineOSConfigForBuilder(builder buildrequest.Builder) (*mcfgv1alpha1.MachineOSConfig, error) {
	moscName, err := builder.MachineOSConfig()
	if err != nil {
		return nil, fmt.Errorf("could not get MachineOSConfig name from builder %q: %w", builder.GetName(), err)
	}

	mosc, err := b.machineOSConfigLister.Get(moscName)
	if err != nil {
		return nil, fmt.Errorf("could not get MachineOSConfig %q for builder %s: %w", moscName, builder.GetObject().GetName(), err)
	}

	return mosc.DeepCopy(), nil
}

// Deletes the underlying build objects for a given MachineOSBuild.
func (b *buildReconciler) deleteBuilderForMachineOSBuild(ctx context.Context, mosb *mcfgv1alpha1.MachineOSBuild) error {
	if err := imagebuilder.NewJobImageBuildCleaner(b.kubeclient, b.mcfgclient, b.tektonclient, mosb).Clean(ctx); err != nil {
		return fmt.Errorf("could not clean build %s: %w", mosb.Name, err)
	}

	return nil
}

// Deletes the MachineOSBuild.
func (b *buildReconciler) deleteMachineOSBuild(ctx context.Context, mosb *mcfgv1alpha1.MachineOSBuild) error {
	moscName, err := utils.GetRequiredLabelValueFromObject(mosb, constants.MachineOSConfigNameLabelKey)
	if err != nil {
		moscName = "<unknown MachineOSConfig>"
	}

	err = b.mcfgclient.MachineconfigurationV1alpha1().MachineOSBuilds().Delete(ctx, mosb.Name, metav1.DeleteOptions{})
	if err == nil {
		klog.Infof("Deleted MachineOSBuild %s for MachineOSConfig %s", mosb.Name, moscName)
		return nil
	}

	if k8serrors.IsNotFound(err) {
		klog.Infof("MachineOSBuild %s was not found for MachineOSConfig %s", mosb.Name, moscName)
		return nil
	}

	return fmt.Errorf("could not delete MachineOSBuild %s for MachineOSConfig %s: %w", mosb.Name, moscName, err)
}

// Finds and deletes any other running builds for a given MachineOSConfig.
func (b *buildReconciler) deleteOtherBuildsForMachineOSConfig(ctx context.Context, newMosb *mcfgv1alpha1.MachineOSBuild, mosc *mcfgv1alpha1.MachineOSConfig) error {
	mosbList, err := b.getMachineOSBuildsForMachineOSConfig(mosc)
	if err != nil {
		return fmt.Errorf("could not get MachineOSBuilds for MachineOSConfig %s: %w", mosc.Name, err)
	}

	for _, mosb := range mosbList {
		// Ignore the newly-created MachineOSBuild.
		if mosb.Name == newMosb.Name {
			continue
		}

		mosbState := ctrlcommon.NewMachineOSBuildState(mosb)

		// If the build is in any other state except for "success", delete it.
		if !mosbState.IsBuildSuccess() {
			klog.Infof("Found running MachineOSBuild %s for MachineOSConfig %s, deleting...", mosb.Name, mosc.Name)
			if err := b.deleteMachineOSBuild(ctx, mosb); err != nil {
				return fmt.Errorf("could not delete running MachineOSBuild %s: %w", mosb.Name, err)
			}
		}
	}

	return nil
}

// Gets a list of MachineOSBuilds for a given MachineOSConfig.
func (b *buildReconciler) getMachineOSBuildsForMachineOSConfig(mosc *mcfgv1alpha1.MachineOSConfig) ([]*mcfgv1alpha1.MachineOSBuild, error) {
	sel := utils.MachineOSBuildForPoolSelector(mosc)

	mosbList, err := b.machineOSBuildLister.List(sel)
	if err != nil {
		return nil, fmt.Errorf("could not get MachineOSBuilds for MachineOSConfig %s: %w", mosc.Name, err)
	}

	return mosbList, nil
}

// Times how long a given operation takes to complete.
func (b *buildReconciler) timeObjectOperation(obj kubeObject, op string, toRun func() error) error {
	start := time.Now()

	kind, err := utils.GetKindForObject(obj)
	if err != nil && kind == "" {
		kind = "<unknown object kind>"
	}

	detail := fmt.Sprintf("%s %s %q", op, kind, obj.GetName())

	klog.Info(detail)

	defer func() {
		klog.Infof("Finished %s %s %q after %s", strings.ToLower(op), kind, obj.GetName(), time.Since(start))
	}()

	if err := toRun(); err != nil {
		return fmt.Errorf("%s failed: %w", detail, err)
	}

	return nil
}

// Times how long a given sync operation takes.
func (b *buildReconciler) timeSyncOperation(name string, toRun func() error) error {
	start := time.Now()
	defer func() {
		klog.Infof("Finished syncing %s after %s", name, time.Since(start))
	}()

	klog.Infof("Syncing %s", name)

	if err := toRun(); err != nil {
		return fmt.Errorf("sync %s failed: %w", name, err)
	}

	return nil
}

// Syncs all MachineOSConfigs and MachineOSBuilds.
func (b *buildReconciler) syncAll(ctx context.Context) error {
	err := b.timeSyncOperation("MachineOSConfigs and MachineOSBuilds", func() error {
		if err := b.syncMachineOSConfigs(ctx); err != nil {
			return err
		}

		if err := b.syncMachineOSBuilds(ctx); err != nil {
			return err
		}

		return nil
	})

	if err != nil {
		return fmt.Errorf("could not sync all: %w", err)
	}

	return nil
}

// Syncs all existing MachineOSBuilds.
func (b *buildReconciler) syncMachineOSBuilds(ctx context.Context) error {
	err := b.timeSyncOperation("MachineOSBuilds", func() error {
		mosbs, err := b.machineOSBuildLister.List(labels.Everything())
		if err != nil {
			return err
		}

		for _, mosb := range mosbs {
			if err := b.syncMachineOSBuild(ctx, mosb); err != nil {
				return fmt.Errorf("could not sync MachineOSBuild %q: %w", mosb.Name, err)
			}
		}

		return nil
	})

	if err != nil {
		return fmt.Errorf("could not sync MachineOSBuilds: %w", err)
	}

	return nil
}

// Syncs a given MachineOSBuild. In this case, sync means that if the
// MachineOSBuild is not in a terminal or transient state and does not have a
// builder associated with it that one should be created.
func (b *buildReconciler) syncMachineOSBuild(ctx context.Context, mosb *mcfgv1alpha1.MachineOSBuild) error {
	return b.timeObjectOperation(mosb, syncingVerb, func() error {

		// It could be the case that the MCP the mosb in queue was targeting no longer is valid
		mcp, err := b.machineConfigPoolLister.Get(mosb.ObjectMeta.Labels[constants.TargetMachineConfigPoolLabelKey])
		if err != nil {
			return fmt.Errorf("could not get MachineConfigPool from MachineOSBuild %q: %w", mosb.Name, err)
		}

		// An mosb which had previously been forgotten by the queue and is no longer desired by the mcp should not build
		if mosb.ObjectMeta.Labels[constants.RenderedMachineConfigLabelKey] != mcp.Spec.Configuration.Name {
			klog.Infof("The MachineOSBuild %q which builds the rendered Machine Config %q is no longer desired by the MCP %q", mosb.Name, mosb.ObjectMeta.Labels[constants.RenderedMachineConfigLabelKey], mosb.ObjectMeta.Labels[constants.TargetMachineConfigPoolLabelKey])
			return nil
		}

		mosbState := ctrlcommon.NewMachineOSBuildState(mosb)

		if mosbState.IsInTerminalState() {
			return nil
		}

		if mosbState.IsInTransientState() {
			return nil
		}

		if mosbState.IsInInitialState() || !mosbState.HasBuildConditions() {
			mosc, err := utils.GetMachineOSConfigForMachineOSBuild(mosb, b.utilListers())
			if err != nil {
				// It is possible that the MachineOSConfig could be deleted by the time
				// we get here. If that is the case, we should ignore any not found
				// errors here.
				return ignoreErrIsNotFound(fmt.Errorf("could not sync MachineOSBuild %q: %w", mosb.Name, err))
			}

			if mosc.Spec.BuildInputs.ImageBuilder.ImageBuilderType == mcfgv1alpha1.PipelineBuilder {
				// Check and install pipeline
				err := checkAndInstallPipeline(ctx, b.kubeclient, b.pipelineoperatorclient, b.olmclient, b.tektonclient)
				if err != nil {
					return fmt.Errorf("error checking pipeline exists and installing: %v", err)
				}
			}


			observer := imagebuilder.NewJobImageBuildObserver(b.kubeclient, b.mcfgclient, b.tektonclient, mosb, mosc)

			exists, err := observer.Exists(ctx)
			if err != nil {
				return fmt.Errorf("could not determine if builder exists for MachineOSBuild %q: %w", mosb.Name, err)
			}

			if exists {
				return nil
			}

			if err := b.startBuild(ctx, mosb); err != nil {
				return fmt.Errorf("could not start build for MachineOSBuild %q: %w", mosb.Name, err)
			}

			klog.Infof("Started new build for MachineOSBuild %q", mosb.Name)
		}

		return nil
	})
}

// Syncs all existing MachineOSConfigs.
func (b *buildReconciler) syncMachineOSConfigs(ctx context.Context) error {
	err := b.timeSyncOperation("MachineOSConfigs", func() error {
		moscs, err := b.machineOSConfigLister.List(labels.Everything())
		if err != nil {
			return err
		}

		for _, mosc := range moscs {
			if err := b.syncMachineOSConfig(ctx, mosc); err != nil {
				return fmt.Errorf("could not sync MachineOSConfig %q: %w", mosc.Name, err)
			}
		}

		return nil
	})

	if err != nil {
		return fmt.Errorf("could not sync MachineOSConfigs: %w", err)
	}

	return nil
}

// Syncs a given MachineOSConfig. In this case, sync means that if the
// MachineOSConfig does not have any MachineOSBuilds associated with it or the
// one it thinks is its current build does not exist, then a new MachineOSBuild
// should be created.
func (b *buildReconciler) syncMachineOSConfig(ctx context.Context, mosc *mcfgv1alpha1.MachineOSConfig) error {
	return b.timeObjectOperation(mosc, syncingVerb, func() error {
		mosbs, err := b.getMachineOSBuildsForMachineOSConfig(mosc)
		if err != nil {
			return fmt.Errorf("could not list MachineOSBuilds for MachineOSConfig %q: %w", mosc.Name, err)
		}

		if mosc.Spec.BuildInputs.ImageBuilder.ImageBuilderType == mcfgv1alpha1.PipelineBuilder {
			// Check and install pipeline
			err := checkAndInstallPipeline(ctx, b.kubeclient, b.pipelineoperatorclient, b.olmclient, b.tektonclient)
			if err != nil {
				return fmt.Errorf("error checking pipeline exists and installing: %v", err)
			}
		}


		klog.V(4).Infof("MachineOSConfig %q is associated with %d MachineOSBuilds %v", mosc.Name, len(mosbs), getMachineOSBuildNames(mosbs))

		for _, mosb := range mosbs {
			// If we found the currently-associated MachineOSBuild for this
			// MachineOSConfig, we're done. We prefer ones with the full image pullspec.
			if isMachineOSBuildCurrentForMachineOSConfigWithPullspec(mosc, mosb) {
				klog.Infof("MachineOSConfig %q has current build annotation and current image pullspec %q for MachineOSBuild %q", mosc.Name, mosc.Status.CurrentImagePullspec, mosb.Name)
				return nil
			}
		}

		for _, mosb := range mosbs {
			// If we didn't find one with the current pullspec set but we did find
			// one matching our current annotation, we'll use that one instead.
			if isMachineOSBuildCurrentForMachineOSConfig(mosc, mosb) {
				klog.Infof("MachineOSConfig %q has current build annotation for MachineOSBuild %q", mosc.Name, mosb.Name)
				return nil
			}
		}

		klog.Infof("No matching MachineOSBuild found for MachineOSConfig %q, will create one", mosc.Name)
		if err := b.createNewMachineOSBuildOrReuseExisting(ctx, mosc); err != nil {
			return fmt.Errorf("could not create new or reuse existing MachineOSBuild for MachineOSConfig %q: %w", mosc.Name, err)
		}

		return nil
	})
}

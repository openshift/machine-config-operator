package build

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/containers/image/v5/types"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	imagev1clientset "github.com/openshift/client-go/image/clientset/versioned"
	mcfgclientset "github.com/openshift/client-go/machineconfiguration/clientset/versioned"
	routeclientset "github.com/openshift/client-go/route/clientset/versioned"
	"github.com/openshift/machine-config-operator/pkg/apihelpers"
	"github.com/openshift/machine-config-operator/pkg/controller/build/buildrequest"
	"github.com/openshift/machine-config-operator/pkg/controller/build/constants"
	"github.com/openshift/machine-config-operator/pkg/controller/build/imagebuilder"
	"github.com/openshift/machine-config-operator/pkg/controller/build/imagepruner"
	"github.com/openshift/machine-config-operator/pkg/controller/build/utils"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	daemonconstants "github.com/openshift/machine-config-operator/pkg/daemon/constants"
	"github.com/openshift/machine-config-operator/pkg/helpers"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/util/retry"
	"k8s.io/klog/v2"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	addingVerb   string = "Adding"
	updatingVerb string = "Updating"
	deletingVerb string = "Deleting"
	syncingVerb  string = "Syncing"
	certsDir     string = "/etc/docker/certs.d"
)

type reconciler interface {
	AddMachineOSBuild(context.Context, *mcfgv1.MachineOSBuild) error
	UpdateMachineOSBuild(context.Context, *mcfgv1.MachineOSBuild, *mcfgv1.MachineOSBuild) error
	DeleteMachineOSBuild(context.Context, *mcfgv1.MachineOSBuild) error

	AddMachineOSConfig(context.Context, *mcfgv1.MachineOSConfig) error
	UpdateMachineOSConfig(context.Context, *mcfgv1.MachineOSConfig, *mcfgv1.MachineOSConfig) error
	DeleteMachineOSConfig(context.Context, *mcfgv1.MachineOSConfig) error

	AddJob(context.Context, *batchv1.Job) error
	UpdateJob(context.Context, *batchv1.Job, *batchv1.Job) error
	DeleteJob(context.Context, *batchv1.Job) error

	AddMachineConfigPool(context.Context, *mcfgv1.MachineConfigPool) error
	UpdateMachineConfigPool(context.Context, *mcfgv1.MachineConfigPool, *mcfgv1.MachineConfigPool) error
}

// Holds the implementation of the buildReconciler. The buildReconciler's job
// is to respond to incoming events in a specific way. By doing this, the
// reconciliation process has a clear entrypoint for each incoming event.
type buildReconciler struct {
	mcfgclient  mcfgclientset.Interface
	kubeclient  clientset.Interface
	imageclient imagev1clientset.Interface
	routeclient routeclientset.Interface
	imagepruner imagepruner.ImagePruner
	*listers
}

// Instantiates a new reconciler instance. This returns an interface to
// disallow access to its private methods.
func newBuildReconciler(mcfgclient mcfgclientset.Interface, kubeclient clientset.Interface, imageclient imagev1clientset.Interface, routeclient routeclientset.Interface, l *listers, imagepruner imagepruner.ImagePruner) reconciler {
	return newBuildReconcilerAsStruct(mcfgclient, kubeclient, imageclient, routeclient, l, imagepruner)
}

func newBuildReconcilerAsStruct(mcfgclient mcfgclientset.Interface, kubeclient clientset.Interface, imageclient imagev1clientset.Interface, routeclient routeclientset.Interface, l *listers, imagepruner imagepruner.ImagePruner) *buildReconciler {
	return &buildReconciler{
		mcfgclient:  mcfgclient,
		kubeclient:  kubeclient,
		imageclient: imageclient,
		routeclient: routeclient,
		imagepruner: imagepruner,
		listers:     l,
	}
}

// Executes whenever a new MachineOSConfig is added.
func (b *buildReconciler) AddMachineOSConfig(ctx context.Context, mosc *mcfgv1.MachineOSConfig) error {
	return b.timeObjectOperation(mosc, addingVerb, func() error {
		if err := b.addMachineOSConfig(ctx, mosc); err != nil {
			return err
		}

		return b.syncMachineOSConfigs(ctx)
	})
}

// Executes whenever an existing MachineOSConfig is updated.
func (b *buildReconciler) UpdateMachineOSConfig(ctx context.Context, old, cur *mcfgv1.MachineOSConfig) error {
	return b.timeObjectOperation(cur, updatingVerb, func() error {
		return b.updateMachineOSConfig(ctx, old, cur)
	})
}

// Executes whenever a MachineOSConfig is updated. If the build inputs have
// changed, a new MachineOSBuild should be created.
func (b *buildReconciler) updateMachineOSConfig(ctx context.Context, old, cur *mcfgv1.MachineOSConfig) error {
	// If we have gained the rebuild annotation, we should delete the current MachineOSBuild associated with this MachineOSConfig.
	if !hasRebuildAnnotation(old) && hasRebuildAnnotation(cur) {
		if err := b.rebuildMachineOSConfig(ctx, cur); err != nil {
			return fmt.Errorf("could not rebuild MachineOSConfig %q: %w", cur.Name, err)
		}

		return nil
	}

	// Whenever the MachineOSConfig spec has changed, create a new MachineOSBuild.
	if !equality.Semantic.DeepEqual(old.Spec, cur.Spec) {
		klog.Infof("Detected MachineOSConfig change for %s", cur.Name)
		return b.createNewMachineOSBuildOrReuseExisting(ctx, cur, false)
	}

	return b.syncMachineOSConfigs(ctx)
}

// Rebuilds the most current build associated with a MachineOSConfig whenever
// the rebuild annotation is applied. This is done by deleting the current
// MachineOSBuild and allowing the controller to replace it with a new one.
func (b *buildReconciler) rebuildMachineOSConfig(ctx context.Context, mosc *mcfgv1.MachineOSConfig) error {
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

	if err := b.createNewMachineOSBuildOrReuseExisting(ctx, mosc, true); err != nil {
		return fmt.Errorf("could not create new MachineOSBuild for MachineOSConfig %q: %w", mosc.Name, err)
	}

	klog.Infof("MachineOSConfig %q is now rebuilding", mosc.Name)

	return nil
}

// Runs whenever a new MachineOSConfig is added. Determines if a new
// MachineOSBuild should be created and then creates it, if needed.
func (b *buildReconciler) addMachineOSConfig(ctx context.Context, mosc *mcfgv1.MachineOSConfig) error {
	// Check for pre-built image seeding annotation - only seed if not already seeded
	if preBuiltImage, hasImage := getPreBuiltImage(mosc); hasImage {
		if shouldSeedWithPreBuiltImage(mosc) {
			klog.Infof("MachineOSConfig %q has pre-built image annotation, attempting to seed with image %q", mosc.Name, preBuiltImage)
			return b.seedMachineOSConfigWithExistingImage(ctx, mosc, preBuiltImage)
		}
		// If we get here, either already seeded or has current build - proceed normally
		klog.V(4).Infof("MachineOSConfig %q has pre-built image annotation but seeding already complete, proceeding with normal processing", mosc.Name)
	}

	// Existing logic for normal MachineOSConfig processing
	return b.syncMachineOSConfig(ctx, mosc)
}

// Executes whenever a MachineOSConfig is deleted. This deletes all
// MachineOSBuilds (and the underlying associated build objects).
func (b *buildReconciler) DeleteMachineOSConfig(ctx context.Context, mosc *mcfgv1.MachineOSConfig) error {
	return b.timeObjectOperation(mosc, deletingVerb, func() error {
		return b.deleteMachineOSConfig(ctx, mosc)
	})
}

// Performs the deletion reconciliation of the MachineOSConfig.
func (b *buildReconciler) deleteMachineOSConfig(ctx context.Context, mosc *mcfgv1.MachineOSConfig) error {
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

		if err := b.updateMachineOSBuildWithStatus(ctx, job); err != nil {
			return fmt.Errorf("could not update job status for %q: %w", job.Name, err)
		}

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
func (b *buildReconciler) AddMachineOSBuild(ctx context.Context, mosb *mcfgv1.MachineOSBuild) error {
	return b.timeObjectOperation(mosb, addingVerb, func() error {
		return b.addMachineOSBuild(ctx, mosb)
	})
}

// Executes whenever a MachineOSBuild is updated.
func (b *buildReconciler) UpdateMachineOSBuild(ctx context.Context, old, cur *mcfgv1.MachineOSBuild) error {
	return b.timeObjectOperation(cur, updatingVerb, func() error {
		if err := b.updateMachineOSBuild(ctx, old, cur); err != nil {
			return fmt.Errorf("could not update MachineOSBuild: %w", err)
		}

		return b.syncMachineOSBuilds(ctx)
	})
}

// Performs the reconciliation whenever the MachineOSBuild is updated, such as
// cleaning up the build artifacts upon success.
func (b *buildReconciler) updateMachineOSBuild(ctx context.Context, old, current *mcfgv1.MachineOSBuild) error {
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

		// Always update ImageBuildDegraded condition based on current active build status
		return b.updateImageBuildDegradedCondition(ctx, mcp, mosc)
	}

	// If the build was successful, clean up the build objects and propagate the
	// final image pushspec onto the MachineOSConfig object.
	// Also update BuildDegraded condition based on current active build status
	if !oldState.IsBuildSuccess() && curState.IsBuildSuccess() {
		klog.Infof("MachineOSBuild %s succeeded, cleaning up all ephemeral objects used for the build", current.Name)

		mcp, err := b.machineConfigPoolLister.Get(mosc.Spec.MachineConfigPool.Name)
		if err != nil {
			return fmt.Errorf("could not get MachineConfigPool from MachineOSConfig %q: %w", mosc.Name, err)
		}

		// Update BuildDegraded condition based on current active build status
		if err := b.updateImageBuildDegradedCondition(ctx, mcp, mosc); err != nil {
			klog.Errorf("Failed to update ImageBuildDegraded condition for pool %s: %v", mcp.Name, err)
		}

		// Clean up ephemeral objects
		if err := imagebuilder.NewJobImageBuilder(b.kubeclient, b.mcfgclient, current, mosc).Clean(ctx); err != nil {
			return err
		}

		if err := b.updateMachineOSConfigStatus(ctx, mosc, current); err != nil {
			return fmt.Errorf("could not update MachineOSConfig %q status for successful MachineOSBuild %q: %w", mosc.Name, current.Name, err)
		}
	}

	return nil
}

// Updates the status on the MachineOSConfig object from the supplied MachineOSBuild object.
func (b *buildReconciler) updateMachineOSConfigStatus(ctx context.Context, mosc *mcfgv1.MachineOSConfig, mosb *mcfgv1.MachineOSBuild) error {
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
		updatedMosc, err := b.mcfgclient.MachineconfigurationV1().MachineOSConfigs().Update(ctx, mosc, metav1.UpdateOptions{})
		if err != nil {
			return fmt.Errorf("could not update annotations on MachineOSConfig %q: %w", mosc.Name, err)
		}

		klog.Infof("Updated annotations on MachineOSConfig %q", mosc.Name)

		mosc = updatedMosc

		// When annotations are updated (new build starts), also update observedGeneration
		// to signal that the controller is processing the current generation
		if mosc.Status.ObservedGeneration != mosc.GetGeneration() {
			mosc.Status.ObservedGeneration = mosc.GetGeneration()
			_, err = b.mcfgclient.MachineconfigurationV1().MachineOSConfigs().UpdateStatus(ctx, mosc, metav1.UpdateOptions{})
			if err != nil {
				klog.Errorf("Failed to update observedGeneration on MachineOSConfig %q: %v", mosc.Name, err)
			} else {
				klog.Infof("Updated observedGeneration on MachineOSConfig %q to %d", mosc.Name, mosc.GetGeneration())
			}
		}
	}

	// Skip the status update if digest image pushspec hasn't been set yet.
	if mosb.Status.DigestedImagePushSpec == "" {
		klog.Infof("MachineOSBuild %q has empty final image pushspec, skipping MachineOSConfig %q status update", mosb.Name, mosc.Name)
		return nil
	}

	// Check if the machineOSBuild reference matches
	machineOSBuildRefMatches := mosc.Status.MachineOSBuild != nil && mosc.Status.MachineOSBuild.Name == mosb.Name

	// skip the status update if both the current image pullspec and machineOSBuild reference are already correct.
	if mosc.Status.CurrentImagePullSpec == mosb.Status.DigestedImagePushSpec && machineOSBuildRefMatches {
		klog.Infof("MachineOSConfig %q already has final image pushspec and machineOSBuild reference for MachineOSBuild %q", mosc.Name, mosb.Name)
		return nil
	}

	mosc.Status.CurrentImagePullSpec = mosb.Status.DigestedImagePushSpec
	mosc.Status.ObservedGeneration = mosc.GetGeneration()
	mosc.Status.MachineOSBuild = &mcfgv1.ObjectReference{
		Name:     mosb.Name,
		Group:    mcfgv1.SchemeGroupVersion.Group,
		Resource: "machineosbuilds",
	}

	_, err = b.mcfgclient.MachineconfigurationV1().MachineOSConfigs().UpdateStatus(ctx, mosc, metav1.UpdateOptions{})
	if err == nil {
		klog.Infof("Updated status on MachineOSConfig %s", mosc.Name)
	}

	return err
}

// Executes whenever a MachineOSBuild is deleted by cleaning up any remaining build artifacts that may be left behind.
func (b *buildReconciler) DeleteMachineOSBuild(ctx context.Context, mosb *mcfgv1.MachineOSBuild) error {
	return b.timeObjectOperation(mosb, deletingVerb, func() error {
		return b.deleteBuilderForMachineOSBuild(ctx, mosb)
	})
}

// Executes whenever a MachineConfigPool is added.
func (b *buildReconciler) AddMachineConfigPool(ctx context.Context, mcp *mcfgv1.MachineConfigPool) error {
	return b.timeObjectOperation(mcp, addingVerb, func() error {
		return b.syncMachineConfigPools(ctx)
	})
}

// Executes whenever a MachineConfigPool is updated.
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
		if err := b.reconcilePoolChange(ctx, curMCP); err != nil {
			return fmt.Errorf("could not create or reuse existing MachineOSBuild for MachineConfigPool %q change: %w", curMCP.Name, err)
		}
	}

	return b.syncAll(ctx)
}

// Adds a MachineOSBuild.
func (b *buildReconciler) addMachineOSBuild(ctx context.Context, mosb *mcfgv1.MachineOSBuild) error {
	return b.syncMachineOSBuild(ctx, mosb)
}

// Starts executing a build for a given MachineOSBuild.
func (b *buildReconciler) startBuild(ctx context.Context, mosb *mcfgv1.MachineOSBuild) error {
	mosc, err := utils.GetMachineOSConfigForMachineOSBuild(mosb, b.utilListers())
	if err != nil {
		return err
	}

	// If there are any other in-progress builds for this MachineOSConfig, stop them first.
	if err := b.deleteOtherBuildsForMachineOSConfig(ctx, mosb, mosc); err != nil {
		return fmt.Errorf("could not delete other non-terminal MachineOSBuilds for MachineOSConfig %s: %w", mosc.Name, err)
	}

	// Next, create our new MachineOSBuild.
	if err := imagebuilder.NewJobImageBuilder(b.kubeclient, b.mcfgclient, mosb, mosc).Start(ctx); err != nil {
		return fmt.Errorf("imagebuilder could not start build for MachineOSBuild %q: %w", mosb.Name, err)
	}

	klog.Infof("Started new build %s for MachineOSBuild", utils.GetBuildJobName(mosb))

	if err := b.updateMachineOSConfigStatus(ctx, mosc, mosb); err != nil {
		return fmt.Errorf("could not update MachineOSConfig %q status for MachineOSBuild %q: %w", mosc.Name, mosb.Name, err)
	}

	// Update BuildDegraded condition based on current active build status when build starts
	mcp, err := b.machineConfigPoolLister.Get(mosc.Spec.MachineConfigPool.Name)
	if err != nil {
		return fmt.Errorf("could not get MachineConfigPool from MachineOSConfig %q: %w", mosc.Name, err)
	}

	if err := b.updateImageBuildDegradedCondition(ctx, mcp, mosc); err != nil {
		klog.Errorf("Failed to update ImageBuildDegraded condition for pool %s: %v", mcp.Name, err)
	}

	return nil
}

// Retrieves a deep-copy of the MachineOSConfig from the lister so that the cache is not mutated during the update.
func (b *buildReconciler) getMachineOSConfigForUpdate(mosc *mcfgv1.MachineOSConfig) (*mcfgv1.MachineOSConfig, error) {
	out, err := b.machineOSConfigLister.Get(mosc.Name)

	if err != nil {
		return nil, err
	}

	return out.DeepCopy(), nil
}

// Retrieves a deep-copy of the MachineOSBuild from the lister so that the cache is not mutated during the update.
func (b *buildReconciler) getMachineOSBuildForUpdate(mosb *mcfgv1.MachineOSBuild) (*mcfgv1.MachineOSBuild, error) {
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

	if err := b.createNewMachineOSBuildOrReuseExisting(ctx, mosc.DeepCopy(), false); err != nil {
		return fmt.Errorf("could not create MachineOSBuild for MachineConfigPool %q change: %w", mcp.Name, err)
	}

	return nil
}

// Executes whenever a MachineOSConfig has the rebuild annotation and a new MachineOSBuild needs to be created.
func (b *buildReconciler) createNewMachineOSBuildForRebuild(ctx context.Context, mosb *mcfgv1.MachineOSBuild, moscName string) error {
	// Verify that the MOSB is actually deleted before we try to create a new one
	// The deletion process may take some time and if we try to create a new MOSB with the same name, a clash may happen

	// First delete any existing MOSB with exactly this name
	if err := b.mcfgclient.MachineconfigurationV1().
		MachineOSBuilds().
		Delete(ctx, mosb.Name, metav1.DeleteOptions{}); err != nil && !k8serrors.IsNotFound(err) {
		return fmt.Errorf("could not delete existing MOSB %q: %w", mosb.Name, err)
	}

	childCtx, cancel := context.WithTimeout(ctx, time.Second*90)
	defer cancel()
	for {
		_, err := b.mcfgclient.MachineconfigurationV1().MachineOSBuilds().Get(childCtx, mosb.Name, metav1.GetOptions{})
		if err != nil && !k8serrors.IsNotFound(err) {
			return fmt.Errorf("could not check if MachineOSBuild %s exists: %w", mosb.Name, err)
		}
		if k8serrors.IsNotFound(err) {
			break
		}
		time.Sleep(1 * time.Second)
	}

	// Delete the digest configmap if it exists
	// This is created by the wait-for-done container once the image has been built and pushed
	// and stays around when the build is successful
	err := b.kubeclient.CoreV1().ConfigMaps(ctrlcommon.MCONamespace).Delete(ctx, utils.GetDigestConfigMapName(mosb), metav1.DeleteOptions{})
	if err != nil && !k8serrors.IsNotFound(err) {
		return fmt.Errorf("could not delete digest configmap for MachineOSBuild %s: %w", mosb.Name, err)
	}

	// Now create the new MOSB
	_, err = b.mcfgclient.MachineconfigurationV1().MachineOSBuilds().Create(ctx, mosb, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("could not create new MachineOSBuild from rebuild annotation for MachineOSConfig %q: %w", moscName, err)
	}
	klog.Infof("New MachineOSBuild created: %s", mosb.Name)
	return nil
}

// Executes whenever a new MachineOSBuild is created.
func (b *buildReconciler) createNewMachineOSBuildOrReuseExisting(ctx context.Context, mosc *mcfgv1.MachineOSConfig, isRebuild bool) error {
	mcp, err := b.machineConfigPoolLister.Get(mosc.Spec.MachineConfigPool.Name)
	if err != nil {
		return fmt.Errorf("could not get MachineConfigPool %s for MachineOSConfig %s: %w", mosc.Spec.MachineConfigPool.Name, mosc.Name, err)
	}

	// Allow builds to retry when pool is degraded only due to BuildDegraded,
	// but prevent builds for other types of degradation (NodeDegraded, RenderDegraded)
	if b.shouldPreventBuildDueToDegradation(mcp) {
		return fmt.Errorf("MachineConfigPool %s is degraded due to non-build issues", mcp.Name)
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

	// Set owner reference of the machineOSBuild to the machineOSConfig that created this
	oref := metav1.NewControllerRef(mosc, mcfgv1.SchemeGroupVersion.WithKind("MachineOSConfig"))
	mosb.SetOwnerReferences([]metav1.OwnerReference{*oref})

	existingMosb, err := b.machineOSBuildLister.Get(mosb.Name)
	if err != nil && !k8serrors.IsNotFound(err) {
		return fmt.Errorf("could not get MachineOSBuild: %w", err)
	}

	if isRebuild {
		return b.createNewMachineOSBuildForRebuild(ctx, mosb, mosc.Name)
	}

	// If err is nil, it means a MachineOSBuild with this name already exists.
	// What likely happened is that a config change was rolled back to the
	// previous state. Rather than performing another build, we should get the
	// previously built image pullspec and adjust the MachineOSConfig to use that
	// image instead.
	if err == nil && existingMosb != nil {
		imageNeedsRebuild, err := b.reuseExistingMachineOSBuildIfPossible(ctx, mosc, existingMosb)
		if err != nil {
			return fmt.Errorf("could not reuse existing MachineOSBuild %q for MachineOSConfig %q: %w", existingMosb.Name, mosc.Name, err)
		}

		// If we need to rebuild, then we need to create a new MachineOSBuild
		if imageNeedsRebuild {
			if err := b.mcfgclient.MachineconfigurationV1().MachineOSBuilds().Delete(ctx, existingMosb.Name, metav1.DeleteOptions{}); err != nil && !k8serrors.IsNotFound(err) {
				return fmt.Errorf("could not delete existing MOSB %q: %w", existingMosb.Name, err)
			}

			return b.createNewMachineOSBuildForRebuild(ctx, mosb, mosc.Name)
		}
		// If we did not need to rebuild, then we can reuse the existing MOSB and we are done
		return nil
	}

	// In this situation, we've determined that the MachineOSBuild does not
	// exist, so we need to create it.
	if k8serrors.IsNotFound(err) {
		mosb, err := b.mcfgclient.MachineconfigurationV1().MachineOSBuilds().Create(ctx, mosb, metav1.CreateOptions{})
		if err != nil && !k8serrors.IsAlreadyExists(err) {
			return fmt.Errorf("could not create new MachineOSBuild %q: %w", mosb.Name, err)
		}
		klog.Infof("New MachineOSBuild created: %s", mosb.Name)
	}

	return nil
}

// Determines if a preexising MachineOSBuild can be reused and if possible, does it.
func (b *buildReconciler) reuseExistingMachineOSBuildIfPossible(ctx context.Context, mosc *mcfgv1.MachineOSConfig, existingMosb *mcfgv1.MachineOSBuild) (bool, error) {
	existingMosbState := ctrlcommon.NewMachineOSBuildState(existingMosb)

	canBeReused := false
	imageNeedsRebuild := false
	// If the existing build is a success and has the image pushspec set, it can be reused.
	if existingMosbState.IsBuildSuccess() && existingMosb.Status.DigestedImagePushSpec != "" {
		klog.Infof("Existing MachineOSBuild %q found, checking if image %q still exists", existingMosb.Name, existingMosb.Status.DigestedImagePushSpec)

		image := string(existingMosb.Spec.RenderedImagePushSpec)
		inspect, err := b.inspectImage(ctx, image, existingMosb)
		if inspect != nil && err == nil {
			klog.Infof("Existing MachineOSBuild %q found, reusing image %q by assigning to MachineOSConfig %q", existingMosb.Name, image, mosc.Name)
			canBeReused = true
		} else {
			klog.Infof("Existing MachineOSBuild image %q no longer exists, skipping reuse. Got error: %v", image, err)
			imageNeedsRebuild = true

			// Delete the MOSB so that we can rebuild since the image associated with it doesn't exist anymore
			klog.Infof("Deleting MachineOSBuild %q so we can rebuild it to create a new image", existingMosb.Name)
			err := b.mcfgclient.MachineconfigurationV1().MachineOSBuilds().Delete(ctx, existingMosb.Name, metav1.DeleteOptions{})
			if err != nil && !k8serrors.IsNotFound(err) {
				return imageNeedsRebuild, fmt.Errorf("could not delete MachineOSBuild %q: %w", existingMosb.Name, err)
			}
			return imageNeedsRebuild, nil
		}
	}

	// If the existing build is in a transient state, it can be reused.
	if existingMosbState.IsInTransientState() {
		klog.Infof("Existing MachineOSBuild %q found in transient state, assigning to MachineOSConfig %q", existingMosb.Name, mosc.Name)
		canBeReused = true
	}

	if canBeReused {
		// Stop any other running builds.
		if err := b.deleteOtherBuildsForMachineOSConfig(ctx, existingMosb, mosc); err != nil {
			return canBeReused, fmt.Errorf("could not delete running builds for MachineOSConfig %q after reusing existing MachineOSBuild %q: %w", mosc.Name, existingMosb.Name, err)
		}

		// Update the MachineOSConfig to use the preexisting MachineOSBuild.
		if err := b.updateMachineOSConfigStatus(ctx, mosc, existingMosb); err != nil {
			return canBeReused, fmt.Errorf("could not update MachineOSConfig %q status to reuse preexisting MachineOSBuild %q: %w", mosc.Name, existingMosb.Name, err)
		}
	}

	return imageNeedsRebuild, nil
}

// Gets the MachineOSBuild status from the provided metav1.Object which can be
// converted into a Builder.
func (b *buildReconciler) getMachineOSBuildStatusForBuilder(ctx context.Context, obj metav1.Object) (mcfgv1.MachineOSBuildStatus, *mcfgv1.MachineOSBuild, error) {
	builder, err := buildrequest.NewBuilder(obj)
	if err != nil {
		return mcfgv1.MachineOSBuildStatus{}, nil, fmt.Errorf("could not instantiate builder: %w", err)
	}

	mosc, mosb, err := b.getMachineOSConfigAndMachineOSBuildForBuilder(builder)
	if err != nil {
		return mcfgv1.MachineOSBuildStatus{}, nil, fmt.Errorf("could not get MachineOSConfig or MachineOSBuild for builder: %w", err)
	}

	observer := imagebuilder.NewJobImageBuildObserverFromBuilder(b.kubeclient, b.mcfgclient, mosb, mosc, builder)

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
func (b *buildReconciler) setStatusOnMachineOSBuildIfNeeded(ctx context.Context, mosb *mcfgv1.MachineOSBuild, oldStatus, curStatus mcfgv1.MachineOSBuildStatus) error {
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

	bs.Build.Status.DigestedImagePushSpec = curStatus.DigestedImagePushSpec

	if bs.Build.Status.BuildStart == nil && curStatus.BuildStart != nil {
		bs.Build.Status.BuildStart = curStatus.BuildStart
	}

	if bs.Build.Status.BuildEnd == nil && curStatus.BuildEnd != nil {
		bs.Build.Status.BuildEnd = curStatus.BuildEnd
	}

	bs.Build.Status.Builder = curStatus.Builder

	_, err = b.mcfgclient.MachineconfigurationV1().MachineOSBuilds().UpdateStatus(ctx, bs.Build, metav1.UpdateOptions{})
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
func (b *buildReconciler) getMachineOSBuildForBuilder(builder buildrequest.Builder) (*mcfgv1.MachineOSBuild, error) {
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
func (b *buildReconciler) getMachineOSConfigAndMachineOSBuildForBuilder(builder buildrequest.Builder) (*mcfgv1.MachineOSConfig, *mcfgv1.MachineOSBuild, error) {
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
func (b *buildReconciler) getMachineOSConfigForBuilder(builder buildrequest.Builder) (*mcfgv1.MachineOSConfig, error) {
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
func (b *buildReconciler) deleteBuilderForMachineOSBuild(ctx context.Context, mosb *mcfgv1.MachineOSBuild) error {
	if err := imagebuilder.NewJobImageBuildCleaner(b.kubeclient, b.mcfgclient, mosb).Clean(ctx); err != nil {
		return fmt.Errorf("could not clean build %s: %w", mosb.Name, err)
	}
	// Delete the image associated with the MOSB first
	moscName, err := utils.GetRequiredLabelValueFromObject(mosb, constants.MachineOSConfigNameLabelKey)
	if err != nil {
		klog.Warningf("could not get MachineOSConfig name for MachineOSBuild %s: %v, cannot delete image", mosb.Name, err)
		return nil
	}
	if err := b.deleteMOSBImage(ctx, mosb, moscName); err != nil {
		return err
	}
	// Delete the digest configmap if it exists
	// This is created by the wait-for-done container once the image has been built and pushed
	// and stays around when the build is successful
	err = b.kubeclient.CoreV1().ConfigMaps(ctrlcommon.MCONamespace).Delete(ctx, utils.GetDigestConfigMapName(mosb), metav1.DeleteOptions{})
	if err != nil && !k8serrors.IsNotFound(err) {
		return fmt.Errorf("could not delete digest configmap for MachineOSBuild %s for MachineOSConfig %s: %w", mosb.Name, moscName, err)
	}
	return nil
}

// Deletes the MachineOSBuild.
func (b *buildReconciler) deleteMachineOSBuild(ctx context.Context, mosb *mcfgv1.MachineOSBuild) error {
	moscName, err := utils.GetRequiredLabelValueFromObject(mosb, constants.MachineOSConfigNameLabelKey)
	if err != nil {
		moscName = "<unknown MachineOSConfig>"
	}
	// Delete the image associated with the MOSB first
	if err := b.deleteMOSBImage(ctx, mosb, moscName); err != nil {
		return err
	}

	// Delete the digest configmap if it exists
	// This is created by the wait-for-done container once the image has been built and pushed
	// and stays around when the build is successful
	err = b.kubeclient.CoreV1().ConfigMaps(ctrlcommon.MCONamespace).Delete(ctx, utils.GetDigestConfigMapName(mosb), metav1.DeleteOptions{})
	if err != nil && !k8serrors.IsNotFound(err) {
		return fmt.Errorf("could not delete digest configmap for MachineOSBuild %s for MachineOSConfig %s: %w", mosb.Name, moscName, err)
	}

	err = b.mcfgclient.MachineconfigurationV1().MachineOSBuilds().Delete(ctx, mosb.Name, metav1.DeleteOptions{})
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

func (b *buildReconciler) deleteMOSBImage(ctx context.Context, mosb *mcfgv1.MachineOSBuild, moscName string) error {
	moscExists := true
	_, err := b.listers.machineOSConfigLister.Get(moscName)
	if k8serrors.IsNotFound(err) {
		moscExists = false
	} else if err != nil {
		return fmt.Errorf("could not get MachineOSConfig for MachineOSBuild %q: %w", mosb.Name, err)
	}

	if moscExists {
		pool, err := b.listers.machineConfigPoolLister.Get(mosb.ObjectMeta.Labels[constants.TargetMachineConfigPoolLabelKey])
		if err != nil {
			return fmt.Errorf("could not get MachineConfigPool from MachineOSBuild %q: %w", mosb.Name, err)
		}

		nodes, err := helpers.GetNodesForPool(b.listers.machineConfigPoolLister, b.listers.nodeLister, pool)
		if err != nil {
			return fmt.Errorf("could not get nodes for MachineConfigPool %q: %w", pool.Name, err)
		}

		for _, node := range nodes {
			if node.GetAnnotations()[daemonconstants.CurrentImageAnnotationKey] == string(mosb.Status.DigestedImagePushSpec) ||
				node.GetAnnotations()[daemonconstants.DesiredImageAnnotationKey] == string(mosb.Status.DigestedImagePushSpec) {
				// the image we are trying to delete is currently on a node or desired by a node
				klog.Warningf("Image %s is currently applied on a node or desired by a node, will not delete", string(mosb.Status.DigestedImagePushSpec))
				return nil
			}
		}
	}

	image := string(mosb.Spec.RenderedImagePushSpec)
	if err := b.deleteImage(ctx, image, mosb); err != nil {
		wrappedErr := fmt.Errorf("could not delete image for MachineOSBuild %s for MachineOSConfig %s: %w", mosb.Name, moscName, err)
		// If the image cannot be deleted because it either does not exist or one's
		// creds do not have the necessary permissions, then we should ignore the
		// error and continue.
		if imagepruner.IsTolerableDeleteErr(err) || k8serrors.IsNotFound(err) {
			klog.Warning(wrappedErr.Error())
		} else {
			return wrappedErr
		}
	} else {
		klog.Infof("Deleted image %s from registry for MachineOSBuild %s", image, mosb.Name)
	}

	return nil
}

// Finds and deletes any other running builds for a given MachineOSConfig.
func (b *buildReconciler) deleteOtherBuildsForMachineOSConfig(ctx context.Context, newMosb *mcfgv1.MachineOSBuild, mosc *mcfgv1.MachineOSConfig) error {
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
func (b *buildReconciler) getMachineOSBuildsForMachineOSConfig(mosc *mcfgv1.MachineOSConfig) ([]*mcfgv1.MachineOSBuild, error) {
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
			return fmt.Errorf("could not sync MachineOSConfigs: %w", err)
		}

		if err := b.syncMachineOSBuilds(ctx); err != nil {
			return fmt.Errorf("could not sync MachineOSBuilds: %w", err)
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
func (b *buildReconciler) syncMachineOSBuild(ctx context.Context, mosb *mcfgv1.MachineOSBuild) error {
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

		// At install time, the pool's status.configuration.name may be empty until the pool converges.
		// Skip the rendered config comparison in this case and proceed with the build.
		if mcp.Status.Configuration.Name == "" {
			klog.V(4).Infof("MachineConfigPool %q status.configuration.name is empty (likely at install time), skipping rendered config comparison", mcp.Name)
		} else {
			oldRendered, err := b.machineConfigLister.Get(mcp.Status.Configuration.Name)
			if err != nil {
				return err
			}
			newRendered, err := b.machineConfigLister.Get(mcp.Spec.Configuration.Name)
			if err != nil {
				return err
			}

			old := mcp.DeepCopy()
			old.Spec.Configuration.Name = mcp.Status.Configuration.Name

			// reconcileImageRebuild checks to see if we require a new build job (an MC consists of a osimage url,
			// kernel args, or ext change)
			needsImageRebuild, err := b.reconcileImageRebuild(old, mcp)
			if err != nil {
				return err
			}
			if oldRendered != newRendered && !needsImageRebuild {
				klog.Infof("MachineOSBuild %q: No new image needs to be created, reusing last MOSB", mosb.Name)
				return nil
			}
		}

		mosbState := ctrlcommon.NewMachineOSBuildState(mosb)

		// CRITICAL CHECK: If this MOSB has the pre-built-image label, it's a synthetic build
		// and we should NEVER create a real build job for it. This check must come FIRST
		// before any state checks, because during synthetic MOSB creation there's a timing
		// window where the MOSB exists but status hasn't been updated yet, so state checks
		// might incorrectly think it needs a build.
		if mosb.Labels != nil {
			if preBuiltLabel, hasLabel := mosb.Labels[constants.PreBuiltImageLabelKey]; hasLabel && preBuiltLabel == constants.TrueValue {
				klog.V(4).Infof("MachineOSBuild %q has pre-built-image label, this is a synthetic build - skipping real build start", mosb.Name)
				return nil
			}
		}

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

			// If this MOSC has a pre-built image annotation and hasn't been seeded yet,
			// don't start a real build - the seeding workflow should handle creating a synthetic build
			if isPreBuiltImageAwaitingSeeding(mosc) {
				klog.Infof("MachineOSBuild %q associated with MachineOSConfig %q has pre-built image annotation but hasn't been seeded yet, skipping real build start (seeding workflow should handle this)", mosb.Name, mosc.Name)
				return nil
			}

			observer := imagebuilder.NewJobImageBuildObserver(b.kubeclient, b.mcfgclient, mosb, mosc)

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
func (b *buildReconciler) syncMachineOSConfig(ctx context.Context, mosc *mcfgv1.MachineOSConfig) error {
	return b.timeObjectOperation(mosc, syncingVerb, func() error {
		// Check if we need to clean up the pre-built image annotation after successful seeding
		// This happens in a separate reconciliation to ensure status is persisted before removing annotation
		if needsPreBuiltImageAnnotationCleanup(mosc) {
			klog.Infof("MachineOSConfig %q seeding complete and status populated, removing pre-built image annotation", mosc.Name)
			delete(mosc.Annotations, constants.PreBuiltImageAnnotationKey)
			if _, err := b.mcfgclient.MachineconfigurationV1().MachineOSConfigs().Update(ctx, mosc, metav1.UpdateOptions{}); err != nil {
				return fmt.Errorf("could not remove pre-built image annotation from MachineOSConfig %q: %w", mosc.Name, err)
			}
			klog.Infof("Successfully removed pre-built image annotation from MachineOSConfig %q", mosc.Name)
			// Trigger another sync to handle any pending work now that annotation is removed
			return nil
		}

		mosbs, err := b.getMachineOSBuildsForMachineOSConfig(mosc)
		if err != nil {
			return fmt.Errorf("could not list MachineOSBuilds for MachineOSConfig %q: %w", mosc.Name, err)
		}

		klog.V(4).Infof("MachineOSConfig %q is associated with %d MachineOSBuilds %v", mosc.Name, len(mosbs), getMachineOSBuildNames(mosbs))

		for _, mosb := range mosbs {
			// If we found the currently-associated MachineOSBuild for this
			// MachineOSConfig, we're done. We prefer ones with the full image pullspec.
			if isMachineOSBuildCurrentForMachineOSConfigWithPullspec(mosc, mosb) {
				klog.Infof("MachineOSConfig %q has current build annotation and current image pullspec %q for MachineOSBuild %q", mosc.Name, mosc.Status.CurrentImagePullSpec, mosb.Name)
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

		// If the MachineOSConfig has a pre-built image annotation AND hasn't been seeded yet,
		// the seeding workflow should handle creating the synthetic build. Don't create a normal build here.
		if shouldSeedWithPreBuiltImage(mosc) {
			klog.Infof("MachineOSConfig %q has pre-built image annotation but hasn't been seeded yet, skipping normal build creation (seeding workflow should handle this)", mosc.Name)
			return nil
		}

		klog.Infof("No matching MachineOSBuild found for MachineOSConfig %q, will create one", mosc.Name)
		if err := b.createNewMachineOSBuildOrReuseExisting(ctx, mosc, false); err != nil {
			return fmt.Errorf("could not create new or reuse existing MachineOSBuild for MachineOSConfig %q: %w", mosc.Name, err)
		}

		return nil
	})
}

// Syncs all existing and opted-in MachineConfigPools.
func (b *buildReconciler) syncMachineConfigPools(ctx context.Context) error {
	err := b.timeSyncOperation("MachineConfigPools", func() error {
		mcps, err := b.machineConfigPoolLister.List(labels.Everything())
		if err != nil {
			return fmt.Errorf("could not list MachineConfigPools: %w", err)
		}

		for _, mcp := range mcps {
			if err := b.syncMachineConfigPool(ctx, mcp); err != nil {
				return fmt.Errorf("could not sync MachineConfigPool %q: %w", mcp.Name, err)
			}
		}

		return nil
	})

	if err != nil {
		return fmt.Errorf("could not sync MachineConfigPools: %w", err)
	}

	return nil
}

// Syncs a given MachineConfigPool by cross-checking it against known
// MachineOSConfigs and MachineOSBuilds, which will create a new MachineOSBuild,
// if needed.
func (b *buildReconciler) syncMachineConfigPool(ctx context.Context, mcp *mcfgv1.MachineConfigPool) error {
	return b.timeObjectOperation(mcp, syncingVerb, func() error {
		return b.reconcilePoolChange(ctx, mcp)
	})
}

func (b *buildReconciler) reconcilePoolChange(ctx context.Context, mcp *mcfgv1.MachineConfigPool) error {
	mosc, err := utils.GetMachineOSConfigForMachineConfigPool(mcp, b.utilListers())
	if err != nil {
		if k8serrors.IsNotFound(err) {
			klog.Infof("No MachineOSConfig for pool %q, skipping", mcp.Name)
			return nil
		}
		return fmt.Errorf("failed to get MachineOSConfig for pool %q: %w", mcp.Name, err)
	}

	// If the MachineOSConfig has a pre-built image annotation AND hasn't been seeded yet,
	// the seeding workflow should handle creating the synthetic build. Don't proceed with normal build workflow.
	// Seeding is considered complete once the currentBuild annotation is set.
	firstOptIn := mosc.Annotations[constants.CurrentMachineOSBuildAnnotationKey]
	if _, hasPreBuiltImage := getPreBuiltImage(mosc); hasPreBuiltImage && firstOptIn == "" {
		klog.Infof("MachineOSConfig %q has pre-built image annotation but hasn't been seeded yet, skipping pool change reconciliation (seeding workflow should handle this)", mosc.Name)
		return nil
	}

	oldRendered := mcp.Status.Configuration.Name
	newRendered := mcp.Spec.Configuration.Name

	// At install time, the pool's status.configuration.name or spec.configuration.name may be empty
	// until the pool converges. Skip the image rebuild check in this case.
	if oldRendered == "" || newRendered == "" {
		klog.V(4).Infof("MachineConfigPool %q has empty configuration name (status: %q, spec: %q), likely at install time, skipping pool change reconciliation", mcp.Name, oldRendered, newRendered)
		return nil
	}

	// old pool
	old := mcp.DeepCopy()
	old.Spec.Configuration.Name = mcp.Status.Configuration.Name
	if firstOptIn == "" {
		return fmt.Errorf("no current build annotation on MachineOSConfig %q", mosc.Name)
	}

	needsImageRebuild, err := b.reconcileImageRebuild(old, mcp)
	if err != nil {
		return err
	}

	// This is our trigger point
	if (oldRendered != newRendered && needsImageRebuild) || firstOptIn == "" {
		klog.Infof("pool %q: rendered config changed and requires an image rebuild. Verifying if a valid build already exists...", mcp.Name)

		osImageURLs, _ := ctrlcommon.GetOSImageURLConfig(ctx, b.kubeclient)
		targetMosb, err := buildrequest.NewMachineOSBuild(buildrequest.MachineOSBuildOpts{
			MachineOSConfig:   mosc,
			MachineConfigPool: mcp,
			OSImageURLConfig:  osImageURLs,
		})
		if err != nil {
			return fmt.Errorf("could not generate name for target MOSB: %w", err)
		}

		// Now, check if a MOSB with that name already exists.
		existingMosb, err := b.machineOSBuildLister.Get(targetMosb.Name)
		if err == nil {
			// A MOSB for our target config was found. Check its status
			mosbState := ctrlcommon.NewMachineOSBuildState(existingMosb)

			if mosbState.IsInTransientState() {
				klog.Infof("pool %q: MOSB (%s) is in a transient state. Please allow time for MOSB to finish updating.", mcp.Name, existingMosb.Name)
				return nil
			}

			// if MOSB state is successful, this can mean one of two things:
			// 1. Applied MC triggered a MOSB build through `needsImageRebuild`, and it completed, but we are waiting for spec == status in the node update
			// 2. Current MOSB state is successful, and a deleted MC triggered a MOSB build through `needsImageRebuild`.
			if mosbState.IsBuildSuccess() {
				// Next, we should check if the image associated with the MachineOSBuild still exists.
				info, err := b.inspectImage(ctx, string(existingMosb.Status.DigestedImagePushSpec), existingMosb)
				// If the image exists, reuse it.
				if info != nil && err == nil {
					klog.Infof("pool %q: Found successful build for target whose image exists. Reusing image.", mcp.Name)
					return b.reuseImageForNewMOSB(ctx, mosc, existingMosb)
				}

				// If the image does not exist, rebuild it.
				if imagepruner.IsImageNotFoundErr(err) {
					klog.Infof("pool %q: Found successful build for target whose image no longer exists. Will rebuild.", mcp.Name)
					return b.createNewMachineOSBuildOrReuseExisting(ctx, mosc, true)
				}

				// If we could not inspect the image, we might not have permissions to
				// do so, or it could be another issue. Either way, we should return an
				// error here.
				return fmt.Errorf("could not inspect image %s for MachineOSBuild %s for MachineConfigPool %s: %w", string(existingMosb.Status.DigestedImagePushSpec), existingMosb.Name, mcp.Name, err)
			}
		} else if !k8serrors.IsNotFound(err) {
			// An actual error occurred (not just "not found"). Return the error.
			return fmt.Errorf("could not get target MOSB %s: %w", targetMosb.Name, err)
		}

	} else if oldRendered != newRendered && !needsImageRebuild {
		klog.Infof("pool %q: No new image needs to be created, reusing last MOSB", mcp.Name)
		prevPullSpec := mosc.Status.CurrentImagePullSpec
		oldMOSB, err := utils.GetMachineOSBuildForImagePullspec(string(prevPullSpec), b.utilListers())
		if err != nil {
			return fmt.Errorf("failed to look up MachineOSBuild for pull-spec %q: %w", prevPullSpec, err)
		}
		return b.reuseImageForNewMOSB(ctx, mosc, oldMOSB)
	}

	klog.Infof("pool %q: detected extension/kernel/kargs/OSImageURL change → will rebuild image", mcp.Name)
	return b.createNewMachineOSBuildOrReuseExisting(ctx, mosc, needsImageRebuild)

}

// reuseImageForNewMOSB creates a new MOSB (for the new rendered-MC name)
// but populates its status from oldMosb so that no build actually runs.
func (b *buildReconciler) reuseImageForNewMOSB(ctx context.Context, mosc *mcfgv1.MachineOSConfig, oldMosb *mcfgv1.MachineOSBuild,
) error {
	// Look up the MCP associated with the MOSC
	mcp, err := b.machineConfigPoolLister.Get(mosc.Spec.MachineConfigPool.Name)
	if err != nil {
		return err
	}

	// Get the osimageurl for our new MOSB object
	osImageURLs, err := ctrlcommon.GetOSImageURLConfig(ctx, b.kubeclient)
	if err != nil {
		return err
	}
	// Build the new MOSB object. this is our "promise", we will eventually check if we will proceed with this
	newMosb, err := buildrequest.NewMachineOSBuild(
		buildrequest.MachineOSBuildOpts{
			MachineOSConfig:   mosc,
			MachineConfigPool: mcp,
			OSImageURLConfig:  osImageURLs,
		})
	if err != nil {
		return err
	}
	// todo (dkhater): push the SetOwnerReferences() part into the NewMachineOSBuild() constructor
	// since we already have the MOSC there and it feels like something the MOSB constructor should be setting.

	// set the ownder of the new MOSB to be the MOSC so we can garbage collect this later
	newMosb.SetOwnerReferences([]metav1.OwnerReference{
		*metav1.NewControllerRef(mosc, mcfgv1.SchemeGroupVersion.WithKind("MachineOSConfig")),
	})

	// check if a MOSB with the newly generated name already exists in the cluster
	_, err = b.machineOSBuildLister.Get(newMosb.Name)
	if k8serrors.IsNotFound(err) {
		// create the new MOSB object
		if newMosb, err = b.mcfgclient.
			MachineconfigurationV1().
			MachineOSBuilds().
			Create(ctx, newMosb, metav1.CreateOptions{}); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}

	image := string(oldMosb.Status.DigestedImagePushSpec)

	inspect, err := b.inspectImage(ctx, image, newMosb)
	// this is our "reality check": try to inspect the image in the registry to see if it still exists
	switch {
	// image is found, we will reuse this image
	case inspect != nil && err == nil:
		klog.Infof("Existing MachineOSBuild %q found, reusing image %q by assigning to MachineOSConfig %q", newMosb.Name, image, mosc.Name)
	// we are unauthorized and need to report this
	case err != nil && (k8serrors.IsUnauthorized(err) || imagepruner.IsAccessDeniedErr(err)):
		return fmt.Errorf("authentication failed while inspecting image %q for MachineOSBuild %q: %w", image, newMosb.Name, err)
	// image does not exist, so we delete MOSB and rebuild
	case err != nil && (k8serrors.IsNotFound(err) || imagepruner.IsImageNotFoundErr(err)):
		klog.Infof("Deleting MachineOSBuild %q and rebuilding", newMosb.Name)
		if deleteErr := b.mcfgclient.MachineconfigurationV1().MachineOSBuilds().Delete(ctx, newMosb.Name, metav1.DeleteOptions{}); deleteErr != nil && !k8serrors.IsNotFound(deleteErr) {
			return fmt.Errorf("could not delete MachineOSBuild %q: %w", newMosb.Name, deleteErr)
		}
		return nil
	default:
		return fmt.Errorf("unexpected error inspecting image %q for MachineOSBuild %q: %w", image, newMosb.Name, err)
	}

	// get the latest version of the new MOSB object to ensure we update it correctly
	toUpdate, err := b.getMachineOSBuildForUpdate(newMosb)
	if err != nil {
		return err
	}
	// store the current status
	oldStatus := toUpdate.Status

	// copy image url
	toUpdate.Status.DigestedImagePushSpec = oldMosb.Status.DigestedImagePushSpec

	// set conditions on new MOSB status to succeeded
	for _, c := range apihelpers.MachineOSBuildSucceededConditions() {
		apihelpers.SetMachineOSBuildCondition(&toUpdate.Status, c)
	}

	// update MOSB object with the status
	if err := b.setStatusOnMachineOSBuildIfNeeded(ctx, toUpdate, oldStatus, toUpdate.Status); err != nil {
		return err
	}

	// update parent MOSC status to point to newly built (aka the reused) MOSB
	return b.updateMachineOSConfigStatus(ctx, mosc, toUpdate)
}

// getObjectsForImagePruner retrieves the secret for the MachineOSBuild and the ControllerConfig for use by the imagepruner.
func (b *buildReconciler) getObjectsForImagePruner(mosb *mcfgv1.MachineOSBuild) (*corev1.Secret, *mcfgv1.ControllerConfig, error) {
	secretName := mosb.Annotations[constants.RenderedImagePushSecretAnnotationKey]

	if secretName == "" {
		return nil, nil, fmt.Errorf("MachineOSBuild %s missing annotation %s", mosb.Name, constants.RenderedImagePushSecretAnnotationKey)
	}

	secret, err := b.kubeclient.CoreV1().Secrets(ctrlcommon.MCONamespace).Get(context.TODO(), secretName, metav1.GetOptions{})
	if err != nil {
		return nil, nil, fmt.Errorf("could not get rendered push secret %s: %w", secretName, err)
	}

	controllerConfigs, err := b.listers.controllerConfigLister.List(labels.Everything())
	if err != nil {
		return nil, nil, fmt.Errorf("could not list ControllerConfigs: %w", err)
	}

	if len(controllerConfigs) == 0 {
		return nil, nil, fmt.Errorf("no ControllerConfigs found")
	}

	return secret.DeepCopy(), controllerConfigs[0].DeepCopy(), nil
}

// inspectImage retrieves the necessary objects and calls InspectImage on the imagepruner.
func (b *buildReconciler) inspectImage(ctx context.Context, pullspec string, mosb *mcfgv1.MachineOSBuild) (*types.ImageInspectInfo, error) {
	secret, cc, err := b.getObjectsForImagePruner(mosb)
	if err != nil {
		return nil, err
	}

	info, _, err := b.imagepruner.InspectImage(ctx, pullspec, secret, cc)
	return info, err
}

// deleteImage retrieves the necessary objects and calls DeleteImage on the imagepruner.
func (b *buildReconciler) deleteImage(ctx context.Context, pullspec string, mosb *mcfgv1.MachineOSBuild) error {
	isOpenShiftRegistry, err := ctrlcommon.IsOpenShiftRegistry(ctx, pullspec, b.kubeclient, b.routeclient)
	if err != nil {
		return err
	}

	if isOpenShiftRegistry {
		klog.Infof("Deleting image %s from internal registry for MachineOSBuild %s", pullspec, mosb.Name)
		// Use the openshift API to delete the image
		ns, img, err := extractNSAndNameWithTag(pullspec)
		if err != nil {
			return err
		}
		if err := b.imageclient.ImageV1().ImageStreamTags(ns).Delete(context.TODO(), img, metav1.DeleteOptions{}); err != nil {
			if k8serrors.IsNotFound(err) {
				klog.Infof("image %s for MachineOSBuild %s not found", pullspec, mosb.Name)
				return nil
			}
			return fmt.Errorf("could not delete image %s from internal registry for MachineOSBuild %s: %w", pullspec, mosb.Name, err)
		}
		return nil
	}

	klog.Infof("Deleting image %s from external registry using skopeo for MachineOSBuild %s", pullspec, mosb.Name)
	secret, cc, err := b.getObjectsForImagePruner(mosb)
	if err != nil {
		return err
	}

	return b.imagepruner.DeleteImage(ctx, pullspec, secret, cc)
}

// Determines if builds should be prevented due to pool degradation.
// Returns true if pool has ANY degraded condition other than BuildDegraded,
// but false if degraded ONLY due to BuildDegraded (to allow retry attempts).
func (b *buildReconciler) shouldPreventBuildDueToDegradation(mcp *mcfgv1.MachineConfigPool) bool {
	// Check for ALL degradation conditions except BuildDegraded that should prevent new builds
	// We intentionally exclude BuildDegraded to allow retries
	// We check specific conditions rather than overall Degraded since Degraded=True could be due to BuildDegraded alone
	nonBuildDegradedTypes := []mcfgv1.MachineConfigPoolConditionType{
		mcfgv1.MachineConfigPoolNodeDegraded,
		mcfgv1.MachineConfigPoolRenderDegraded,
		mcfgv1.MachineConfigPoolPinnedImageSetsDegraded,
		mcfgv1.MachineConfigPoolSynchronizerDegraded,
	}

	for _, condType := range nonBuildDegradedTypes {
		if apihelpers.IsMachineConfigPoolConditionTrue(mcp.Status.Conditions, condType) {
			return true
		}
	}

	return false
}

// reconcileImageRebuild calls RequiresRebuild to see if an MC changes the kernel args, ext, or osimageurl.
// if it does, we build a new image in our new MOSB
func (b *buildReconciler) reconcileImageRebuild(oldMCP, curMCP *mcfgv1.MachineConfigPool) (bool, error) {

	curr, err := b.machineConfigLister.Get(oldMCP.Spec.Configuration.Name)
	if err != nil {
		return false, err
	}
	des, err := b.machineConfigLister.Get(curMCP.Spec.Configuration.Name)
	if err != nil {
		return false, err
	}

	return ctrlcommon.RequiresRebuild(curr, des), nil
}

// Clears BuildDegraded condition when a new build starts (allowing retry after failure)
func (b *buildReconciler) initializeBuildDegradedCondition(ctx context.Context, pool *mcfgv1.MachineConfigPool) error {
	// Check if BuildDegraded condition is already False - if so, no update needed
	if apihelpers.IsMachineConfigPoolConditionFalse(pool.Status.Conditions, mcfgv1.MachineConfigPoolImageBuildDegraded) {
		return nil
	}

	// Clear BuildDegraded condition (even if it was True from previous failure) when new build starts
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		// Get fresh copy for update to avoid conflicts
		currentPool, err := b.mcfgclient.MachineconfigurationV1().MachineConfigPools().Get(ctx, pool.Name, metav1.GetOptions{})
		if err != nil {
			return err
		}

		buildDegraded := apihelpers.NewMachineConfigPoolCondition(mcfgv1.MachineConfigPoolImageBuildDegraded, corev1.ConditionFalse, string(mcfgv1.MachineConfigPoolBuilding), "Build started for pool "+currentPool.Name)
		apihelpers.SetMachineConfigPoolCondition(&currentPool.Status, *buildDegraded)

		_, err = b.mcfgclient.MachineconfigurationV1().MachineConfigPools().UpdateStatus(ctx, currentPool, metav1.UpdateOptions{})
		return err
	})
}

// Clears BuildDegraded condition when build succeeds
func (b *buildReconciler) syncBuildSuccessStatus(ctx context.Context, pool *mcfgv1.MachineConfigPool) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		// Get fresh copy for update to avoid conflicts
		currentPool, err := b.mcfgclient.MachineconfigurationV1().MachineConfigPools().Get(ctx, pool.Name, metav1.GetOptions{})
		if err != nil {
			return err
		}

		buildDegraded := apihelpers.NewMachineConfigPoolCondition(mcfgv1.MachineConfigPoolImageBuildDegraded, corev1.ConditionFalse, string(mcfgv1.MachineConfigPoolBuildSuccess), "Build succeeded for pool "+currentPool.Name)
		apihelpers.SetMachineConfigPoolCondition(&currentPool.Status, *buildDegraded)

		_, err = b.mcfgclient.MachineconfigurationV1().MachineConfigPools().UpdateStatus(ctx, currentPool, metav1.UpdateOptions{})
		return err
	})
}

// Sets BuildDegraded condition when build fails
func (b *buildReconciler) syncBuildFailureStatus(ctx context.Context, pool *mcfgv1.MachineConfigPool, buildErr error, mosbName string) error {
	updateErr := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		// Get fresh copy for update to avoid conflicts
		currentPool, err := b.mcfgclient.MachineconfigurationV1().MachineConfigPools().Get(ctx, pool.Name, metav1.GetOptions{})
		if err != nil {
			return err
		}

		// The message content may be truncated https://github.com/kubernetes/apimachinery/blob/f5dd29d6ada12819a4a6ddc97d5bdf812f8a1cad/pkg/apis/meta/v1/types.go#L1619-L1635
		buildDegraded := apihelpers.NewMachineConfigPoolCondition(mcfgv1.MachineConfigPoolImageBuildDegraded, corev1.ConditionTrue, string(mcfgv1.MachineConfigPoolBuildFailed), fmt.Sprintf("Failed to build OS image for pool %s (MachineOSBuild: %s): %v", currentPool.Name, mosbName, buildErr))
		apihelpers.SetMachineConfigPoolCondition(&currentPool.Status, *buildDegraded)

		_, updateErr := b.mcfgclient.MachineconfigurationV1().MachineConfigPools().UpdateStatus(ctx, currentPool, metav1.UpdateOptions{})
		return updateErr
	})
	if updateErr != nil {
		klog.Errorf("Error updating MachineConfigPool %s BuildDegraded status: %v", pool.Name, updateErr)
	}
	return buildErr
}

// getCurrentBuild finds the currently active (most relevant) build from a list of MachineOSBuilds
// Priority: 1) Currently referenced by MOSC, 2) Building/Prepared, 3) Most recent
func (b *buildReconciler) getCurrentBuild(mosc *mcfgv1.MachineOSConfig, mosbList []*mcfgv1.MachineOSBuild) *mcfgv1.MachineOSBuild {
	var activeBuild *mcfgv1.MachineOSBuild
	var mostRecentBuild *mcfgv1.MachineOSBuild

	// First, look for the build currently referenced by the MachineOSConfig
	for _, mosb := range mosbList {
		if isMachineOSBuildCurrentForMachineOSConfig(mosc, mosb) {
			activeBuild = mosb
			break
		}
	}

	// If no current build found, look for active builds (building/prepared)
	if activeBuild == nil {
		for _, mosb := range mosbList {
			mosbState := ctrlcommon.NewMachineOSBuildState(mosb)
			if mosbState.IsBuilding() || mosbState.IsBuildPrepared() {
				activeBuild = mosb
				break
			}
		}
	}

	// Keep track of the most recent build regardless
	for _, mosb := range mosbList {
		if mostRecentBuild == nil || mosb.CreationTimestamp.After(mostRecentBuild.CreationTimestamp.Time) {
			mostRecentBuild = mosb
		}
	}

	// If still no active build, use the most recent one
	if activeBuild == nil {
		activeBuild = mostRecentBuild
	}

	return activeBuild
}

// updateImageBuildDegradedCondition examines all MachineOSBuilds for the MachineOSConfig
// and sets the ImageBuildDegraded condition based on the status of the currently active build
func (b *buildReconciler) updateImageBuildDegradedCondition(ctx context.Context, pool *mcfgv1.MachineConfigPool, mosc *mcfgv1.MachineOSConfig) error {
	mosbList, err := b.getMachineOSBuildsForMachineOSConfig(mosc)
	if err != nil {
		return fmt.Errorf("could not get MachineOSBuilds for MachineOSConfig %q: %w", mosc.Name, err)
	}

	// Find the currently active build
	activeBuild := b.getCurrentBuild(mosc, mosbList)

	// If no builds exist at all, clear any existing BuildDegraded condition
	if activeBuild == nil {
		return b.syncBuildSuccessStatus(ctx, pool)
	}

	// Update condition based on the active build's status
	activeState := ctrlcommon.NewMachineOSBuildState(activeBuild)

	switch {
	case activeState.IsBuildFailure():
		// Set BuildDegraded=True for failed builds
		buildError := getBuildErrorFromMOSB(activeBuild)
		return b.syncBuildFailureStatus(ctx, pool, buildError, activeBuild.Name)
	case activeState.IsBuildSuccess():
		// Clear BuildDegraded=False for successful builds
		return b.syncBuildSuccessStatus(ctx, pool)
	case activeState.IsBuilding(), activeState.IsBuildPrepared():
		// Clear BuildDegraded=False for builds in progress (allow retry after previous failure)
		return b.initializeBuildDegradedCondition(ctx, pool)
	}

	// For any other states, don't change the condition
	return nil
}

// seedMachineOSConfigWithExistingImage handles the seeding of a MachineOSConfig with a pre-built image
func (b *buildReconciler) seedMachineOSConfigWithExistingImage(ctx context.Context, mosc *mcfgv1.MachineOSConfig, imageSpec string) error {
	// Step 1: Ensure required push secret exists from bootstrap
	if mosc.Spec.RenderedImagePushSecret.Name == "" {
		return fmt.Errorf("MachineOSConfig %q has no rendered image push secret", mosc.Name)
	}
	if err := b.ensureBootstrapSecretExists(ctx, mosc.Spec.RenderedImagePushSecret.Name); err != nil {
		return fmt.Errorf("could not ensure push secret %s exists: %w", mosc.Spec.RenderedImagePushSecret.Name, err)
	}

	// Step 2: Generate expected MachineOSBuild using existing MCO logic
	mcp, err := b.machineConfigPoolLister.Get(mosc.Spec.MachineConfigPool.Name)
	if err != nil {
		return fmt.Errorf("could not get MachineConfigPool %q: %w", mosc.Spec.MachineConfigPool.Name, err)
	}

	templateMOSB, err := buildrequest.NewMachineOSBuildFromAPI(ctx, b.kubeclient, mosc, mcp)
	if err != nil {
		return fmt.Errorf("could not generate MachineOSBuild template for MachineOSConfig %q: %w", mosc.Name, err)
	}

	// Step 2: Create synthetic MachineOSBuild with success status
	syntheticMOSB, err := b.createSyntheticMachineOSBuild(ctx, mosc, templateMOSB.Name, imageSpec)
	if err != nil {
		return fmt.Errorf("could not create synthetic MachineOSBuild for MachineOSConfig %q: %w", mosc.Name, err)
	}

	// Step 3: Update MachineOSConfig with build annotation and status
	if err := b.updateMachineOSConfigForSeeding(ctx, mosc, syntheticMOSB, imageSpec); err != nil {
		return fmt.Errorf("could not update MachineOSConfig %q for seeding: %w", mosc.Name, err)
	}

	klog.Infof("Successfully seeded MachineOSConfig %q with pre-built image %q", mosc.Name, imageSpec)
	return nil
}

// createSyntheticMachineOSBuild creates a MachineOSBuild object for pre-built images
func (b *buildReconciler) createSyntheticMachineOSBuild(ctx context.Context, mosc *mcfgv1.MachineOSConfig, buildName, imageSpec string) (*mcfgv1.MachineOSBuild, error) {
	// Get current rendered MachineConfig for the pool
	mcp, err := b.machineConfigPoolLister.Get(mosc.Spec.MachineConfigPool.Name)
	if err != nil {
		return nil, fmt.Errorf("could not get MachineConfigPool %q: %w", mosc.Spec.MachineConfigPool.Name, err)
	}

	// Generate build metadata using the same utility as normal OCL workflow
	buildLabels := utils.GetMachineOSBuildLabels(mosc, mcp)
	// Add pre-built image marker
	buildLabels[constants.PreBuiltImageLabelKey] = constants.TrueValue

	buildAnnotations := map[string]string{
		constants.RenderedImagePushSecretAnnotationKey: mosc.Spec.RenderedImagePushSecret.Name,
	}

	now := metav1.Now()
	// buildEnd must be after buildStart for API validation
	buildEnd := metav1.NewTime(now.Add(1 * time.Second))

	// Set owner reference to the MachineOSConfig (matching normal OCL workflow)
	oref := metav1.NewControllerRef(mosc, mcfgv1.SchemeGroupVersion.WithKind("MachineOSConfig"))

	// Create MachineOSBuild object matching the normal OCL workflow template
	mosb := &mcfgv1.MachineOSBuild{
		TypeMeta: metav1.TypeMeta{
			Kind:       "MachineOSBuild",
			APIVersion: "machineconfiguration.openshift.io/v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:   buildName,
			Labels: buildLabels,
			Finalizers: []string{
				metav1.FinalizerDeleteDependents,
			},
			Annotations:     buildAnnotations,
			OwnerReferences: []metav1.OwnerReference{*oref},
		},
		Spec: mcfgv1.MachineOSBuildSpec{
			RenderedImagePushSpec: mosc.Spec.RenderedImagePushSpec,
			MachineConfig: mcfgv1.MachineConfigReference{
				Name: mcp.Spec.Configuration.Name,
			},
			MachineOSConfig: mcfgv1.MachineOSConfigReference{
				Name: mosc.Name,
			},
		},
		Status: mcfgv1.MachineOSBuildStatus{
			BuildStart:            &now,
			BuildEnd:              &buildEnd,
			DigestedImagePushSpec: mcfgv1.ImageDigestFormat(imageSpec),
			Conditions: []metav1.Condition{
				{
					Type:               string(mcfgv1.MachineOSBuildSucceeded),
					Status:             metav1.ConditionTrue,
					LastTransitionTime: now,
					Reason:             constants.ReasonPreBuiltImageSeeded,
					Message:            fmt.Sprintf("Pre-built image %q successfully seeded", imageSpec),
				},
				{
					Type:               string(mcfgv1.MachineOSBuildPrepared),
					Status:             metav1.ConditionFalse,
					LastTransitionTime: now,
					Reason:             constants.ReasonPreBuiltImageSeeded,
					Message:            "Skipped: using pre-built image",
				},
				{
					Type:               string(mcfgv1.MachineOSBuilding),
					Status:             metav1.ConditionFalse,
					LastTransitionTime: now,
					Reason:             constants.ReasonPreBuiltImageSeeded,
					Message:            "Skipped: using pre-built image",
				},
				{
					Type:               string(mcfgv1.MachineOSBuildFailed),
					Status:             metav1.ConditionFalse,
					LastTransitionTime: now,
					Reason:             constants.ReasonPreBuiltImageSeeded,
					Message:            "Skipped: using pre-built image",
				},
				{
					Type:               string(mcfgv1.MachineOSBuildInterrupted),
					Status:             metav1.ConditionFalse,
					LastTransitionTime: now,
					Reason:             constants.ReasonPreBuiltImageSeeded,
					Message:            "Skipped: using pre-built image",
				},
			},
		},
	}

	// Check if the MachineOSBuild already exists (may have been created by normal workflow due to timing)
	existingMOSB, err := b.mcfgclient.MachineconfigurationV1().MachineOSBuilds().Get(ctx, buildName, metav1.GetOptions{})
	if err != nil && !k8serrors.IsNotFound(err) {
		return nil, fmt.Errorf("could not check if MachineOSBuild %q exists: %w", buildName, err)
	}

	var createdMOSB *mcfgv1.MachineOSBuild
	if k8serrors.IsNotFound(err) {
		// Create the MachineOSBuild object (status will be ignored on creation)
		createdMOSB, err = b.mcfgclient.MachineconfigurationV1().MachineOSBuilds().Create(ctx, mosb, metav1.CreateOptions{})
		if err != nil {
			return nil, fmt.Errorf("could not create synthetic MachineOSBuild %q: %w", buildName, err)
		}
		klog.Infof("Created synthetic MachineOSBuild %q for pre-built image %q", buildName, imageSpec)
	} else {
		// MachineOSBuild already exists - this can happen due to race between seeding and normal workflow
		klog.Infof("MachineOSBuild %q already exists, converting to synthetic build with success status", buildName)

		// Update the existing MOSB to have the pre-built-image label if it doesn't already
		if existingMOSB.Labels == nil {
			existingMOSB.Labels = make(map[string]string)
		}
		if existingMOSB.Labels[constants.PreBuiltImageLabelKey] != constants.TrueValue {
			existingMOSB.Labels[constants.PreBuiltImageLabelKey] = constants.TrueValue
			existingMOSB, err = b.mcfgclient.MachineconfigurationV1().MachineOSBuilds().Update(ctx, existingMOSB, metav1.UpdateOptions{})
			if err != nil {
				return nil, fmt.Errorf("could not update labels on existing MachineOSBuild %q: %w", buildName, err)
			}
		}
		createdMOSB = existingMOSB
	}

	// Update the status separately (status is ignored on Create and must always be set for synthetic builds)
	createdMOSB.Status = mosb.Status
	updatedMOSB, err := b.mcfgclient.MachineconfigurationV1().MachineOSBuilds().UpdateStatus(ctx, createdMOSB, metav1.UpdateOptions{})
	if err != nil {
		return nil, fmt.Errorf("could not update status on synthetic MachineOSBuild %q: %w", buildName, err)
	}

	klog.Infof("Updated status on synthetic MachineOSBuild %q with success condition", buildName)
	return updatedMOSB, nil
}

// updateMachineOSConfigForSeeding updates MachineOSConfig for seeded state
func (b *buildReconciler) updateMachineOSConfigForSeeding(ctx context.Context, mosc *mcfgv1.MachineOSConfig, mosb *mcfgv1.MachineOSBuild, imageSpec string) error {
	// Update annotations - set current build annotation to mark seeding as complete
	// The currentBuild annotation serves as the marker that seeding has occurred
	metav1.SetMetaDataAnnotation(&mosc.ObjectMeta, constants.CurrentMachineOSBuildAnnotationKey, mosb.Name)

	// Update the MachineOSConfig object
	updatedMOSC, err := b.mcfgclient.MachineconfigurationV1().MachineOSConfigs().Update(ctx, mosc, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("could not update MachineOSConfig %q annotations: %w", mosc.Name, err)
	}

	// Update status with current image
	updatedMOSC.Status.CurrentImagePullSpec = mcfgv1.ImageDigestFormat(imageSpec)
	updatedMOSC.Status.ObservedGeneration = updatedMOSC.GetGeneration()
	updatedMOSC.Status.MachineOSBuild = &mcfgv1.ObjectReference{
		Name:     mosb.Name,
		Group:    mcfgv1.SchemeGroupVersion.Group,
		Resource: "machineosbuilds",
	}

	// Add conditions indicating seeded state
	seededCondition := metav1.Condition{
		Type:               constants.MachineOSConfigSeeded,
		Status:             metav1.ConditionTrue,
		LastTransitionTime: metav1.NewTime(time.Now()),
		Reason:             constants.ReasonPreBuiltImageSeeded,
		Message:            fmt.Sprintf("MachineOSConfig seeded with pre-built image %q", imageSpec),
	}

	// Add the condition to the status
	updatedMOSC.Status.Conditions = append(updatedMOSC.Status.Conditions, seededCondition)

	_, err = b.mcfgclient.MachineconfigurationV1().MachineOSConfigs().UpdateStatus(ctx, updatedMOSC, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("could not update MachineOSConfig %q status: %w", mosc.Name, err)
	}

	klog.Infof("Updated MachineOSConfig %q status with pre-built image %q", mosc.Name, imageSpec)
	return nil
}

// ensureBootstrapSecretExists verifies that the required secret exists in the MCO namespace.
// Secrets are expected to be created from bootstrap manifests when the cluster starts up.
func (b *buildReconciler) ensureBootstrapSecretExists(ctx context.Context, secretName string) error {
	// Check if the secret exists in the MCO namespace
	_, err := b.kubeclient.CoreV1().Secrets(ctrlcommon.MCONamespace).Get(ctx, secretName, metav1.GetOptions{})
	if err == nil {
		// Secret exists
		klog.V(4).Infof("Secret %s exists in MCO namespace", secretName)
		return nil
	}

	if k8serrors.IsNotFound(err) {
		return fmt.Errorf("required secret %s not found in %s namespace. Ensure the secret was created from bootstrap manifests or exists in the cluster", secretName, ctrlcommon.MCONamespace)
	}

	return fmt.Errorf("failed to check if secret %s exists: %w", secretName, err)
}

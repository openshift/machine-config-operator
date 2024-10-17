package build

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	mcfgv1alpha1 "github.com/openshift/api/machineconfiguration/v1alpha1"
	mcfgclientset "github.com/openshift/client-go/machineconfiguration/clientset/versioned"
	"github.com/openshift/machine-config-operator/pkg/apihelpers"
	"github.com/openshift/machine-config-operator/pkg/controller/build/buildrequest"
	"github.com/openshift/machine-config-operator/pkg/controller/build/constants"
	"github.com/openshift/machine-config-operator/pkg/controller/build/imagebuilder"
	"github.com/openshift/machine-config-operator/pkg/controller/build/utils"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
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
)

type reconciler interface {
	AddMachineOSBuild(context.Context, *mcfgv1alpha1.MachineOSBuild) error
	UpdateMachineOSBuild(context.Context, *mcfgv1alpha1.MachineOSBuild, *mcfgv1alpha1.MachineOSBuild) error
	DeleteMachineOSBuild(context.Context, *mcfgv1alpha1.MachineOSBuild) error

	AddMachineOSConfig(context.Context, *mcfgv1alpha1.MachineOSConfig) error
	UpdateMachineOSConfig(context.Context, *mcfgv1alpha1.MachineOSConfig, *mcfgv1alpha1.MachineOSConfig) error
	DeleteMachineOSConfig(context.Context, *mcfgv1alpha1.MachineOSConfig) error

	AddPod(context.Context, *corev1.Pod) error
	UpdatePod(context.Context, *corev1.Pod, *corev1.Pod) error

	UpdateMachineConfigPool(context.Context, *mcfgv1.MachineConfigPool, *mcfgv1.MachineConfigPool) error
}

// Holds the implementation of the buildReconciler. The buildReconciler's job
// is to respond to incoming events in a specific way. By doing this, the
// reconciliation process has a clear entrypoint for each incoming event.
type buildReconciler struct {
	mcfgclient mcfgclientset.Interface
	kubeclient clientset.Interface
	*listers
}

// Instantiates a new reconciler instance. This returns an interface to
// disallow access to its private methods.
func newBuildReconciler(mcfgclient mcfgclientset.Interface, kubeclient clientset.Interface, l *listers) reconciler {
	return newBuildReconcilerAsStruct(mcfgclient, kubeclient, l)
}

func newBuildReconcilerAsStruct(mcfgclient mcfgclientset.Interface, kubeclient clientset.Interface, l *listers) *buildReconciler {
	return &buildReconciler{
		mcfgclient: mcfgclient,
		kubeclient: kubeclient,
		listers:    l,
	}
}

// Executes whenever a new MachineOSConfig is added.
func (b *buildReconciler) AddMachineOSConfig(ctx context.Context, mosc *mcfgv1alpha1.MachineOSConfig) error {
	return b.timeObjectOperation(mosc, addingVerb, func() error {
		return b.createMachineOSBuild(ctx, mosc)
	})
}

// Executes whenever an existing MachineOSConfig is updated.
func (b *buildReconciler) UpdateMachineOSConfig(ctx context.Context, old, cur *mcfgv1alpha1.MachineOSConfig) error {
	return b.timeObjectOperation(cur, updatingVerb, func() error {
		return b.updateMachineOSConfig(ctx, old, cur)
	})
}

// Executes whenever a MachineOSConfig is updated. If the build inputs have changeg, a new MachineOSBuild should be created.
func (b *buildReconciler) updateMachineOSConfig(ctx context.Context, old, cur *mcfgv1alpha1.MachineOSConfig) error {
	if !equality.Semantic.DeepEqual(old.Spec.BuildInputs, cur.Spec.BuildInputs) {
		klog.Infof("Detected MachineOSConfig change for %s", cur.Name)
		return b.createMachineOSBuild(ctx, cur)
	}

	return nil
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
		if err := b.deleteMachineOSBuildAndBuilder(ctx, mosb); err != nil {
			return fmt.Errorf("could not delete MachineOSBuild %s for MachineOSConfig %s: %w", mosb.Name, mosc.Name, err)
		}
	}

	return nil
}

// Executes whenever a new build pod is detected and updates the MachineOSBuild
// with any status changes.
func (b *buildReconciler) AddPod(ctx context.Context, pod *corev1.Pod) error {
	return b.timeObjectOperation(pod, addingVerb, func() error {
		return b.updateMachineOSBuildWithStatus(ctx, pod)
	})
}

// Executes whenever a build pod is updated. Updates the MachhineOSBuild object
// with the pod status.
func (b *buildReconciler) UpdatePod(ctx context.Context, oldPod, curPod *corev1.Pod) error {
	if oldPod.Status.Phase != curPod.Status.Phase {
		klog.Infof("Build pod %q transitioned from %q -> %q", curPod.GetName(), oldPod.Status.Phase, curPod.Status.Phase)
	}

	return b.timeObjectOperation(curPod, updatingVerb, func() error {
		return b.updateMachineOSBuildWithStatus(ctx, curPod)
	})
}

// Executes whenever a new MachineOSBuild is added. It starts executing the
// build in response to a new MachineOSBuild being created.
func (b *buildReconciler) AddMachineOSBuild(ctx context.Context, mosb *mcfgv1alpha1.MachineOSBuild) error {
	return b.timeObjectOperation(mosb, addingVerb, func() error {
		return b.startBuild(ctx, mosb)
	})
}

// Executes whenever a MachineOSBuild is updated.
func (b *buildReconciler) UpdateMachineOSBuild(ctx context.Context, old, cur *mcfgv1alpha1.MachineOSBuild) error {
	return b.timeObjectOperation(cur, updatingVerb, func() error {
		return b.updateMachineOSBuild(ctx, old, cur)
	})
}

// Performs the reconciliation whenever the MachineOSBuild is updated, such as
// cleaning up the build artifacts upon success.
func (b *buildReconciler) updateMachineOSBuild(ctx context.Context, _, current *mcfgv1alpha1.MachineOSBuild) error {
	mosc, err := b.getMachineOSConfigForMachineOSBuild(current)
	if err != nil {
		return err
	}

	// If the build was successful, clean up the build objects and propagate the
	// final image pushspec onto the MachineOSConfig object.
	if apihelpers.IsMachineOSBuildConditionTrue(current.Status.Conditions, mcfgv1alpha1.MachineOSBuildSucceeded) {
		klog.Infof("MachineOSBuild %s succeeded, cleaning up all ephemeral objects used for the build", current.Name)
		if err := imagebuilder.NewPodImageBuilder(b.kubeclient, b.mcfgclient, current, mosc).Clean(ctx); err != nil {
			return err
		}

		return retry.RetryOnConflict(retry.DefaultRetry, func() error {
			newMosc, err := b.machineOSConfigLister.Get(mosc.Name)
			if err != nil {
				return err
			}

			copied := newMosc.DeepCopy()
			copied.Status.CurrentImagePullspec = current.Status.FinalImagePushspec
			copied.Status.ObservedGeneration += copied.GetGeneration()

			_, err = b.mcfgclient.MachineconfigurationV1alpha1().MachineOSConfigs().UpdateStatus(ctx, copied, metav1.UpdateOptions{})
			if err == nil {
				klog.Infof("Updated status on MachineOSConfig %s", mosc.Name)
			}

			return err
		})
	}

	return nil
}

// Executes whenever a MachineOSBuild is deleted by cleaning up any remaining build artifacts that may be left behind.
func (b *buildReconciler) DeleteMachineOSBuild(ctx context.Context, mosb *mcfgv1alpha1.MachineOSBuild) error {
	return b.timeObjectOperation(mosb, deletingVerb, func() error {
		return b.deleteMachineOSBuildAndBuilder(ctx, mosb)
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
		return b.createMachineOSBuildForPoolChange(ctx, curMCP)
	}

	return nil
}

// Resolves the MachineOSConfig for a given MachineOSBuild.
func (b *buildReconciler) getMachineOSConfigForMachineOSBuild(mosb *mcfgv1alpha1.MachineOSBuild) (*mcfgv1alpha1.MachineOSConfig, error) {
	moscName, ok := mosb.Labels[constants.MachineOSConfigNameLabelKey]
	if moscName == "" || !ok {
		return nil, fmt.Errorf("MachineOSBuild is missing label %s", constants.MachineOSConfigNameLabelKey)
	}

	mosc, err := b.machineOSConfigLister.Get(moscName)
	if err != nil {
		return nil, fmt.Errorf("could not get MachineOSConfig %s for MachineOSBuild: %w", moscName, err)
	}

	return mosc, nil
}

// Starts executing a build for a given MachineOSBuild.
func (b *buildReconciler) startBuild(ctx context.Context, mosb *mcfgv1alpha1.MachineOSBuild) error {
	mosc, err := b.getMachineOSConfigForMachineOSBuild(mosb)
	if err != nil {
		return err
	}

	// If there are any other in-progress builds for this MachineOSConfig, stop them first.
	if err := b.deleteOtherBuildsForMachineOSConfig(ctx, mosb, mosc); err != nil {
		return fmt.Errorf("could not reconcile all MachineOSBuilds for MachineOSConfig %s: %w", mosc.Name, err)
	}

	// Next, create our new MachineOSBuild.
	if err := imagebuilder.NewPodImageBuilder(b.kubeclient, b.mcfgclient, mosb, mosc).Start(ctx); err != nil {
		return err
	}

	klog.Infof("Started new build %s for MachineOSBuild", utils.GetBuildPodName(mosb))

	// Update the MachineOSConfig with an annotation indicating what the currently running build is.
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		moscCopy := mosc.DeepCopy()

		if moscCopy.Annotations == nil {
			moscCopy.Annotations = map[string]string{}
		}

		currentMOSBName, ok := moscCopy.Annotations[constants.CurrentMachineOSBuildAnnotationKey]
		if ok && currentMOSBName == mosb.Name {
			klog.Infof("MachineOSConfig %s has MachineOSBuild annotation for %s!", mosc.Name, mosb.Name)
			return nil
		}

		moscCopy.Annotations[constants.CurrentMachineOSBuildAnnotationKey] = mosb.Name
		_, err = b.mcfgclient.MachineconfigurationV1alpha1().MachineOSConfigs().Update(ctx, moscCopy, metav1.UpdateOptions{})
		return err
	})
}

// Creates a MachineOSBuild in response to MachineConfigPool changes.
func (b *buildReconciler) createMachineOSBuildForPoolChange(ctx context.Context, mcp *mcfgv1.MachineConfigPool) error {
	moscList, err := b.machineOSConfigLister.List(labels.Everything())
	if err != nil {
		return err
	}

	for _, mosc := range moscList {
		if mosc.Spec.MachineConfigPool.Name == mcp.Name {
			return b.createMachineOSBuild(ctx, mosc.DeepCopy())
		}
	}

	klog.Infof("No MachineOSConfig found for MachineConfigPool %s", mcp.Name)

	return nil
}

// Executes whenever a new MachineOSBuild is created.
func (b *buildReconciler) createMachineOSBuild(ctx context.Context, mosc *mcfgv1alpha1.MachineOSConfig) error {
	mcp, err := b.machineConfigPoolLister.Get(mosc.Spec.MachineConfigPool.Name)
	if err != nil {
		return fmt.Errorf("could not get MachineConfigPool %s for MachineOSConfig %s: %w", mosc.Spec.MachineConfigPool.Name, mosc.Name, err)
	}

	if ctrlcommon.IsPoolAnyDegraded(mcp) {
		return errors.Join(fmt.Errorf("MachineConfigPool %s is degraded", mcp.Name), ctrlcommon.ErrDropFromQueue)
	}

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

	_, err = b.machineOSBuildLister.Get(mosb.Name)
	if k8serrors.IsNotFound(err) {
		_, err := b.mcfgclient.MachineconfigurationV1alpha1().MachineOSBuilds().Create(ctx, mosb, metav1.CreateOptions{})
		if err == nil {
			klog.Infof("New MachineOSBuild created: %s", mosb.Name)
			return nil
		}
	}

	// TODO: Figure out what to do when the MachineOSBuild already exists. If it
	// exists, that means we've already produced an image with the same inputs.
	// So we should be able to set the image pullspec on the MachineOSConfig and
	// use it.

	return err
}

// Gets the status from the running build and applies it to the MachineOSBuild.
func (b *buildReconciler) updateMachineOSBuildWithStatus(ctx context.Context, obj metav1.Object) error {
	builder, err := buildrequest.NewBuilder(obj)
	if err != nil {
		klog.Infof("%s does not have the required metadata: %s", obj.GetName(), err.Error())
		return nil
	}

	mosb, err := b.getMachineOSBuildForBuilder(builder)
	if err != nil {
		// If we can't find the MachineOSConfig at this point, it means that it was
		// probably deleted. Instead of trying to reconcile the status, we'll drop
		// it out of the execution queue.
		if k8serrors.IsNotFound(err) {
			return errors.Join(err, ctrlcommon.ErrDropFromQueue)
		}

		return err
	}

	mosc, err := b.getMachineOSConfigForBuilder(builder)
	if err != nil {
		// If we can't find the MachineOSConfig at this point, it means that it was
		// probably deleted. Instead of trying to reconcile the status, we'll drop
		// it out of the execution queue.
		if k8serrors.IsNotFound(err) {
			return errors.Join(err, ctrlcommon.ErrDropFromQueue)
		}

		return err
	}

	// TODO: Maybe infer MachineOSBuild state from the provided build object
	// instead of having to talk to the API server?
	observer := imagebuilder.NewPodImageBuildObserver(b.kubeclient, b.mcfgclient, mosb, mosc)

	status, err := observer.MachineOSBuildStatus(ctx)
	if err != nil {
		return err
	}

	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		newMosb, err := b.machineOSBuildLister.Get(mosb.Name)
		if err != nil {
			return err
		}

		bs := ctrlcommon.NewMachineOSBuildState(newMosb.DeepCopy())

		if bs.Build.Status.BuildStart == nil {
			now := metav1.Now()
			bs.Build.Status.BuildStart = &now
		}

		bs.SetBuildConditions(status.Conditions)
		bs.Build.Status.FinalImagePushspec = status.FinalImagePushspec
		bs.Build.Status.BuildEnd = status.BuildEnd
		bs.Build.Status.BuilderReference = status.BuilderReference

		_, err = b.mcfgclient.MachineconfigurationV1alpha1().MachineOSBuilds().UpdateStatus(ctx, bs.Build, metav1.UpdateOptions{})
		if err == nil {
			klog.Infof("Updated status on MachineOSBuild %s", bs.Build.Name)
		}

		return err
	})
}

// Resolves the MachineOSBuild for a given builder.
func (b *buildReconciler) getMachineOSBuildForBuilder(builder buildrequest.Builder) (*mcfgv1alpha1.MachineOSBuild, error) {
	mosbName, err := builder.MachineOSBuild()
	if err != nil {
		return nil, err
	}

	mosb, err := b.machineOSBuildLister.Get(mosbName)
	if err != nil {
		return nil, fmt.Errorf("could not get MachineOSBuild %s for builder %s: %w", mosbName, builder.GetObject().GetName(), err)
	}

	return mosb.DeepCopy(), nil
}

// Resolves the MachineOSConfig for a given builder.
func (b *buildReconciler) getMachineOSConfigForBuilder(builder buildrequest.Builder) (*mcfgv1alpha1.MachineOSConfig, error) {
	moscName, err := builder.MachineOSConfig()
	if err != nil {
		return nil, err
	}

	mosc, err := b.machineOSConfigLister.Get(moscName)
	if err != nil {
		return nil, fmt.Errorf("could not get MachineOSConfig %s for builder %s: %w", moscName, builder.GetObject().GetName(), err)
	}

	return mosc.DeepCopy(), nil
}

// Deletes the MachineOSBuild as well as the underlying builder objects.
func (b *buildReconciler) deleteMachineOSBuildAndBuilder(ctx context.Context, mosb *mcfgv1alpha1.MachineOSBuild) error {
	if err := imagebuilder.NewPodImageBuildCleaner(b.kubeclient, b.mcfgclient, mosb).Clean(ctx); err != nil {
		return fmt.Errorf("could not clean build %s: %w", mosb.Name, err)
	}

	if err := b.deleteMachineOSBuild(ctx, mosb); err != nil {
		return fmt.Errorf("could not delete MachineOSBuild %s: %w", mosb.Name, err)
	}

	return nil
}

// Deletes the MachineOSBuild.
func (b *buildReconciler) deleteMachineOSBuild(ctx context.Context, mosb *mcfgv1alpha1.MachineOSBuild) error {
	moscName := mosb.Labels[constants.MachineOSConfigNameLabelKey]

	err := b.mcfgclient.MachineconfigurationV1alpha1().MachineOSBuilds().Delete(ctx, mosb.Name, metav1.DeleteOptions{})
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

// Finds any other running builds for a given MachineOSConfig and deletes them.
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

		// If the build is in any other state except for "success", delete it.
		if isMachineOSBuildAnythingButSucceeded(mosb) {
			klog.Infof("Found running MachineOSBuild %s for MachineOSConfig %s, deleting...", mosb.Name, mosc.Name)
			if err := b.deleteMachineOSBuildAndBuilder(ctx, mosb); err != nil {
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

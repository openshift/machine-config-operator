package build

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	mcfgv1alpha1 "github.com/openshift/api/machineconfiguration/v1alpha1"
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
	"k8s.io/client-go/util/retry"
	"k8s.io/klog/v2"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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

type detailLine struct {
	machineConfig     string
	machineOSConfig   string
	machineOSBuild    string
	machineConfigPool string
}

func newDetailLine(mosc *mcfgv1alpha1.MachineOSConfig, mosb *mcfgv1alpha1.MachineOSBuild, mcp *mcfgv1.MachineConfigPool) *detailLine {
	return &detailLine{
		machineConfig:     mcp.Spec.Configuration.Name,
		machineConfigPool: mcp.Name,
		machineOSConfig:   mosc.Name,
		machineOSBuild:    mosb.Name,
	}
}

func (d *detailLine) String() string {
	return fmt.Sprintf("<MachineConfig: %q, MachineOSConfig: %q, MachineOSBuild: %q, MachineConfigPool %q>", d.machineConfig, d.machineOSConfig, d.machineOSBuild, d.machineConfigPool)
}

type buildReconciler struct {
	*Clients
	*listers
}

func newBuildReconciler(c *Clients, l *listers) reconciler {
	return &buildReconciler{
		Clients: c,
		listers: l,
	}
}

func (m *buildReconciler) timeObjectOperation(obj kubeObject, op string, toRun func() error) error {
	start := time.Now()

	kind, err := utils.GetKindForObject(obj)
	if err != nil && kind == "" {
		kind = "<unknown object kind>"
	}

	klog.Infof("%s %s %q", op, kind, obj.GetName())
	defer func() {
		klog.Infof("Finished %s %s %q after %s", strings.ToLower(op), kind, obj.GetName(), time.Since(start))
	}()

	return toRun()
}

func (m *buildReconciler) AddMachineOSConfig(ctx context.Context, mosc *mcfgv1alpha1.MachineOSConfig) error {
	return m.timeObjectOperation(mosc, "Adding", func() error {
		return m.createMachineOSBuild(ctx, mosc)
	})
}

func (m *buildReconciler) UpdateMachineOSConfig(ctx context.Context, old, cur *mcfgv1alpha1.MachineOSConfig) error {
	return m.timeObjectOperation(cur, "Updating", func() error {
		return m.updateMachineOSConfig(ctx, old, cur)
	})
}

func (m *buildReconciler) updateMachineOSConfig(ctx context.Context, old, cur *mcfgv1alpha1.MachineOSConfig) error {
	if !equality.Semantic.DeepEqual(old.Spec.BuildInputs, cur.Spec.BuildInputs) {
		klog.Infof("Detected MachineOSConfig change for %s", cur.Name)
		return m.createMachineOSBuild(ctx, cur)
	}

	return nil
}

func (m *buildReconciler) DeleteMachineOSConfig(ctx context.Context, mosc *mcfgv1alpha1.MachineOSConfig) error {
	return m.timeObjectOperation(mosc, "Deleting", func() error {
		return m.deleteMachineOSConfig(ctx, mosc)
	})
}

func (m *buildReconciler) deleteMachineOSConfig(ctx context.Context, mosc *mcfgv1alpha1.MachineOSConfig) error {
	klog.Infof("Removing MachineOSBuild(s) associated with non-existent MachineOSConfig %s", mosc.Name)

	mosbList, err := m.machineOSBuildLister.List(utils.MachineOSBuildForPoolSelector(mosc))
	if err != nil {
		return fmt.Errorf("could not list MachineOSBuilds for MachineOSConfig %s: %w", mosc.Name, err)
	}

	for _, mosb := range mosbList {
		if err := m.deleteMachineOSBuildAndBuilder(ctx, mosb); err != nil {
			return fmt.Errorf("could not delete MachineOSBuild %s for MachineOSConfig %s: %w", mosb.Name, mosc.Name, err)
		}
	}

	return nil
}

func (m *buildReconciler) AddPod(ctx context.Context, pod *corev1.Pod) error {
	return m.timeObjectOperation(pod, "Adding", func() error {
		return m.updateMachineOSBuildWithStatus(ctx, pod)
	})
}

func (m *buildReconciler) UpdatePod(ctx context.Context, _, curPod *corev1.Pod) error {
	return m.timeObjectOperation(curPod, "Updating", func() error {
		return m.updateMachineOSBuildWithStatus(ctx, curPod)
	})
}

func (m *buildReconciler) AddMachineOSBuild(ctx context.Context, mosb *mcfgv1alpha1.MachineOSBuild) error {
	return m.timeObjectOperation(mosb, "Adding", func() error {
		return m.startBuild(ctx, mosb)
	})
}

func (m *buildReconciler) UpdateMachineOSBuild(ctx context.Context, old, cur *mcfgv1alpha1.MachineOSBuild) error {
	return m.timeObjectOperation(cur, "Updating", func() error {
		return m.updateMachineOSBuild(ctx, old, cur)
	})
}

func (m *buildReconciler) updateMachineOSBuild(ctx context.Context, _, current *mcfgv1alpha1.MachineOSBuild) error {
	mosc, err := m.getMachineOSConfigForMachineOSBuild(current)
	if err != nil {
		return err
	}

	if apihelpers.IsMachineOSBuildConditionTrue(current.Status.Conditions, mcfgv1alpha1.MachineOSBuildSucceeded) {
		klog.Infof("MachineOSBuild %s succeeded, cleaning up all ephemeral objects used for the build", current.Name)
		if err := imagebuilder.NewPodImageBuilder(m.kubeclient, m.mcfgclient, current, mosc).Clean(ctx); err != nil {
			return err
		}

		return retry.RetryOnConflict(retry.DefaultRetry, func() error {
			newMosc, err := m.machineOSConfigLister.Get(mosc.Name)
			if err != nil {
				return err
			}

			copied := newMosc.DeepCopy()
			copied.Status.CurrentImagePullspec = current.Status.FinalImagePushspec

			_, err = m.mcfgclient.MachineconfigurationV1alpha1().MachineOSConfigs().UpdateStatus(ctx, copied, metav1.UpdateOptions{})
			if err == nil {
				klog.Infof("Updated status on MachineOSConfig %s", mosc.Name)
			}

			return err
		})
	}

	return nil
}

func (m *buildReconciler) DeleteMachineOSBuild(ctx context.Context, mosb *mcfgv1alpha1.MachineOSBuild) error {
	return m.timeObjectOperation(mosb, "Deleting", func() error {
		return m.deleteMachineOSBuildAndBuilder(ctx, mosb)
	})
}

func (m *buildReconciler) UpdateMachineConfigPool(ctx context.Context, oldMCP, curMCP *mcfgv1.MachineConfigPool) error {
	return m.timeObjectOperation(curMCP, "Updating", func() error {
		return m.updateMachineConfigPool(ctx, oldMCP, curMCP)
	})
}

func (m *buildReconciler) updateMachineConfigPool(ctx context.Context, oldMCP, curMCP *mcfgv1.MachineConfigPool) error {
	if oldMCP.Spec.Configuration.Name != curMCP.Spec.Configuration.Name {
		klog.Infof("Rendered config for pool %s changed from %s to %s", curMCP.Name, oldMCP.Spec.Configuration.Name, curMCP.Spec.Configuration.Name)
		return m.createMachineOSBuildForPoolChange(ctx, curMCP)
	}

	return nil
}

func (m *buildReconciler) getMachineOSConfigForMachineOSBuild(mosb *mcfgv1alpha1.MachineOSBuild) (*mcfgv1alpha1.MachineOSConfig, error) {
	moscName, ok := mosb.Labels[constants.MachineOSConfigNameLabelKey]
	if moscName == "" || !ok {
		return nil, fmt.Errorf("MachineOSBuild is missing label %s", constants.MachineOSConfigNameLabelKey)
	}

	mosc, err := m.machineOSConfigLister.Get(moscName)
	if err != nil {
		return nil, fmt.Errorf("could not get MachineOSConfig %s for MachineOSBuild: %w", moscName, err)
	}

	return mosc, nil
}

func (m *buildReconciler) startBuild(ctx context.Context, mosb *mcfgv1alpha1.MachineOSBuild) error {
	mosc, err := m.getMachineOSConfigForMachineOSBuild(mosb)
	if err != nil {
		return err
	}

	// If there are any other in-progress builds for this MachineOSConfig, stop them first.
	if err := m.deleteOtherBuildsForMachineOSConfig(ctx, mosb, mosc); err != nil {
		return fmt.Errorf("could not reconcile all MachineOSBuilds for MachineOSConfig %s: %w", mosc.Name, err)
	}

	// Next, create our new MachineOSBuild.
	if err := imagebuilder.NewPodImageBuilder(m.kubeclient, m.mcfgclient, mosb, mosc).Start(ctx); err != nil {
		return err
	}

	klog.Infof("Started new build %s for MachineOSBuild", utils.GetBuildPodName(mosb))

	// Update our MachineOSConfig.
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
		_, err = m.mcfgclient.MachineconfigurationV1alpha1().MachineOSConfigs().Update(ctx, moscCopy, metav1.UpdateOptions{})
		return err
	})
}

func (m *buildReconciler) createMachineOSBuildForPoolChange(ctx context.Context, mcp *mcfgv1.MachineConfigPool) error {
	moscList, err := m.machineOSConfigLister.List(labels.Everything())
	if err != nil {
		return err
	}

	for _, mosc := range moscList {
		if mosc.Spec.MachineConfigPool.Name == mcp.Name {
			return m.createMachineOSBuild(ctx, mosc.DeepCopy())
		}
	}

	klog.Infof("No MachineOSConfig found for MachineConfigPool %s", mcp.Name)

	return nil
}

func (m *buildReconciler) createMachineOSBuild(ctx context.Context, mosc *mcfgv1alpha1.MachineOSConfig) error {
	mcp, err := m.machineConfigPoolLister.Get(mosc.Spec.MachineConfigPool.Name)
	if err != nil {
		return fmt.Errorf("could not get MachineConfigPool %s for MachineOSConfig %s: %w", mosc.Spec.MachineConfigPool.Name, mosc.Name, err)
	}

	if ctrlcommon.IsPoolAnyDegraded(mcp) {
		return errors.Join(fmt.Errorf("MachineConfigPool %s is degraded", mcp.Name), ctrlcommon.ErrDropFromQueue)
	}

	osImageURLs, err := ctrlcommon.GetOSImageURLConfig(ctx, m.kubeclient)
	if err != nil {
		return fmt.Errorf("could not get OSImageURLConfig: %w", err)
	}

	imagesConfig, err := ctrlcommon.GetImagesConfig(ctx, m.kubeclient)
	if err != nil {
		return fmt.Errorf("could not get Images config: %w", err)
	}

	// Construct a new MachineOSBuild object which has the hashed name attached
	// to it.
	mosb, err := utils.NewMachineOSBuild(utils.MachineOSBuildOpts{
		MachineOSConfig:   mosc,
		MachineConfigPool: mcp,
		OSImageURLConfig:  osImageURLs,
		Images:            imagesConfig,
	})

	if err != nil {
		return fmt.Errorf("could not instantiate new MachineOSBuild: %w", err)
	}

	_, err = m.machineOSBuildLister.Get(mosb.Name)
	if k8serrors.IsNotFound(err) {
		_, err := m.mcfgclient.MachineconfigurationV1alpha1().MachineOSBuilds().Create(ctx, mosb, metav1.CreateOptions{})
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

func (m *buildReconciler) updateMachineOSBuildWithStatus(ctx context.Context, obj metav1.Object) error {
	builder, err := buildrequest.NewBuilder(obj)
	if err != nil {
		klog.Infof("%s does not have the required metadata: %s", obj.GetName(), err.Error())
		return nil
	}

	mosb, err := m.getMachineOSBuildForBuilder(builder)
	if err != nil {
		// If we can't find the MachineOSConfig at this point, it means that it was
		// probably deleted. Instead of trying to reconcile the status, we'll drop
		// it out of the execution queue.
		if k8serrors.IsNotFound(err) {
			return errors.Join(err, ctrlcommon.ErrDropFromQueue)
		}

		return err
	}

	mosc, err := m.getMachineOSConfigForBuilder(builder)
	if err != nil {
		// If we can't find the MachineOSConfig at this point, it means that it was
		// probably deleted. Instead of trying to reconcile the status, we'll drop
		// it out of the execution queue.
		if k8serrors.IsNotFound(err) {
			return errors.Join(err, ctrlcommon.ErrDropFromQueue)
		}

		return err
	}

	observer := imagebuilder.NewPodImageBuildObserver(m.kubeclient, m.mcfgclient, mosb, mosc)

	status, err := observer.MachineOSBuildStatus(ctx)
	if err != nil {
		return err
	}

	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		newMosb, err := m.machineOSBuildLister.Get(mosb.Name)
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

		_, err = m.mcfgclient.MachineconfigurationV1alpha1().MachineOSBuilds().UpdateStatus(ctx, bs.Build, metav1.UpdateOptions{})
		if err == nil {
			klog.Infof("Updated status on MachineOSBuild %s", bs.Build.Name)
		}

		return err
	})
}

func (p *buildReconciler) getMachineOSBuildForBuilder(builder buildrequest.Builder) (*mcfgv1alpha1.MachineOSBuild, error) {
	mosbName, err := builder.MachineOSBuild()
	if err != nil {
		return nil, err
	}

	mosb, err := p.machineOSBuildLister.Get(mosbName)
	if err != nil {
		return nil, fmt.Errorf("could not get MachineOSBuild %s for builder %s: %w", mosbName, builder.GetObject().GetName(), err)
	}

	return mosb.DeepCopy(), nil
}

func (p *buildReconciler) getMachineOSConfigForBuilder(builder buildrequest.Builder) (*mcfgv1alpha1.MachineOSConfig, error) {
	moscName, err := builder.MachineOSConfig()
	if err != nil {
		return nil, err
	}

	mosc, err := p.machineOSConfigLister.Get(moscName)
	if err != nil {
		return nil, fmt.Errorf("could not get MachineOSConfig %s for builder %s: %w", moscName, builder.GetObject().GetName(), err)
	}

	return mosc.DeepCopy(), nil
}

func (m *buildReconciler) deleteMachineOSBuildAndBuilder(ctx context.Context, mosb *mcfgv1alpha1.MachineOSBuild) error {
	if err := imagebuilder.NewPodImageBuildCleaner(m.kubeclient, m.mcfgclient, mosb).Clean(ctx); err != nil {
		return fmt.Errorf("could not clean build %s: %w", mosb.Name, err)
	}

	if err := m.deleteMachineOSBuild(ctx, mosb); err != nil {
		return fmt.Errorf("could not delete MachineOSBuild %s: %w", mosb.Name, err)
	}

	return nil
}

func (m *buildReconciler) deleteMachineOSBuild(ctx context.Context, mosb *mcfgv1alpha1.MachineOSBuild) error {
	moscName := mosb.Labels[constants.MachineOSConfigNameLabelKey]

	err := m.mcfgclient.MachineconfigurationV1alpha1().MachineOSBuilds().Delete(ctx, mosb.Name, metav1.DeleteOptions{})
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

func (m *buildReconciler) deleteOtherBuildsForMachineOSConfig(ctx context.Context, newMosb *mcfgv1alpha1.MachineOSBuild, mosc *mcfgv1alpha1.MachineOSConfig) error {
	mosbList, err := m.getMachineOSBuildsForMachineOSConfig(mosc)
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
			if err := m.deleteMachineOSBuildAndBuilder(ctx, mosb); err != nil {
				return fmt.Errorf("could not delete running MachineOSBuild %s: %w", mosb.Name, err)
			}
		}
	}

	return nil
}

func (m *buildReconciler) getMachineOSBuild(name string) (*mcfgv1alpha1.MachineOSBuild, error) {
	mosb, err := m.machineOSBuildLister.Get(name)
	if err == nil {
		return mosb, nil
	}

	if k8serrors.IsNotFound(err) {
		return nil, nil
	}

	return nil, err
}

func (m *buildReconciler) doesMachineOSBuildExist(name string) (bool, error) {
	mosb, err := m.getMachineOSBuild(name)
	if err != nil {
		return false, err
	}

	return mosb != nil, nil
}

func (m *buildReconciler) getMachineOSBuildsForMachineOSConfig(mosc *mcfgv1alpha1.MachineOSConfig) ([]*mcfgv1alpha1.MachineOSBuild, error) {
	sel := utils.MachineOSBuildForPoolSelector(mosc)

	mosbList, err := m.machineOSBuildLister.List(sel)
	if err != nil {
		return nil, fmt.Errorf("could not get MachineOSBuilds for MachineOSConfig %s: %w", mosc.Name, err)
	}

	return mosbList, nil
}

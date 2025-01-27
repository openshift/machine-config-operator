package main

import (
	"context"
	"fmt"
	"time"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	clientmachineconfigv1 "github.com/openshift/client-go/machineconfiguration/clientset/versioned/typed/machineconfiguration/v1"
	"github.com/openshift/machine-config-operator/devex/internal/pkg/utils"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/test/framework"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog/v2"
)

type moscOpts struct {
	poolName              string
	containerfileContents string
	pullSecretName        string
	pushSecretName        string
	finalPullSecretName   string
	finalImagePullspec    string
}

func newMachineOSConfig(opts moscOpts) *mcfgv1.MachineOSConfig {
	return &mcfgv1.MachineOSConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: opts.poolName,
			Labels: map[string]string{
				createdByOnClusterBuildsHelper: "",
			},
		},
		Spec: mcfgv1.MachineOSConfigSpec{
			MachineConfigPool: mcfgv1.MachineConfigPoolReference{
				Name: opts.poolName,
			},
			BaseImagePullSecret: &mcfgv1.ImageSecretObjectReference{
				Name: opts.pullSecretName,
			},
			RenderedImagePushSecret: mcfgv1.ImageSecretObjectReference{
				Name: opts.pushSecretName,
			},
			RenderedImagePushSpec: mcfgv1.ImageTagFormat(opts.finalImagePullspec),
			ImageBuilder: mcfgv1.MachineOSImageBuilder{
				ImageBuilderType: mcfgv1.MachineOSImageBuilderType("PodImageBuilder"),
			},
			Containerfile: []mcfgv1.MachineOSContainerfile{
				{
					ContainerfileArch: mcfgv1.NoArch,
					Content:           opts.containerfileContents,
				},
			},
		},
	}

}

func getMachineOSConfigForPool(cs *framework.ClientSet, pool *mcfgv1.MachineConfigPool) (*mcfgv1.MachineOSConfig, error) {
	client := clientmachineconfigv1.NewForConfigOrDie(cs.GetRestConfig())

	moscList, err := client.MachineOSConfigs().List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return nil, err
	}

	found := filterMachineOSConfigsForPool(moscList, pool)
	if len(found) == 1 {
		return found[0], nil
	}

	if len(found) == 0 {
		return nil, fmt.Errorf("no MachineOSConfigs exist for MachineConfigPool %s", pool.Name)
	}

	names := []string{}
	for _, mosc := range found {
		names = append(names, mosc.Name)
	}

	return nil, fmt.Errorf("expected one MachineOSConfig for MachineConfigPool %s, found multiple: %v", pool.Name, names)
}

func filterMachineOSConfigsForPool(moscList *mcfgv1.MachineOSConfigList, pool *mcfgv1.MachineConfigPool) []*mcfgv1.MachineOSConfig {
	found := []*mcfgv1.MachineOSConfig{}

	for _, mosc := range moscList.Items {
		if mosc.Spec.MachineConfigPool.Name == pool.Name {
			mosc := mosc
			found = append(found, &mosc)
		}
	}

	return found
}

func createMachineOSConfig(cs *framework.ClientSet, mosc *mcfgv1.MachineOSConfig) error {
	client := clientmachineconfigv1.NewForConfigOrDie(cs.GetRestConfig())

	_, err := client.MachineOSConfigs().Create(context.TODO(), mosc, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("could not create MachineOSConfig %s: %w", mosc.Name, err)
	}

	klog.Infof("Created MachineOSConfig %s", mosc.Name)
	return nil
}

func deleteMachineOSConfigs(cs *framework.ClientSet) error {
	client := clientmachineconfigv1.NewForConfigOrDie(cs.GetRestConfig())

	moscList, err := client.MachineOSConfigs().List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return err
	}

	for _, mosc := range moscList.Items {
		err := client.MachineOSConfigs().Delete(context.TODO(), mosc.Name, metav1.DeleteOptions{})
		if err != nil {
			return fmt.Errorf("could not delete MachineOSConfig %s: %w", mosc.Name, err)
		}

		klog.Infof("Deleted MachineOSConfig %s", mosc.Name)
	}

	return err
}

func deleteMachineOSBuilds(cs *framework.ClientSet) error {
	client := clientmachineconfigv1.NewForConfigOrDie(cs.GetRestConfig())

	mosbList, err := client.MachineOSBuilds().List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return err
	}

	for _, mosb := range mosbList.Items {
		err := client.MachineOSBuilds().Delete(context.TODO(), mosb.Name, metav1.DeleteOptions{})
		if err != nil {
			return fmt.Errorf("could not delete MachineOSBuild %s: %w", mosb.Name, err)
		}

		klog.Infof("Deleted MachineOSBuild %s", mosb.Name)
	}

	return err
}

func waitForBuildToComplete(ctx context.Context, cs *framework.ClientSet, poolName string) error {
	isExists := false
	isPending := false
	isBuilding := false
	isSuccess := false

	start := time.Now()

	return waitForMachineOSBuildToReachState(ctx, cs, poolName, func(mosb *mcfgv1.MachineOSBuild, err error) (bool, error) {
		// There is a lag between when the MachineOSConfig is created and the
		// MachineOSBuild object gets created and is available.
		if err != nil && !utils.IsNotFoundErr(err) {
			return false, err
		}

		// If the MachineOSBuild has not been created yet, try again later.
		if utils.IsNotFoundErr(err) {
			return false, nil
		}

		// If the MachineOSBuild exists, we can interrogate its state.
		if !isExists && mosb != nil && err == nil {
			isExists = true
			klog.Infof("Build %s exists after %s", mosb.Name, time.Since(start))
		}

		state := ctrlcommon.NewMachineOSBuildState(mosb)

		if !isPending && state.IsBuildPending() {
			isPending = true
			klog.Infof("Build %s is now pending after %s", mosb.Name, time.Since(start))
		}

		if !isBuilding && state.IsBuilding() {
			isBuilding = true
			klog.Infof("Build %s is now running after %s", mosb.Name, time.Since(start))
		}

		if !isSuccess && state.IsBuildSuccess() {
			isSuccess = true
			klog.Infof("Build %s is complete after %s", mosb.Name, time.Since(start))
			return true, nil
		}

		if state.IsBuildFailure() {
			return false, fmt.Errorf("build %s failed after %s", mosb.Name, time.Since(start))
		}

		return false, nil
	})
}

func waitForMachineOSBuildToReachState(ctx context.Context, cs *framework.ClientSet, poolName string, condFunc func(*mcfgv1.MachineOSBuild, error) (bool, error)) error {
	return wait.PollUntilContextCancel(ctx, time.Second, true, func(funcCtx context.Context) (bool, error) {
		mosb, err := utils.GetMachineOSBuildForPoolName(funcCtx, cs, poolName)
		return condFunc(mosb, err)
	})
}

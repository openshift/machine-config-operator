package utils

import (
	"context"
	"errors"
	"fmt"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	"github.com/openshift/machine-config-operator/test/framework"
	apierrs "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
)

type notFoundErr struct {
	poolName string
	err      error
}

func newNotFoundErr(resource, poolName string) error {
	return &notFoundErr{
		poolName: poolName,
		err:      apierrs.NewNotFound(mcfgv1.GroupVersion.WithResource(resource).GroupResource(), ""),
	}
}

func (n *notFoundErr) Error() string {
	return fmt.Sprintf("resource not found for MachineConfigPool %s: %s", n.poolName, n.err)
}

func (n *notFoundErr) Unwrap() error {
	return n.err
}

func IsNotFoundErr(err error) bool {
	notFoundErr := &notFoundErr{}
	return errors.As(err, &notFoundErr)
}

func IsMachineConfigPoolLayered(ctx context.Context, cs *framework.ClientSet, mcp *mcfgv1.MachineConfigPool) (bool, error) {
	mosc, err := GetMachineOSConfigForPool(ctx, cs, mcp)
	if err != nil && !IsNotFoundErr(err) {
		return false, err
	}

	return mosc != nil && !IsNotFoundErr(err), nil
}

func GetMachineOSBuildForPoolName(ctx context.Context, cs *framework.ClientSet, poolName string) (*mcfgv1.MachineOSBuild, error) {
	mcp, err := cs.MachineConfigPools().Get(ctx, poolName, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}

	return GetMachineOSBuildForPool(ctx, cs, mcp)
}

func GetMachineOSConfigForPoolName(ctx context.Context, cs *framework.ClientSet, poolName string) (*mcfgv1.MachineOSConfig, error) {
	mcp, err := cs.MachineConfigPools().Get(ctx, poolName, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}

	return GetMachineOSConfigForPool(ctx, cs, mcp)
}

func GetMachineOSBuildForPool(ctx context.Context, cs *framework.ClientSet, mcp *mcfgv1.MachineConfigPool) (*mcfgv1.MachineOSBuild, error) {
	mosbList, err := cs.MachineconfigurationV1Interface.MachineOSBuilds().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}

	for _, mosb := range mosbList.Items {
		mosb := mosb
		if mosb.Spec.MachineConfig.Name == mcp.Spec.Configuration.Name {
			return &mosb, nil
		}
	}

	return nil, newNotFoundErr("machineosbuilds", mcp.Name)
}

func GetMachineOSConfigForPool(ctx context.Context, cs *framework.ClientSet, mcp *mcfgv1.MachineConfigPool) (*mcfgv1.MachineOSConfig, error) {
	moscList, err := cs.MachineconfigurationV1Interface.MachineOSConfigs().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}

	for _, mosc := range moscList.Items {
		mosc := mosc
		if mosc.Spec.MachineConfigPool.Name == mcp.Name {
			return &mosc, nil
		}
	}

	return nil, newNotFoundErr("machineosconfigs", mcp.Name)
}

func PauseMachineConfigPool(ctx context.Context, cs *framework.ClientSet, poolName string) error {
	return setMachineConfigPoolPauseState(ctx, cs, poolName, true)
}

func UnpauseMachineConfigPool(ctx context.Context, cs *framework.ClientSet, poolName string) error {
	return setMachineConfigPoolPauseState(ctx, cs, poolName, false)
}

func setMachineConfigPoolPauseState(ctx context.Context, cs *framework.ClientSet, poolName string, pauseStatus bool) error {
	mcp, err := cs.MachineConfigPools().Get(ctx, poolName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("could not get MachineConfigPool %s for pausing: %w", poolName, err)
	}

	if pauseStatus {
		klog.Infof("Pausing MachineConfigPool %s", poolName)
	} else {
		klog.Infof("Unpausing MachineConfigPool %s", poolName)
	}

	mcp.Spec.Paused = pauseStatus

	_, err = cs.MachineConfigPools().Update(ctx, mcp, metav1.UpdateOptions{})
	return err
}

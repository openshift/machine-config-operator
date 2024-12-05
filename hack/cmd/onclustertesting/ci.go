package main

import (
	"context"
	"fmt"
	"time"

	"github.com/openshift/machine-config-operator/hack/internal/pkg/rollout"
	"github.com/openshift/machine-config-operator/hack/internal/pkg/utils"
	"github.com/openshift/machine-config-operator/test/framework"
	"golang.org/x/sync/errgroup"
	apierrs "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/klog/v2"
)

const (
	controlPlanePoolName string = "master"
	workerPoolName       string = "worker"
)

func runCiSetupCmd(setupOpts opts) error {
	utils.ParseFlags()

	if setupOpts.injectYumRepos && setupOpts.copyEtcPkiEntitlementSecret {
		return fmt.Errorf("flags --inject-yum-repos and --copy-etc-pki-entitlement cannot be combined")
	}

	if err := utils.CheckForBinaries([]string{"oc"}); err != nil {
		return err
	}

	cs := framework.NewClientSet("")

	if err := checkForRequiredFeatureGates(cs, setupOpts); err != nil {
		return err
	}

	if err := setupForCI(cs, setupOpts); err != nil {
		return err
	}

	klog.Infof("Setup for CI complete!")

	return nil
}

func setupForCI(cs *framework.ClientSet, setupOpts opts) error {
	start := time.Now()
	klog.Infof("Beginning setup of on-cluster layering (OCL) for CI testing")

	// If the containerfile is provided using the <() shell redirect, it will
	// only be read and applied to one MachineOSConfig. Instead, we should read
	// it once and apply it to both MachineOSConfigs.
	if setupOpts.containerfilePath != "" {
		contents, err := setupOpts.getContainerfileContent()
		if err != nil {
			return fmt.Errorf("could not get containerfile content from %s: %w", setupOpts.containerfilePath, err)
		}

		setupOpts.containerfileContents = contents
	}

	eg := errgroup.Group{}

	eg.Go(func() error {
		return createSecrets(cs, setupOpts)
	})

	pools := []string{
		workerPoolName,
		controlPlanePoolName,
	}

	for _, pool := range pools {
		pool := pool
		eg.Go(func() error {
			return setupMoscForCI(cs, setupOpts.deepCopy(), pool)
		})
	}

	if err := eg.Wait(); err != nil {
		return fmt.Errorf("could not setup MachineOSConfig for CI test: %w", err)
	}

	klog.Infof("All builds completed after %s", time.Since(start))

	for _, pool := range pools {
		if err := utils.UnpauseMachineConfigPool(context.TODO(), cs, pool); err != nil {
			return fmt.Errorf("could not unpause MachineConfigPool %s: %w", pool, err)
		}
	}

	if err := waitForPoolsToComplete(cs, pools); err != nil {
		return fmt.Errorf("pools did not complete: %w", err)
	}

	klog.Infof("Completed on-cluster layering (OCL) setup for CI testing after %s", time.Since(start))

	return nil
}

func setupMoscForCI(cs *framework.ClientSet, opts opts, poolName string) error {
	waitTime := time.Minute * 20
	ctx, cancel := context.WithTimeout(context.Background(), waitTime)
	defer cancel()

	opts.poolName = poolName

	if poolName != controlPlanePoolName && poolName != workerPoolName {
		if _, err := createPool(cs, poolName); err != nil {
			return fmt.Errorf("could not create MachineConfigPool %s: %w", poolName, err)
		}
	}

	pullspec, err := createImagestreamAndGetPullspec(cs, poolName)
	if err != nil && !apierrs.IsAlreadyExists(err) {
		return fmt.Errorf("could not create imagestream or get pullspec: %w", err)
	}

	pushSecretName, err := createLongLivedImagePushSecretForPool(context.TODO(), cs, poolName)
	if err != nil {
		return fmt.Errorf("could not create long-lived secret: %w", err)
	}

	opts.finalImagePullSecretName = pushSecretName
	opts.pushSecretName = pushSecretName
	opts.finalImagePullspec = pullspec

	mosc, err := opts.toMachineOSConfig()
	if err != nil {
		return fmt.Errorf("could not generate MachineOSConfig: %w", err)
	}

	if err := createMachineOSConfig(cs, mosc); err != nil {
		return fmt.Errorf("could not insert new MachineOSConfig %s: %w", mosc.Name, err)
	}

	// Only pause pools that are not the control plane.
	if poolName != controlPlanePoolName {
		if err := utils.PauseMachineConfigPool(ctx, cs, poolName); err != nil {
			return fmt.Errorf("could not pause MachineConfigPool %s: %w", poolName, err)
		}
	}

	if err := waitForBuildToComplete(ctx, cs, poolName); err != nil {
		return err
	}

	return nil
}

func waitForPoolsToComplete(cs *framework.ClientSet, pools []string) error {
	eg := errgroup.Group{}

	for _, pool := range pools {
		pool := pool

		eg.Go(func() error {
			return rollout.WaitForMachineConfigPoolUpdateToComplete(cs, time.Minute*30, pool)
		})
	}

	return eg.Wait()
}

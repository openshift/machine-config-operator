package main

import (
	"fmt"
	"os"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	"k8s.io/klog/v2"
)

type opts struct {
	pushSecretName           string
	pullSecretName           string
	finalImagePullSecretName string
	pushSecretPath           string
	pullSecretPath           string
	finalImagePullspec       string
	containerfilePath        string
	containerfileContents    string
	poolName                 string
	injectYumRepos           bool
	waitForBuildInfo         bool
	enableFeatureGate        bool
}

func (o *opts) deepCopy() opts {
	return opts{
		pushSecretName:           o.pushSecretName,
		pullSecretName:           o.pullSecretName,
		pushSecretPath:           o.pushSecretPath,
		pullSecretPath:           o.pullSecretPath,
		finalImagePullspec:       o.finalImagePullspec,
		finalImagePullSecretName: o.finalImagePullSecretName,
		containerfilePath:        o.containerfilePath,
		containerfileContents:    o.containerfileContents,
		poolName:                 o.poolName,
		injectYumRepos:           o.injectYumRepos,
		waitForBuildInfo:         o.waitForBuildInfo,
		enableFeatureGate:        o.enableFeatureGate,
	}
}

func (o *opts) getContainerfileContent() (string, error) {
	if o.containerfileContents != "" {
		return o.containerfileContents, nil
	}

	if o.containerfilePath == "" {
		return "", fmt.Errorf("no custom Containerfile path provided")
	}

	containerfileBytes, err := os.ReadFile(o.containerfilePath)
	if err != nil {
		return "", fmt.Errorf("cannot read Containerfile from %s: %w", o.containerfilePath, err)
	}

	klog.Infof("Using contents in Containerfile %q for %s custom Containerfile", o.containerfilePath, o.poolName)
	return string(containerfileBytes), nil
}

func (o *opts) maybeGetContainerfileContent() (string, error) {
	if o.containerfileContents != "" {
		return o.containerfileContents, nil
	}

	if o.containerfilePath == "" {
		return "", nil
	}

	return o.getContainerfileContent()
}

func (o *opts) shouldCloneGlobalPullSecret() bool {
	if o.pullSecretName == globalPullSecretCloneName && o.pullSecretPath == "" {
		return true
	}

	return isNoneSet(o.pullSecretName, o.pullSecretPath)
}

func (o *opts) toMachineOSConfig() (*mcfgv1.MachineOSConfig, error) {
	pushSecretName, err := o.getPushSecretName()
	if err != nil {
		return nil, err
	}

	pullSecretName, err := o.getPullSecretName()
	if err != nil {
		return nil, err
	}

	containerfileContents, err := o.maybeGetContainerfileContent()
	if err != nil {
		return nil, err
	}

	finalPullSecretName, err := o.getFinalPullSecretName()
	if err != nil {
		return nil, err
	}

	moscOpts := moscOpts{
		poolName:              o.poolName,
		containerfileContents: containerfileContents,
		pullSecretName:        pullSecretName,
		pushSecretName:        pushSecretName,
		finalImagePullspec:    o.finalImagePullspec,
		finalPullSecretName:   finalPullSecretName,
	}

	return newMachineOSConfig(moscOpts), nil
}

func (o *opts) getFinalPullSecretName() (string, error) {
	if o.finalImagePullSecretName == "" {
		return "", fmt.Errorf("no final image pull secret name given")
	}

	return o.finalImagePullSecretName, nil
}

func (o *opts) getPullSecretName() (string, error) {
	if o.shouldCloneGlobalPullSecret() {
		return globalPullSecretCloneName, nil
	}

	if o.pullSecretName != "" {
		return o.pullSecretName, nil
	}

	name, err := getSecretNameFromFile(o.pullSecretPath)
	if err != nil {
		return "", fmt.Errorf("could not get pull secret name from file: %w", err)
	}

	return name, nil
}

func (o *opts) getPushSecretName() (string, error) {
	if o.pushSecretName != "" {
		return o.pushSecretName, nil
	}

	name, err := getSecretNameFromFile(o.pushSecretPath)
	if err != nil {
		return "", fmt.Errorf("could not get push secret name from file: %w", err)
	}

	return name, nil
}

func (o *opts) getSecretNameParams() []string {
	secretNames := []string{}

	if o.pullSecretName != "" {
		secretNames = append(secretNames, o.pullSecretName)
	}

	if o.pushSecretName != "" {
		secretNames = append(secretNames, o.pushSecretName)
	}

	return secretNames
}

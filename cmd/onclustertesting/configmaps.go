package main

import (
	"context"

	"github.com/openshift/machine-config-operator/pkg/controller/build"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/test/framework"
	corev1 "k8s.io/api/core/v1"
	apierrs "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"
)

func createConfigMap(cs *framework.ClientSet, cm *corev1.ConfigMap) error {
	_, err := cs.CoreV1Interface.ConfigMaps(ctrlcommon.MCONamespace).Create(context.TODO(), cm, metav1.CreateOptions{})
	if err == nil {
		klog.Infof("Created ConfigMap %q in namespace %q", cm.Name, ctrlcommon.MCONamespace)
		return nil
	}

	if err != nil && !apierrs.IsAlreadyExists(err) {
		return err
	}

	configmap, err := cs.CoreV1Interface.ConfigMaps(ctrlcommon.MCONamespace).Get(context.TODO(), cm.Name, metav1.GetOptions{})
	if err != nil {
		return err
	}

	if !hasOurLabel(configmap.Labels) {
		klog.Infof("Found preexisting user-supplied ConfigMap %q, using as-is.", cm.Name)
		return nil
	}

	// Delete and recreate.
	klog.Infof("ConfigMap %q was created by us, but could be out of date. Recreating...", cm.Name)
	err = cs.CoreV1Interface.ConfigMaps(ctrlcommon.MCONamespace).Delete(context.TODO(), cm.Name, metav1.DeleteOptions{})
	if err != nil {
		return err
	}

	return createConfigMap(cs, cm)
}

type onClusterBuildConfigMapOpts struct {
	pushSecretName     string
	pullSecretName     string
	pushSecretPath     string
	pullSecretPath     string
	finalImagePullspec string
}

func (o *onClusterBuildConfigMapOpts) shouldCloneGlobalPullSecret() bool {
	return isNoneSet(o.pullSecretName, o.pullSecretPath)
}

func (o *onClusterBuildConfigMapOpts) toConfigMap() (*corev1.ConfigMap, error) {
	pushSecretName, err := o.getPushSecretName()
	if err != nil {
		return nil, err
	}

	pullSecretName, err := o.getPullSecretName()
	if err != nil {
		return nil, err
	}

	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      build.OnClusterBuildConfigMapName,
			Namespace: ctrlcommon.MCONamespace,
			Labels: map[string]string{
				createdByOnClusterBuildsHelper: "",
			},
		},
		Data: map[string]string{
			build.BaseImagePullSecretNameConfigKey:  pullSecretName,
			build.FinalImagePushSecretNameConfigKey: pushSecretName,
			build.FinalImagePullspecConfigKey:       o.finalImagePullspec,
		},
	}

	return cm, nil
}

func (o *onClusterBuildConfigMapOpts) getPullSecretName() (string, error) {
	if o.shouldCloneGlobalPullSecret() {
		return globalPullSecretCloneName, nil
	}

	if o.pullSecretName != "" {
		return o.pullSecretName, nil
	}

	return getSecretNameFromFile(o.pullSecretPath)
}

func (o *onClusterBuildConfigMapOpts) getPushSecretName() (string, error) {
	if o.pushSecretName != "" {
		return o.pushSecretName, nil
	}

	return getSecretNameFromFile(o.pushSecretPath)
}

func (o *onClusterBuildConfigMapOpts) getSecretNameParams() []string {
	secretNames := []string{}

	if o.pullSecretName != "" {
		secretNames = append(secretNames, o.pullSecretName)
	}

	if o.pushSecretName != "" {
		secretNames = append(secretNames, o.pushSecretName)
	}

	return secretNames
}

func createOnClusterBuildConfigMap(cs *framework.ClientSet, opts onClusterBuildConfigMapOpts) error {
	cm, err := opts.toConfigMap()
	if err != nil {
		return err
	}

	return createConfigMap(cs, cm)
}

func createCustomDockerfileConfigMap(cs *framework.ClientSet) error {
	pools, err := cs.MachineconfigurationV1Interface.MachineConfigPools().List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return err
	}

	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "on-cluster-build-custom-dockerfile",
			Namespace: ctrlcommon.MCONamespace,
			Labels: map[string]string{
				createdByOnClusterBuildsHelper: "",
			},
		},
		Data: map[string]string{},
	}

	for _, pool := range pools.Items {
		cm.Data[pool.Name] = ""
	}

	return createConfigMap(cs, cm)
}

func cleanupConfigMaps(cs *framework.ClientSet) error {
	configMaps, err := cs.CoreV1Interface.ConfigMaps(ctrlcommon.MCONamespace).List(context.TODO(), getListOptsForOurLabel())

	if err != nil {
		return err
	}

	for _, configMap := range configMaps.Items {
		if err := cs.CoreV1Interface.ConfigMaps(ctrlcommon.MCONamespace).Delete(context.TODO(), configMap.Name, metav1.DeleteOptions{}); err != nil {
			return err
		}
		klog.Infof("Deleted ConfigMap %q from namespace %q", configMap.Name, ctrlcommon.MCONamespace)
	}

	return nil
}

func forceCleanupConfigMaps(cs *framework.ClientSet) error {
	configMaps, err := cs.CoreV1Interface.ConfigMaps(ctrlcommon.MCONamespace).List(context.TODO(), metav1.ListOptions{})

	if err != nil {
		return err
	}

	toDelete := sets.NewString("on-cluster-build-config", "on-cluster-build-custom-dockerfile")

	for _, configMap := range configMaps.Items {
		if toDelete.Has(configMap.Name) {
			if err := cs.CoreV1Interface.ConfigMaps(ctrlcommon.MCONamespace).Delete(context.TODO(), configMap.Name, metav1.DeleteOptions{}); err != nil {
				return err
			}

			klog.Infof("Deleted ConfigMap %q from namespace %q", configMap.Name, ctrlcommon.MCONamespace)
		}
	}

	return nil
}

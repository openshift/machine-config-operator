package main

import (
	"context"

	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/test/framework"

	corev1 "k8s.io/api/core/v1"
	apierrs "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
)

func createConfigMap(cs *framework.ClientSet, cm *corev1.ConfigMap) error { //nolint:dupl // These are ConfigMaps.
	if !hasOurLabel(cm.Labels) {
		if cm.Labels == nil {
			cm.Labels = map[string]string{}
		}

		cm.Labels[createdByOnClusterBuildsHelper] = ""
	}

	_, err := cs.CoreV1Interface.ConfigMaps(ctrlcommon.MCONamespace).Create(context.TODO(), cm, metav1.CreateOptions{})
	if err == nil {
		klog.Infof("Created ConfigMap %q in namespace %q", cm.Name, ctrlcommon.MCONamespace)
		return nil
	}

	if err != nil && !apierrs.IsAlreadyExists(err) {
		return err
	}

	configMap, err := cs.CoreV1Interface.ConfigMaps(ctrlcommon.MCONamespace).Get(context.TODO(), cm.Name, metav1.GetOptions{})
	if err != nil {
		return err
	}

	if !hasOurLabel(configMap.Labels) {
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

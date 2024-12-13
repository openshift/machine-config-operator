package main

import (
	"context"

	imagev1 "github.com/openshift/api/image/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/test/framework"
	apierrs "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
)

func createImagestream(cs *framework.ClientSet, name string) error {
	is := &imagev1.ImageStream{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ctrlcommon.MCONamespace,
		},
	}

	_, err := cs.ImageV1Interface.ImageStreams(ctrlcommon.MCONamespace).Create(context.TODO(), is, metav1.CreateOptions{})
	if err == nil {
		klog.Infof("Imagestream %q created", name)
		return nil
	}

	if apierrs.IsAlreadyExists(err) {
		klog.Infof("Imagestream %q already exists, will re-use", name)
		return nil
	}

	return err
}

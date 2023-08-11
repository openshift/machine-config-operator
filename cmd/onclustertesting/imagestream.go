package main

import (
	"context"
	"fmt"

	"github.com/docker/distribution/reference"
	imagev1 "github.com/openshift/api/image/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/test/framework"
	apierrs "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
)

const (
	imagestreamName     string = "os-image"
	imagestreamPullspec string = "image-registry.openshift-image-registry.svc:5000/" + ctrlcommon.MCONamespace + "/" + imagestreamName + ":latest"
)

func getImagestreamPullspec(cs *framework.ClientSet, name string) (string, error) {
	is, err := cs.ImageV1Interface.ImageStreams(ctrlcommon.MCONamespace).Get(context.TODO(), name, metav1.GetOptions{})
	if err != nil {
		return "", err
	}

	return appendTagToPullspec(is.Status.DockerImageRepository, "latest")
}

// Not sure if htis is strictly required, but we'll do it anyway.
func appendTagToPullspec(pullspec, tag string) (string, error) {
	named, err := reference.ParseNamed(pullspec)
	if err != nil {
		return "", fmt.Errorf("could not parse %s: %w", pullspec, err)
	}

	tagged, err := reference.WithTag(named, tag)
	if err != nil {
		return "", fmt.Errorf("could not add tag %s to image pullspec %s: %w", tag, pullspec, err)
	}

	return tagged.String(), nil
}

func createImagestream(cs *framework.ClientSet, name string) error {
	is := &imagev1.ImageStream{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ctrlcommon.MCONamespace,
			Labels: map[string]string{
				createdByOnClusterBuildsHelper: "",
			},
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

func cleanupImagestreams(cs *framework.ClientSet) error {
	isList, err := cs.ImageV1Interface.ImageStreams(ctrlcommon.MCONamespace).List(context.TODO(), getListOptsForOurLabel())
	if err != nil {
		return err
	}

	for _, is := range isList.Items {
		if err := deleteImagestream(cs, is.Name); err != nil {
			return err
		}
	}

	return nil
}

func deleteImagestream(cs *framework.ClientSet, name string) error {
	err := cs.ImageV1Interface.ImageStreams(ctrlcommon.MCONamespace).Delete(context.TODO(), name, metav1.DeleteOptions{})
	if err == nil {
		klog.Infof("Imagestream %q deleted", name)
	}

	if err != nil && apierrs.IsNotFound(err) {
		klog.Infof("Imagestream %q not found", name)
		return nil
	}

	return err
}

package main

import (
	"context"
	"fmt"

	"github.com/containers/image/v5/docker/reference"
	imagev1 "github.com/openshift/api/image/v1"
	commonconsts "github.com/openshift/machine-config-operator/pkg/controller/common/constants"
	"github.com/openshift/machine-config-operator/test/framework"
	apierrs "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
)

const (
	imagestreamName     string = "os-image"
	imagestreamPullspec string = "image-registry.openshift-image-registry.svc:5000/" + commonconsts.MCONamespace + "/" + imagestreamName + ":latest"
)

func createImagestreamAndGetPullspec(cs *framework.ClientSet, name string) (string, error) {
	if err := createImagestream(cs, name); err != nil {
		return "", err
	}

	return getImagestreamPullspec(cs, name)
}

func getImagestreamPullspec(cs *framework.ClientSet, name string) (string, error) {
	is, err := cs.ImageV1Interface.ImageStreams(commonconsts.MCONamespace).Get(context.TODO(), name, metav1.GetOptions{})
	if err != nil {
		return "", err
	}

	return appendTagToPullspec(is.Status.DockerImageRepository, "latest")
}

// Not sure if this is strictly required, but we'll do it anyway.
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
			Namespace: commonconsts.MCONamespace,
			Labels: map[string]string{
				createdByOnClusterBuildsHelper: "",
			},
		},
	}

	created, err := cs.ImageV1Interface.ImageStreams(commonconsts.MCONamespace).Create(context.TODO(), is, metav1.CreateOptions{})
	if err == nil {
		klog.Infof("Imagestream %q created", name)
		return nil
	}

	if apierrs.IsAlreadyExists(err) && hasOurLabel(created.Labels) {
		klog.Infof("Imagestream %q already exists and has our labels, will re-use", name)
		return nil
	}

	return err
}

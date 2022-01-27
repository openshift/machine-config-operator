package e2e_test

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"testing"

	buildv1 "github.com/openshift/api/build/v1"
	imagev1 "github.com/openshift/api/image/v1"
	"github.com/openshift/machine-config-operator/test/constants"
	"github.com/openshift/machine-config-operator/test/framework"
	"github.com/openshift/machine-config-operator/test/helpers"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/kubernetes/pkg/credentialprovider"
)

const (
	imageStreamName = "test-boot-in-cluster-image"
	buildName       = imageStreamName
	// ostreeUnverifiedRegistry means no GPG or container signatures are used.
	// Right now we're usually relying on digested pulls. See
	// https://github.com/openshift/machine-config-operator/blob/master/docs/OSUpgrades.md#questions-and-answers around integrity.
	// See https://docs.rs/ostree-ext/0.5.1/ostree_ext/container/struct.OstreeImageReference.html
	ostreeUnverifiedRegistry = "ostree-unverified-registry"
	imageRegistry            = "image-registry.openshift-image-registry.svc:5000"
	// If this moves from /run, make sure files get cleaned up
	authfilePath = "/run/ostree/auth.json"
)

type Deployments struct {
	booted                  bool
	ContainerImageReference string `json:"container-image-reference"`
}
type Status struct {
	deployments []Deployments
}

func TestBootInClusterImage(t *testing.T) {
	cs := framework.NewClientSet("")

	// create a new image stream
	ctx := context.TODO()
	imageStreamConfig := &imagev1.ImageStream{
		ObjectMeta: metav1.ObjectMeta{
			Name:      imageStreamName,
			Namespace: constants.MCONamespace,
		},
	}
	_, err := cs.ImageStreams(constants.MCONamespace).Create(ctx, imageStreamConfig, metav1.CreateOptions{})
	require.Nil(t, err)
	defer cs.ImageStreams(constants.MCONamespace).Delete(ctx, imageStreamName, metav1.DeleteOptions{})

	// push a build to the image stream
	buildConfig := &buildv1.Build{
		ObjectMeta: metav1.ObjectMeta{
			Name: buildName,
		},
		Spec: buildv1.BuildSpec{
			CommonSpec: buildv1.CommonSpec{
				Source: buildv1.BuildSource{
					Type: "Git",
					Git: &buildv1.GitBuildSource{
						URI: "https://github.com/mkenigs/fcos-derivation-example",
						Ref: "rhcos-openshift-builder",
					},
				},
				Strategy: buildv1.BuildStrategy{
					DockerStrategy: &buildv1.DockerBuildStrategy{},
				},
				Output: buildv1.BuildOutput{
					To: &v1.ObjectReference{
						Kind: "ImageStreamTag",
						Name: imageStreamName + ":latest",
					},
				},
			},
		},
	}
	_, err = cs.BuildV1Interface.Builds(constants.MCONamespace).Create(ctx, buildConfig, metav1.CreateOptions{})
	require.Nil(t, err)
	defer cs.BuildV1Interface.Builds(constants.MCONamespace).Delete(ctx, buildName, metav1.DeleteOptions{})
	helpers.WaitForBuild(t, cs, buildConfig.ObjectMeta.Name)

	// pick a random worker node
	unlabelFunc := helpers.LabelRandomNodeFromPool(t, cs, "worker", "node-role.kubernetes.io/infra")
	defer unlabelFunc()
	infraNode := helpers.GetSingleNodeByRole(t, cs, "infra")

	// get ImagePullSecret for the MCD service account and save to authfilePath on the node
	// eventually we should use rest.InClusterConfig() instead of cs with kubeadmin
	mcdServiceAccount, err := cs.ServiceAccounts(constants.MCONamespace).Get(ctx, "machine-config-daemon", metav1.GetOptions{})
	require.Nil(t, err)
	require.Equal(t, 1, len(mcdServiceAccount.ImagePullSecrets))
	imagePullSecret, err := cs.Secrets(constants.MCONamespace).Get(ctx, mcdServiceAccount.ImagePullSecrets[0].Name, metav1.GetOptions{})
	dockerConfigData := imagePullSecret.Data[corev1.DockerConfigKey]
	var dockerConfig credentialprovider.DockerConfig
	err = json.Unmarshal(dockerConfigData, &dockerConfig)
	require.Nil(t, err)
	dockerConfigJSON := credentialprovider.DockerConfigJSON{
		Auths: dockerConfig,
	}
	authfileData, err := json.Marshal(dockerConfigJSON)
	require.Nil(t, err)
	helpers.ExecCmdOnNode(t, cs, infraNode, "mkdir", "-p", path.Dir(path.Join("/rootfs", authfilePath)))
	// will get cleaned up on reboot since file is in /run
	helpers.WriteToMCDContainer(t, cs, infraNode, path.Join("/rootfs", authfilePath), authfileData)

	// rpm-ostree rebase --experimental ostree-unverified-image:docker://image-registry.openshift-image-registry.svc.cluster.local:5000/openshift-machine-config-operator/test-boot-in-cluster-image-build
	imageURL := fmt.Sprintf("%s:%s/%s/%s", ostreeUnverifiedRegistry, imageRegistry, constants.MCONamespace, imageStreamName)
	helpers.ExecCmdOnNode(t, cs, infraNode, "chroot", "/rootfs", "rpm-ostree", "rebase", "--experimental", imageURL)
	// reboot
	helpers.RebootAndWait(t, cs, infraNode)
	// check that new image is used
	checkUsingImage := func(usingImage bool) {
		status := helpers.ExecCmdOnNode(t, cs, infraNode, "chroot", "/rootfs", "rpm-ostree", "status", "--json")
		var statusJSON Status
		err = json.Unmarshal([]byte(status), &statusJSON)
		require.Nil(t, err)
		for _, deployment := range statusJSON.deployments {
			if deployment.booted {
				if usingImage {
					require.Equal(t, imageURL, deployment.ContainerImageReference)
				} else {
					require.NotEqual(t, imageURL, deployment.ContainerImageReference)
				}
			}
		}
	}
	checkUsingImage(true)
	// rollback
	helpers.ExecCmdOnNode(t, cs, infraNode, "chroot", "/rootfs", "rpm-ostree", "rollback")
	helpers.RebootAndWait(t, cs, infraNode)
	checkUsingImage(false)
}

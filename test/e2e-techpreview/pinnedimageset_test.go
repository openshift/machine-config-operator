package e2e_techpreview

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"

	imagev1 "github.com/openshift/api/image/v1"
	"github.com/openshift/api/machineconfiguration/v1alpha1"
	"github.com/openshift/machine-config-operator/pkg/daemon/constants"
	"github.com/openshift/machine-config-operator/test/framework"
	"github.com/openshift/machine-config-operator/test/helpers"
	"github.com/stretchr/testify/require"
)

const (
	timeOut = 5 * time.Minute
)

func TestPinnedImageHappyPath(t *testing.T) {
	require := require.New(t)
	ctx, cancel := context.WithTimeout(context.Background(), timeOut)
	defer cancel()

	cs := framework.NewClientSet("")

	pinnedImageSetSize := 10
	pinnedImageSetName := "worker-test"
	pinnedImageSetPool := "worker"
	imageSetVersion := "4.15" // version is hardcoded we just need something from the past or future.

	release, err := getReleasePayloadByRelease(imageSetVersion)
	require.NoError(err)

	pinnedImageSet, err := createPinnedImageSetFromRelease(release, pinnedImageSetName, pinnedImageSetPool, pinnedImageSetSize)
	require.NoError(err)

	for _, image := range pinnedImageSet.Spec.PinnedImages {
		t.Logf("image: %s", image.Name)
	}

	_, err = cs.PinnedImageSets().Create(context.Background(), pinnedImageSet, metav1.CreateOptions{})
	require.NoError(err)

	t.Cleanup(func() {
		err := cs.PinnedImageSets().Delete(context.Background(), pinnedImageSetName, metav1.DeleteOptions{})
		require.NoError(err)
	})

	// validate that the pinned image set status is updated correctly
	err = wait.PollUntilContextCancel(ctx, 1*time.Second, true, func(ctx context.Context) (bool, error) {
		return verifyMachineConfigNodeStatus(ctx, cs, pinnedImageSetPool, pinnedImageSetName)
	})
	require.NoError(err)

	// validate that the machine config pool synchronizer status is updated correctly
	err = wait.PollUntilContextCancel(ctx, 1*time.Second, true, func(ctx context.Context) (bool, error) {
		return verifyMachineConfigPoolStatus(ctx, cs, pinnedImageSetPool)
	})
	require.NoError(err)

	// validate that the pinned image set config is applied to the nodes
	nodes, err := getNodesForPool(ctx, cs, pinnedImageSetPool)
	require.NoError(err)
	for _, node := range nodes {
		config, err := helpers.ExecCmdOnNodeWithError(cs, node, "cat", fmt.Sprintf("/rootfs/%s",constants.CrioPinnedImagesDropInFilePath))
		require.NoError(err)
		require.True(strings.Contains(config, pinnedImageSet.Spec.PinnedImages[0].Name))
	}

	// TODO: validate that the images are pinned
}

func getReleasePayloadByRelease(release string) (string, error) {
	// use okd so we don't need to worry about auth
	url := fmt.Sprintf("https://amd64.origin.releases.ci.openshift.org/api/v1/releasestream/%s.0-0.okd/latest", release)

	resp, err := http.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", err
	}

	return result["pullSpec"].(string), nil
}

func createPinnedImageSetFromRelease(payload, name, pool string, count int) (*v1alpha1.PinnedImageSet, error) {
	args := fmt.Sprintf("oc adm release info %s -o json", payload)
	stdout, err := runCmd(args)
	if err != nil {
		return nil, err
	}

	type Stream struct {
		References imagev1.ImageStream `json:"references"`
	}

	var stream Stream
	if err := json.Unmarshal(stdout.Bytes(), &stream); err != nil {
		return nil, err
	}

	var images []v1alpha1.PinnedImageRef
	for i, tag := range stream.References.Spec.Tags {
		if i >= count {
			break
		}
		images = append(images, v1alpha1.PinnedImageRef{
			Name: tag.From.Name,
		})
	}

	return &v1alpha1.PinnedImageSet{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			Labels: map[string]string{
				"machineconfiguration.openshift.io/role": pool,
			},
		},
		Spec: v1alpha1.PinnedImageSetSpec{
			PinnedImages: images,
		},
	}, nil
}

func verifyMachineConfigNodeStatus(ctx context.Context, cs *framework.ClientSet, poolName, pinnedImageSetName string) (bool, error) {
	mcns, err := cs.MachineConfigNodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return false, err
	}

	for _, mcn := range mcns.Items {
		if mcn.Spec.Pool.Name != poolName {
			continue
		}
		if mcn.Status.PinnedImageSets == nil {
			return false, nil
		}
		for _, set := range mcn.Status.PinnedImageSets {
			if set.Name == pinnedImageSetName && set.CurrentGeneration == 1 && set.DesiredGeneration == 1 {
				return true, nil
			}
		}
	}
	return false, nil
}

func verifyMachineConfigPoolStatus(ctx context.Context, cs *framework.ClientSet, poolName string) (bool, error) {
	mcns, err := cs.MachineConfigNodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return false, err
	}
	nodeCount := int64(0)
	for _, mcn := range mcns.Items {
		if mcn.Spec.Pool.Name != poolName {
			continue
		}
		nodeCount++
	}

	mcp, err := cs.MachineConfigPools().Get(ctx, poolName, metav1.GetOptions{})
	if err != nil {
		return false, err
	}

	if len(mcp.Status.PoolSynchronizersStatus) == 0 {
		return false, nil
	}
	for _, status := range mcp.Status.PoolSynchronizersStatus {
		if status.UpdatedMachineCount != nodeCount {
			return false, nil
		}
	}
	return true, nil
}

func getNodesForPool(ctx context.Context, cs *framework.ClientSet, poolName string) ([]corev1.Node, error) {
	mcns, err := cs.MachineConfigNodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	
	nodes := []corev1.Node{}
	for _, mcn := range mcns.Items {
		if mcn.Spec.Pool.Name != poolName {
			continue
		}
		nodeName := mcn.Name
		node := corev1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Name: nodeName,
			},
		}
		nodes = append(nodes, node)
	}
	
	return nodes, nil
}

func runCmd(args string) (*bytes.Buffer, error) {
	stdout := bytes.NewBuffer([]byte{})
	stderr := bytes.NewBuffer([]byte{})
	cmd := exec.Command("bash", "-c", args)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	err := cmd.Run()
	if err != nil {
		return nil, fmt.Errorf("got stdout: %s and stderr: %s", stdout.String(), stderr.String())
	}
	return stdout, nil
}

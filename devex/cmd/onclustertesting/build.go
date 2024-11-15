package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/openshift/machine-config-operator/devex/internal/pkg/utils"
	commonconsts "github.com/openshift/machine-config-operator/pkg/controller/common/constants"
	"github.com/openshift/machine-config-operator/test/framework"
	"golang.org/x/sync/errgroup"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
)

const (
	yumReposContainerImagePullspec string = "quay.io/zzlotnik/devex:epel"
)

func extractAndInjectYumEpelRepos(cs *framework.ClientSet) error {
	eg := errgroup.Group{}

	eg.Go(func() error {
		yumReposContents, err := convertFilesFromContainerImageToBytesMap(yumReposContainerImagePullspec, "/etc/yum.repos.d/")
		if err != nil {
			return err
		}

		return createConfigMap(cs, &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "etc-yum-repos-d",
				Namespace: commonconsts.MCONamespace,
			},
			// Note: Even though the BuildController retrieves this ConfigMap, it only
			// does so to determine whether or not it is present. It does not look at
			// its contents. For that reason, we can use the BinaryData field here
			// because the Build Pod will use its contents the same regardless of
			// whether its string data or binary data.
			BinaryData: yumReposContents,
		})
	})

	eg.Go(func() error {
		rpmGpgContents, err := convertFilesFromContainerImageToBytesMap(yumReposContainerImagePullspec, "/etc/pki/rpm-gpg/")
		if err != nil {
			return err
		}

		return utils.CreateOrRecreateSecret(cs, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "etc-pki-rpm-gpg",
				Namespace: commonconsts.MCONamespace,
				Labels: map[string]string{
					createdByOnClusterBuildsHelper: "",
				},
			},
			Data: rpmGpgContents,
		})
	})

	return eg.Wait()
}

// Extracts the contents of a directory within a given container to a temporary
// directory. Next, it loads them into a bytes map keyed by filename. It does
// not handle nested directories, so use with caution.
func convertFilesFromContainerImageToBytesMap(pullspec, containerFilepath string) (map[string][]byte, error) {
	tempDir, err := os.MkdirTemp("", "prefix")
	if err != nil {
		return nil, err
	}

	path := fmt.Sprintf("%s:%s", containerFilepath, tempDir)
	cmd := exec.Command("oc", "image", "extract", pullspec, "--path", path)
	klog.Infof("Extracting files under %q from %q to %q; running %s", containerFilepath, pullspec, tempDir, cmd.String())
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return nil, err
	}

	out := map[string][]byte{}

	err = filepath.Walk(tempDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if info.IsDir() {
			return nil
		}

		contents, err := os.ReadFile(path)
		if err != nil {
			return err
		}

		out[filepath.Base(path)] = contents
		return nil
	})

	if err != nil {
		return nil, err
	}

	err = os.RemoveAll(tempDir)
	if err == nil {
		klog.Infof("Tempdir %q from fetching files from %q removed", tempDir, pullspec)
	}

	return out, err
}

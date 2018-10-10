package operator

import (
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"

	"github.com/golang/glog"
	installertypes "github.com/openshift/installer/pkg/types"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/scheme"
)

// RenderBootstrap writes to destinationDir static Pods.
func RenderBootstrap(
	clusterConfigConfigMapFile string,
	etcdCAFile, rootCAFile string,
	imgs Images,
	destinationDir string,
) error {
	filesData := map[string][]byte{}
	files := []string{
		clusterConfigConfigMapFile,
		rootCAFile,
		etcdCAFile,
	}
	for _, file := range files {
		data, err := ioutil.ReadFile(file)
		if err != nil {
			return err
		}
		filesData[file] = data
	}

	mcoconfig, err := discoverMCOConfig(getInstallConfigFromFile(filesData[clusterConfigConfigMapFile]))
	if err != nil {
		return fmt.Errorf("error discovering MCOConfig from %q: %v", clusterConfigConfigMapFile, err)
	}

	config := getRenderConfig(mcoconfig, filesData[etcdCAFile], filesData[rootCAFile], imgs)

	manifests := []struct {
		name     string
		filename string
	}{{
		name:     "manifests/machineconfigcontroller/controllerconfig.yaml",
		filename: "manifests/machineconfigcontroller-controllerconfig.yaml",
	}, {
		name:     "manifests/master.machineconfigpool.yaml",
		filename: "manifests/master.machineconfigpool.yaml",
	}, {
		name:     "manifests/worker.machineconfigpool.yaml",
		filename: "manifests/worker.machineconfigpool.yaml",
	}, {
		name:     "manifests/bootstrap-pod.yaml",
		filename: "machineconfigoperator-bootstrap-pod.yaml",
	}}
	for _, m := range manifests {
		glog.Info(m)
		b, err := renderAsset(config, m.name)
		if err != nil {
			return err
		}

		path := filepath.Join(destinationDir, m.filename)
		dirname := filepath.Dir(path)
		if err := os.MkdirAll(dirname, 0655); err != nil {
			return err
		}
		if err := ioutil.WriteFile(path, b, 0655); err != nil {
			return err
		}
	}
	return nil
}

func getInstallConfigFromFile(cmData []byte) installConfigGetter {
	return func() (installertypes.InstallConfig, error) {
		obji, err := runtime.Decode(scheme.Codecs.UniversalDecoder(corev1.SchemeGroupVersion), cmData)
		if err != nil {
			return installertypes.InstallConfig{}, err
		}
		cm, ok := obji.(*corev1.ConfigMap)
		if !ok {
			return installertypes.InstallConfig{}, fmt.Errorf("expected *corev1.ConfigMap found %T", obji)
		}

		return icFromClusterConfig(cm)
	}
}

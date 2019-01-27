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
	etcdCAFile, rootCAFile string, pullSecretFile string,
	imgs Images, osimgConfigMapFile string,
	destinationDir string,
) error {
	filesData := map[string][]byte{}
	files := []string{
		clusterConfigConfigMapFile,
		rootCAFile,
		etcdCAFile,
	}
	if pullSecretFile != "" {
		files = append(files, pullSecretFile)
	}
	for _, file := range files {
		data, err := ioutil.ReadFile(file)
		if err != nil {
			return err
		}
		filesData[file] = data
	}

	var osimageurl string
	if osimgConfigMapFile != "" {
		cm, err := DecodeConfigMap(osimgConfigMapFile)
		if err != nil {
			return err
		}
		osimageurl = cm.Data["osImageURL"]
	}

	mcoconfig, err := discoverMCOConfig(getInstallConfigFromFile(filesData[clusterConfigConfigMapFile]))
	if err != nil {
		return fmt.Errorf("error discovering MCOConfig from %q: %v", clusterConfigConfigMapFile, err)
	}

	config := getRenderConfig(mcoconfig, filesData[etcdCAFile], filesData[rootCAFile], nil, imgs, osimageurl)

	manifests := []struct {
		name     string
		data     []byte
		filename string
	}{{
		name:     "manifests/machineconfigcontroller/controllerconfig.yaml",
		filename: "bootstrap/manifests/machineconfigcontroller-controllerconfig.yaml",
	}, {
		name:     "manifests/master.machineconfigpool.yaml",
		filename: "bootstrap/manifests/master.machineconfigpool.yaml",
	}, {
		name:     "manifests/worker.machineconfigpool.yaml",
		filename: "bootstrap/manifests/worker.machineconfigpool.yaml",
	}, {
		name:     "manifests/bootstrap-pod-v2.yaml",
		filename: "bootstrap/machineconfigoperator-bootstrap-pod.yaml",
	}, {
		data:     filesData[pullSecretFile],
		filename: "bootstrap/manifests/machineconfigcontroller-pull-secret",
	}, {
		name:     "manifests/machineconfigserver/csr-approver-role-binding.yaml",
		filename: "manifests/csr-approver-role-binding.yaml",
	}, {
		name:     "manifests/machineconfigserver/csr-bootstrap-role-binding.yaml",
		filename: "manifests/csr-bootstrap-role-binding.yaml",
	}}
	for _, m := range manifests {
		glog.Info(m)

		var b []byte
		var err error
		if len(m.name) > 0 {
			b, err = renderAsset(config, m.name)
			if err != nil {
				return err
			}
		} else if len(m.data) > 0 {
			b = m.data
		} else {
			continue
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

/// DecodeConfigMap converts a file on disk into a ConfigMap
func DecodeConfigMap(file string) (*corev1.ConfigMap, error) {
	data, err := ioutil.ReadFile(file)
	if err != nil {
		return nil, err
	}
	obji, err := runtime.Decode(scheme.Codecs.UniversalDecoder(corev1.SchemeGroupVersion), data)
	if err != nil {
		return nil, err
	}
	cm, ok := obji.(*corev1.ConfigMap)
	if !ok {
		return nil, fmt.Errorf("expected *corev1.ConfigMap found %T", obji)
	}
	return cm, nil
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

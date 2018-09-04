package operator

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"path/filepath"

	"github.com/golang/glog"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/scheme"

	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	mcfgscheme "github.com/openshift/machine-config-operator/pkg/generated/clientset/versioned/scheme"
)

// RenderBootstrap writes to destinationDir static Pods.
func RenderBootstrap(
	configPath string,
	etcdCAFile, rootCAFile string,
	imagesConfigMapFile string,
	destinationDir string,
) error {
	filesData := map[string][]byte{}
	files := []string{
		configPath,
		rootCAFile,
		etcdCAFile,
		imagesConfigMapFile,
	}
	for _, file := range files {
		data, err := ioutil.ReadFile(file)
		if err != nil {
			return err
		}
		filesData[file] = data
	}

	obji, err := runtime.Decode(mcfgscheme.Codecs.UniversalDecoder(mcfgv1.SchemeGroupVersion), filesData[configPath])
	if err != nil {
		return err
	}
	mcoconfig, ok := obji.(*mcfgv1.MCOConfig)
	if !ok {
		return fmt.Errorf("expected *MCOConfig found %T", obji)
	}

	obji, err = runtime.Decode(scheme.Codecs.UniversalDecoder(corev1.SchemeGroupVersion), filesData[imagesConfigMapFile])
	if err != nil {
		return err
	}
	icm, ok := obji.(*corev1.ConfigMap)
	if !ok {
		return fmt.Errorf("expected *corev1.ConfigMap found %T", obji)
	}
	imgsRaw := icm.Data["images.json"]
	var imgs images
	if err := json.Unmarshal([]byte(imgsRaw), &imgs); err != nil {
		return err
	}

	config := getRenderConfig(mcoconfig, filesData[etcdCAFile], filesData[rootCAFile], imgs)

	manifests := []struct {
		name     string
		filename string
	}{{
		name:     "manifests/machineconfigcontroller/controllerconfig.yaml",
		filename: "machineconfigcontroller-controllerconfig.yaml",
	}, {
		name:     "manifests/machineconfigcontroller/bootstrap-pod.yaml",
		filename: "machineconfigcontroller-bootstrap-pod.yaml",
	}, {
		name:     "manifests/master.machineconfigpool.yaml",
		filename: "master.machineconfigpool.yaml",
	}, {
		name:     "manifests/worker.machineconfigpool.yaml",
		filename: "worker.machineconfigpool.yaml",
	}, {
		name:     "manifests/etcd.machineconfigpool.yaml",
		filename: "etcd.machineconfigpool.yaml",
	}, {
		name:     "manifests/machineconfigserver/bootstrap-pod.yaml",
		filename: "machineconfigserver-bootstrap-pod.yaml",
	}, {
		name:     "manifests/controllerconfig.crd.yaml",
		filename: "controllerconfig.crd.yaml",
	}, {
		name:     "manifests/machineconfig.crd.yaml",
		filename: "machineconfig.crd.yaml",
	}, {
		name:     "manifests/machineconfigpool.crd.yaml",
		filename: "machineconfigpool.crd.yaml",
	}}
	for _, m := range manifests {
		glog.Info(m)
		b, err := renderAsset(config, m.name)
		if err != nil {
			return err
		}

		path := filepath.Join(destinationDir, m.filename)
		if err := ioutil.WriteFile(path, b, 0655); err != nil {
			return err
		}
	}
	return nil
}

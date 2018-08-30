package operator

import (
	"fmt"
	"io/ioutil"
	"path/filepath"

	"github.com/golang/glog"
	"k8s.io/apimachinery/pkg/runtime"

	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	mcfgscheme "github.com/openshift/machine-config-operator/pkg/generated/clientset/versioned/scheme"
)

// RenderBootstrap writes to destinationDir static Pods.
func RenderBootstrap(
	configPath string,
	etcdCAFile, rootCAFile string,
	destinationDir string,
) error {
	raw, err := ioutil.ReadFile(configPath)
	if err != nil {
		return err
	}
	var casData [][]byte
	cas := []string{
		rootCAFile,
		etcdCAFile,
	}
	for _, ca := range cas {
		data, err := ioutil.ReadFile(ca)
		if err != nil {
			return err
		}
		casData = append(casData, data)
	}

	obji, err := runtime.Decode(mcfgscheme.Codecs.UniversalDecoder(mcfgv1.SchemeGroupVersion), raw)
	if err != nil {
		return err
	}
	mcoconfig, ok := obji.(*mcfgv1.MCOConfig)
	if !ok {
		return fmt.Errorf("expected *MCOConfig found %T", obji)
	}
	config := getRenderConfig(mcoconfig, casData[1], casData[0])

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

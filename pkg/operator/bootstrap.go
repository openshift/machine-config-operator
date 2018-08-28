package operator

import (
	"io/ioutil"
	"path/filepath"

	"github.com/golang/glog"
)

// RenderBootstrap writes to destinationDir static Pods.
func RenderBootstrap(destinationDir string) error {
	config := getRenderConfig()

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
		b, err := renderAsset(*config, m.name)
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

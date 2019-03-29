package operator

import (
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"

	"github.com/golang/glog"

	"k8s.io/apimachinery/pkg/runtime"

	configv1 "github.com/openshift/api/config/v1"
	configscheme "github.com/openshift/client-go/config/clientset/versioned/scheme"

	templatectrl "github.com/openshift/machine-config-operator/pkg/controller/template"
)

// RenderBootstrap writes to destinationDir static Pods.
func RenderBootstrap(
	clusterConfigConfigMapFile string,
	infraFile, networkFile string,
	etcdCAFile, etcdMetricCAFile string, rootCAFile string, kubeCAFile string, pullSecretFile string,
	imgs Images,
	destinationDir string,
) error {
	filesData := map[string][]byte{}
	files := []string{
		clusterConfigConfigMapFile,
		infraFile, networkFile,
		rootCAFile, etcdCAFile, pullSecretFile,
	}
	if kubeCAFile != "" {
		files = append(files, kubeCAFile)
	}
	for _, file := range files {
		data, err := ioutil.ReadFile(file)
		if err != nil {
			return err
		}
		filesData[file] = data
	}

	// create ControllerConfigSpec
	obji, err := runtime.Decode(configscheme.Codecs.UniversalDecoder(configv1.SchemeGroupVersion), filesData[infraFile])
	if err != nil {
		return err
	}
	infra, ok := obji.(*configv1.Infrastructure)
	if !ok {
		return fmt.Errorf("expected *configv1.Infrastructure found %T", obji)
	}

	obji, err = runtime.Decode(configscheme.Codecs.UniversalDecoder(configv1.SchemeGroupVersion), filesData[networkFile])
	if err != nil {
		return err
	}
	network, ok := obji.(*configv1.Network)
	if !ok {
		return fmt.Errorf("expected *configv1.Network found %T", obji)
	}
	spec, err := createDiscoveredControllerConfigSpec(infra, network)
	if err != nil {
		return err
	}

	bundle := make([]byte, 0)
	bundle = append(bundle, filesData[rootCAFile]...)
	// Append the kube-ca if given.
	if _, ok := filesData[kubeCAFile]; ok {
		bundle = append(bundle, filesData[kubeCAFile]...)
	}

	spec.EtcdCAData = filesData[etcdCAFile]
	spec.EtcdMetricCAData = filesData[etcdMetricCAFile]
	spec.RootCAData = bundle
	spec.PullSecret = nil
	spec.OSImageURL = imgs.MachineOSContent
	spec.Images = map[string]string{
		templatectrl.EtcdImageKey:            imgs.Etcd,
		templatectrl.SetupEtcdEnvKey:         imgs.SetupEtcdEnv,
		templatectrl.InfraImageKey:           imgs.InfraImage,
		templatectrl.KubeClientAgentImageKey: imgs.KubeClientAgent,
	}

	config := getRenderConfig("", spec, imgs, infra.Status.APIServerURL)

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
		glog.Info(m.name)

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

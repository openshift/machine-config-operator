package server

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/url"
	"strings"

	ignv2_2types "github.com/coreos/ignition/config/v2_2/types"
	"github.com/openshift/machine-config-operator/pkg/daemon"
	"github.com/vincent-petithory/dataurl"
)

const (
	// defaultMachineKubeConfPath defines the default location
	// of the KubeConfig file on the machine.
	defaultMachineKubeConfPath = "/etc/system/kubeconfig"

	// defaultFileSystem defines the default file system to be
	// used for writing the ignition files created by the
	// server.
	defaultFileSystem = "root"

	// etcdTemplateParam defines the parameter used for the etcd
	// index in the machine config.
	// This param needs to be replaced by the value of the index
	// received in the request.
	etcdTemplateParam = "{{.etcd_index}}"
)

// kubeconfigFunc fetches the kubeconfig that needs to be served.
type kubeconfigFunc func() (kubeconfigData []byte, rootCAData []byte, err error)

// Server defines the interface that is implemented by different
// machine config server implementations.
type Server interface {
	GetConfig(poolRequest) (*ignv2_2types.Config, error)
}

func appendNodeAnnotations(conf *ignv2_2types.Config, currConf string) error {
	anno, err := getNodeAnnotation(currConf)
	if err != nil {
		return err
	}
	appendFileToIgnition(conf, daemon.InitialNodeAnnotationsFilePath, string(anno))
	return nil
}

func getNodeAnnotation(conf string) (string, error) {
	nodeAnnotations := map[string]string{
		daemon.CurrentMachineConfigAnnotationKey: conf,
		daemon.DesiredMachineConfigAnnotationKey: conf,
	}
	contents, err := json.Marshal(nodeAnnotations)
	if err != nil {
		return "", fmt.Errorf("could not marshal node annotations, err: %v", err)
	}
	return string(contents), nil
}

func copyFileToIgnition(conf *ignv2_2types.Config, outPath, srcPath string) error {
	contents, err := ioutil.ReadFile(srcPath)
	if err != nil {
		return fmt.Errorf("could not read file from: %s, err: %v", srcPath, err)
	}
	appendFileToIgnition(conf, outPath, string(contents))
	return nil
}

func appendFileToIgnition(conf *ignv2_2types.Config, outPath, contents string) {
	fileMode := int(420)
	file := ignv2_2types.File{
		Node: ignv2_2types.Node{
			Filesystem: defaultFileSystem,
			Path:       outPath,
		},
		FileEmbedded1: ignv2_2types.FileEmbedded1{
			Contents: ignv2_2types.FileContents{
				Source: getEncodedContent(string(contents)),
			},
			Mode: &fileMode,
		},
	}
	if len(conf.Storage.Files) == 0 {
		conf.Storage.Files = make([]ignv2_2types.File, 0)
	}
	conf.Storage.Files = append(conf.Storage.Files, file)
}

func execEtcdTemplates(conf *ignv2_2types.Config, etcdIndex string) error {
	if etcdIndex == "" {
		return nil
	}
	if len(conf.Systemd.Units) > 0 {
		for i := range conf.Systemd.Units {
			conf.Systemd.Units[i].Contents = strings.Replace(conf.Systemd.Units[i].Contents, etcdTemplateParam, etcdIndex, -1)

			for j := range conf.Systemd.Units[i].Dropins {
				conf.Systemd.Units[i].Dropins[j].Contents =
					strings.Replace(conf.Systemd.Units[i].Dropins[j].Contents, etcdTemplateParam, etcdIndex, -1)
			}
		}
	}
	return nil
}

func getDecodedContent(inp string) (string, error) {
	d, err := dataurl.DecodeString(inp)
	if err != nil {
		return "", err
	}

	return string(d.Data), nil
}

func getEncodedContent(inp string) string {
	return (&url.URL{
		Scheme: "data",
		Opaque: "," + dataurl.Escape([]byte(inp)),
	}).String()
}

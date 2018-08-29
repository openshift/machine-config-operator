package daemon

import (
	"fmt"
	"io/ioutil"
	"path/filepath"
	"strings"

	"github.com/coreos/go-systemd/login1"
	ignv2_2types "github.com/coreos/ignition/config/v2_2/types"
	"github.com/golang/glog"
	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	mcfgclientset "github.com/openshift/machine-config-operator/pkg/generated/clientset/versioned"
	mcfgclientv1 "github.com/openshift/machine-config-operator/pkg/generated/clientset/versioned/typed/machineconfiguration.openshift.io/v1"
	"github.com/vincent-petithory/dataurl"
	drain "github.com/wking/kubernetes-drain"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	coreclientv1 "k8s.io/client-go/kubernetes/typed/core/v1"
)

// Daemon is the dispatch point for the functions of the agent on the
// machine. it keeps track of connections and the current state of the update
// process.
type Daemon struct {
	// name is the node name.
	name string

	// login client talks to the systemd-logind service for rebooting the
	// machine
	loginClient *login1.Conn

	client mcfgclientset.Interface
	// kubeClient allows interaction with Kubernetes, including the node we are running on.
	kubeClient kubernetes.Interface

	// prefix is where the root filesystem is mounted
	prefix string
}

const (
	pathSystemd = "/etc/systemd/system"
	pathDevNull = "/dev/null"
)

// NodeAnnotationLoader is the function type that loads annotations from the cluster.
// It is required when creating a new Daemon object via it's constructor.
type NodeAnnotationLoader func(coreclientv1.NodeInterface, string) error

// New sets up the systemd and kubernetes connections needed to update the
// machine.
func New(
	rootPrefix string,
	nodeName string,
	client mcfgclientset.Interface,
	kubeClient kubernetes.Interface,
	loadNodeAnnotations NodeAnnotationLoader,
	loginClient *login1.Conn,
) (*Daemon, error) {
	// Default to creating our own connection to the system dbus
	if loginClient == nil {
		var err error
		loginClient, err = login1.New()
		if err != nil {
			return nil, fmt.Errorf("Error establishing connection to logind dbus: %v", err)
		}
	}
	if err := loadNodeAnnotations(kubeClient.CoreV1().Nodes(), nodeName); err != nil {
		return nil, err
	}

	return &Daemon{
		name:        nodeName,
		loginClient: loginClient,
		client:      client,
		kubeClient:  kubeClient,
		prefix:      rootPrefix,
	}, nil
}

// Run watches the annotations on the machine until they indicate that we need
// an update. then it triggers an update of the machine. currently, the update
// function shouldn't return, and should just reboot the node, unless an error
// occurs, in which case it will return the error up the call stack.
func (dn *Daemon) Run(stop <-chan struct{}) error {
	glog.Info("Starting MachineConfigDameon")
	defer glog.Info("Shutting down MachineConfigDameon")

	err := dn.syncOnce()
	if err != nil {
		return setUpdateDegraded(dn.kubeClient.CoreV1().Nodes(), dn.name)
	}
	return nil
}

// syncOnce only completes once.
func (dn *Daemon) syncOnce() error {
	// validate that the machine correctly made it to the target state
	status, err := dn.validate()
	if err != nil {
		return err
	}
	if !status {
		return dn.triggerUpdate()
	}

	if err := setUpdateDone(dn.kubeClient.CoreV1().Nodes(), dn.name); err != nil {
		return err
	}

	node, err := dn.kubeClient.CoreV1().Nodes().Get(dn.name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	err = drain.Uncordon(dn.kubeClient.CoreV1().Nodes(), node, nil)
	if err != nil {
		return err
	}

	// watch the annotations.
	err = waitUntilUpdate(dn.kubeClient.CoreV1().Nodes(), dn.name)
	if err != nil {
		return fmt.Errorf("Failed to wait until update request: %v", err)
	}

	return dn.triggerUpdate()
}

// triggerUpdate starts the update using the current and the target config.
func (dn *Daemon) triggerUpdate() error {
	if err := setUpdateWorking(dn.kubeClient.CoreV1().Nodes(), dn.name); err != nil {
		return err
	}

	ccAnnotation, err := getNodeAnnotation(dn.kubeClient.CoreV1().Nodes(), dn.name, CurrentMachineConfigAnnotationKey)
	if err != nil {
		return err
	}
	dcAnnotation, err := getNodeAnnotation(dn.kubeClient.CoreV1().Nodes(), dn.name, DesiredMachineConfigAnnotationKey)
	if err != nil {
		return err
	}
	currentConfig, err := getMachineConfig(dn.client.MachineconfigurationV1().MachineConfigs(), ccAnnotation)
	if err != nil {
		return err
	}
	desiredConfig, err := getMachineConfig(dn.client.MachineconfigurationV1().MachineConfigs(), dcAnnotation)
	if err != nil {
		return err
	}

	// run the update process. this function doesn't currently return.
	return dn.update(currentConfig, desiredConfig)
}

// validate confirms that the node is actually in the state that it wants to be
// in. it does this by looking at the elements in the target config and checks
// if all are present on the node. if any file/unit is missing or there is a
// mismatch, it re-triggers the update.
func (dn *Daemon) validate() (bool, error) {
	ccAnnotation, err := getNodeAnnotation(dn.kubeClient.CoreV1().Nodes(), dn.name, CurrentMachineConfigAnnotationKey)
	if err != nil {
		return false, err
	}
	dcAnnotation, err := getNodeAnnotation(dn.kubeClient.CoreV1().Nodes(), dn.name, DesiredMachineConfigAnnotationKey)
	if err != nil {
		return false, err
	}
	// if the current annotation is equal to the desired annotation,
	// system state is valid.
	if strings.Compare(dcAnnotation, ccAnnotation) == 0 {
		return true, nil
	}

	desiredConfig, err := getMachineConfig(dn.client.MachineconfigurationV1().MachineConfigs(), dcAnnotation)
	if err != nil {
		return false, err
	}

	if dn.checkFiles(desiredConfig.Spec.Config.Storage.Files) &&
		dn.checkUnits(desiredConfig.Spec.Config.Systemd.Units) {
		return true, nil
	}

	// error is nil, as we successfully decided that validate is false
	return false, nil
}

// checkUnits validates the contents of  all the units in the
// target config.
func (dn *Daemon) checkUnits(units []ignv2_2types.Unit) bool {
	for _, u := range units {
		for j := range u.Dropins {
			path := filepath.Join(dn.prefix, pathSystemd, u.Name+".d", u.Dropins[j].Name)
			if status := checkFileContents(path, u.Dropins[j].Contents); !status {
				return false
			}
		}

		if u.Contents == "" {
			continue
		}

		path := filepath.Join(dn.prefix, pathSystemd, u.Name)
		if u.Mask {
			link, err := filepath.EvalSymlinks(path)
			if err != nil {
				glog.Errorf("state validation: error while evaluation symlink for path: %q, err: %v", path, err)
				return false
			}
			if strings.Compare(pathDevNull, link) != 0 {
				glog.Errorf("state validation: invalid unit masked setting. path: %q; expected: %v; received: %v", path, pathDevNull, link)
				return false
			}
		}
		if status := checkFileContents(path, u.Contents); !status {
			return false
		}

	}
	return true
}

// checkFiles validates the contents of  all the files in the
// target config.
func (dn *Daemon) checkFiles(files []ignv2_2types.File) bool {
	for _, f := range files {
		path := filepath.Join(dn.prefix, f.Path)
		contents, err := dataurl.DecodeString(f.Contents.Source)
		if err != nil {
			glog.Errorf("couldn't parse file: %v", err)
			return false
		}
		if status := checkFileContents(path, string(contents.Data)); !status {
			return false
		}
	}
	return true
}

// checkFileContents reads the file from the filepath and compares its contents
// with the  expectedContent. It logs an error in case of an error or mismatch
// and returns the status of the evaluation.
func checkFileContents(filePath, expectedContent string) bool {
	contents, err := ioutil.ReadFile(filePath)
	if err != nil {
		glog.Errorf("could not read file: %q, error: %v", filePath, err)
		return false
	}
	if strings.Compare(string(contents), expectedContent) != 0 {
		glog.Errorf("content mismatch for file: %q; expected: %v; received: %v", filePath, expectedContent, string(contents))
		return false
	}
	return true
}

// Close closes all the connections the node agent has open for it's lifetime
func (dn *Daemon) Close() {
	dn.loginClient.Close()
}

func getMachineConfig(client mcfgclientv1.MachineConfigInterface, name string) (*mcfgv1.MachineConfig, error) {
	return client.Get(name, metav1.GetOptions{})
}

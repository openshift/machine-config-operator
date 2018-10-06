package daemon

import (
	"fmt"
	"io/ioutil"
	"path/filepath"
	"strings"

	"github.com/coreos/go-systemd/login1"
	ignv2_2types "github.com/coreos/ignition/config/v2_2/types"
	"github.com/golang/glog"
	drain "github.com/openshift/kubernetes-drain"
	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	mcfgclientset "github.com/openshift/machine-config-operator/pkg/generated/clientset/versioned"
	mcfgclientv1 "github.com/openshift/machine-config-operator/pkg/generated/clientset/versioned/typed/machineconfiguration.openshift.io/v1"
	"github.com/vincent-petithory/dataurl"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// Daemon is the dispatch point for the functions of the agent on the
// machine. it keeps track of connections and the current state of the update
// process.
type Daemon struct {
	// name is the node name.
	name string
	// OperatingSystem the operating system the MCD is running on
	OperatingSystem string

	// NodeUpdaterClient an instance of the client which interfaces with host content deployments
	NodeUpdaterClient NodeUpdaterClient

	// bootedOSImageURL is the currently booted URL of the operating system
	bootedOSImageURL string

	// login client talks to the systemd-logind service for rebooting the
	// machine
	loginClient *login1.Conn

	client mcfgclientset.Interface
	// kubeClient allows interaction with Kubernetes, including the node we are running on.
	kubeClient kubernetes.Interface

	// rootMount is the location for the MCD to chroot in
	rootMount string
}

const (
	// pathSystemd is the path systemd modifiable units, services, etc.. reside
	pathSystemd = "/etc/systemd/system"
	// wantsPathSystemd is the path where enabled units should be linked
	wantsPathSystemd = "/etc/systemd/system/multi-user.target.wants/"
	// pathDevNull is the systems path to and endless blackhole
	pathDevNull = "/dev/null"
)

// New sets up the systemd and kubernetes connections needed to update the
// machine.
func New(
	rootMount string,
	nodeName string,
	operatingSystem string,
	nodeUpdaterClient NodeUpdaterClient,
	client mcfgclientset.Interface,
	kubeClient kubernetes.Interface,
) (*Daemon, error) {
	loginClient, err := login1.New()
	if err != nil {
		return nil, fmt.Errorf("Error establishing connection to logind dbus: %v", err)
	}

	if err = loadNodeAnnotations(kubeClient.CoreV1().Nodes(), nodeName); err != nil {
		return nil, err
	}

	osImageURL, osVersion, err := nodeUpdaterClient.GetBootedOSImageURL(rootMount)
	glog.Infof("Booted osImageURL: %s (%s)", osImageURL, osVersion)

	return &Daemon{
		name:              nodeName,
		OperatingSystem:   operatingSystem,
		NodeUpdaterClient: nodeUpdaterClient,
		loginClient:       loginClient,
		client:            client,
		kubeClient:        kubeClient,
		rootMount:         rootMount,
		bootedOSImageURL:  osImageURL,
	}, nil
}

// Run watches the annotations on the machine until they indicate that we need
// an update. then it triggers an update of the machine. currently, the update
// function shouldn't return, and should just reboot the node, unless an error
// occurs, in which case it will return the error up the call stack.
func (dn *Daemon) Run(stop <-chan struct{}) error {
	glog.Info("Starting MachineConfigDaemon")
	defer glog.Info("Shutting down MachineConfigDaemon")

	err := dn.process()
	if err != nil {
		glog.Errorf("Marking degraded due to: %v", err)
		return setUpdateDegraded(dn.kubeClient.CoreV1().Nodes(), dn.name)
	}

	return nil
}

// process starts the main loop that actually does all the work.
func (dn *Daemon) process() error {

	// do a first pass before entering the main loop
	if err := dn.syncOnce(); err != nil {
		return err
	}

	for {
		glog.V(2).Infof("Watching for node annotation updates on %q", dn.name)
		if err := waitUntilUpdate(dn.kubeClient.CoreV1().Nodes(), dn.name); err != nil {
			return err
		}
		if err := dn.syncOnce(); err != nil {
			return err
		}
	}
}

// syncOnce only completes once.
func (dn *Daemon) syncOnce() error {
	// validate that the machine correctly made it to the target state
	isDesired, err := dn.isDesiredMachineState()
	if err != nil {
		return err
	}

	if !isDesired {
		// this currently doesn't return, but may return in the future if the
		// update could be done without rebooting
		if err := dn.triggerUpdate(); err != nil {
			return err
		}

		glog.V(2).Infof("Successfully updated without reboot")
	} else {
		glog.V(2).Infof("Node is up to date")
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

	return nil
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

// isDesiredMachineState confirms that the node is actually in the state that it
// wants to be in. It does this by looking at the elements in the target config
// and checks if all are present on the node. Returns true iff there are no
// mismatches (e.g. files, units... XXX: but not yet OS version).
func (dn *Daemon) isDesiredMachineState() (bool, error) {
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

	isDesiredOS, err := dn.checkOS(desiredConfig.Spec.OSImageURL)
	if err != nil {
		return false, err
	}

	if dn.checkFiles(desiredConfig.Spec.Config.Storage.Files) &&
		dn.checkUnits(desiredConfig.Spec.Config.Systemd.Units) &&
		isDesiredOS {
		return true, nil
	}

	// error is nil, as we successfully decided that validate is false
	return false, nil
}

// checkOS validates the OS image URL and returns true if they match.
func (dn *Daemon) checkOS(osImageURL string) (bool, error) {
	return dn.bootedOSImageURL == osImageURL, nil
}

// checkUnits validates the contents of all the units in the
// target config and retursn true if they match.
func (dn *Daemon) checkUnits(units []ignv2_2types.Unit) bool {
	for _, u := range units {
		for j := range u.Dropins {
			path := filepath.Join(pathSystemd, u.Name+".d", u.Dropins[j].Name)
			if status := checkFileContents(path, u.Dropins[j].Contents); !status {
				return false
			}
		}

		if u.Contents == "" {
			continue
		}

		path := filepath.Join(pathSystemd, u.Name)
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
		contents, err := dataurl.DecodeString(f.Contents.Source)
		if err != nil {
			glog.Errorf("couldn't parse file: %v", err)
			return false
		}
		if status := checkFileContents(f.Path, string(contents.Data)); !status {
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

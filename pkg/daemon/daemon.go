package daemon

import (
	"fmt"
	"io/ioutil"
	"os"
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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	coreinformersv1 "k8s.io/client-go/informers/core/v1"
	"k8s.io/client-go/kubernetes"
	corelisterv1 "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
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

	// filesystemClient allows interaction with the local filesystm
	fileSystemClient FileSystemClient

	// rootMount is the location for the MCD to chroot in
	rootMount string

	// nodeLister is used to watch for updates via the informer
	nodeLister corelisterv1.NodeLister

	nodeListerSynced cache.InformerSynced
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
	fileSystemClient FileSystemClient,
	nodeInformer coreinformersv1.NodeInformer,
) (*Daemon, error) {
	loginClient, err := login1.New()
	if err != nil {
		return nil, fmt.Errorf("Error establishing connection to logind dbus: %v", err)
	}

	if err = loadNodeAnnotations(kubeClient.CoreV1().Nodes(), nodeName); err != nil {
		return nil, err
	}

	osImageURL, osVersion, err := nodeUpdaterClient.GetBootedOSImageURL(rootMount)
	if err != nil {
		return nil, fmt.Errorf("Error reading osImageURL from rpm-ostree: %v", err)
	}
	glog.Infof("Booted osImageURL: %s (%s)", osImageURL, osVersion)

	dn := &Daemon{
		name:              nodeName,
		OperatingSystem:   operatingSystem,
		NodeUpdaterClient: nodeUpdaterClient,
		loginClient:       loginClient,
		client:            client,
		kubeClient:        kubeClient,
		rootMount:         rootMount,
		fileSystemClient:  fileSystemClient,
		bootedOSImageURL:  osImageURL,
	}

	nodeInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		UpdateFunc: dn.handleNodeUpdate,
	})

	dn.nodeLister = nodeInformer.Lister()
	dn.nodeListerSynced = nodeInformer.Informer().HasSynced

	return dn, nil
}

// Run finishes informer setup and then blocks, and the informer will be
// responsible for triggering callbacks to handle updates. Successful
// updates shouldn't return, and should just reboot the node.
func (dn *Daemon) Run(stop <-chan struct{}) error {
	if !cache.WaitForCacheSync(stop, dn.nodeListerSynced) {
		glog.Error("Marking degraded due to: failure to sync caches")
		return setUpdateDegraded(dn.kubeClient.CoreV1().Nodes(), dn.name)
	}

	<-stop

	// Run() should block on the above <-stop until an updated is detected,
	// which is handled by the callbacks.
	return nil
}

// CheckStateOnBoot is responsible for checking whether the node has
// degraded, and if not, whether an update is required immediately.
// The flow goes something like this -
// 1. Sanity check if we're in a degraded state. If yes, handle appropriately.
// 2. we restarted for some reason. the happy path reason we restarted is
//    because of a machine reboot. validate the current machine state is the
//    desired machine state. if we aren't try updating again. if we are, update
//    the current state annotation accordingly.
func (dn *Daemon) CheckStateOnBoot(stop <-chan struct{}) error {
	// sanity check we're not already in a degraded state
	if state, err := getNodeAnnotationExt(dn.kubeClient.CoreV1().Nodes(), dn.name, MachineConfigDaemonStateAnnotationKey, true); err != nil {
		// try to set to degraded... because we failed to check if we're degraded
		glog.Errorf("Marking degraded due to: %v", err)
		return setUpdateDegraded(dn.kubeClient.CoreV1().Nodes(), dn.name)
	} else if state == MachineConfigDaemonStateDegraded {
		// just sleep so that we don't clobber output of previous run which
		// probably contains the real reason why we marked the node as degraded
		// in the first place
		glog.Info("Node is degraded; going to sleep")
		select {
		case <-stop:
			return nil
		}
	}

	// validate machine state
	isDesired, dcAnnotation, err := dn.isDesiredMachineState()
	if err != nil {
		glog.Errorf("Marking degraded due to: %v", err)
		return setUpdateDegraded(dn.kubeClient.CoreV1().Nodes(), dn.name)
	}

	if isDesired {
		// we got the machine state we wanted. set the update complete!
		if err := dn.completeUpdate(dcAnnotation); err != nil {
			glog.Errorf("Marking degraded due to: %v", err)
			return setUpdateDegraded(dn.kubeClient.CoreV1().Nodes(), dn.name)
		}
	} else if err := dn.triggerUpdate(); err != nil {
		return err
	}

	return nil
}

// handleNodeUpdate is the handler for informer callbacks detecting node
// changes. If an update is requested by the controller, we assume that
// that means something changed and apply the desired config no matter what.
// Also note that we only care about node updates, not creation or deletion.
func (dn *Daemon) handleNodeUpdate(old, cur interface{}) {
	node := cur.(*corev1.Node)

	// First check if the node that was updated is this daemon's node
	if node.Name != dn.name {
		// The node that was changed was not ours
		return
	}

	// Then check we're not already in a degraded state.
	if state, err := getNodeAnnotation(dn.kubeClient.CoreV1().Nodes(), dn.name, MachineConfigDaemonStateAnnotationKey); err != nil {
		// try to set to degraded... because we failed to check if we're degraded
		glog.Errorf("Marking degraded due to: %v", err)
		setUpdateDegraded(dn.kubeClient.CoreV1().Nodes(), dn.name)
		return
	} else if state == MachineConfigDaemonStateDegraded {
		// Just return since we want to continue sleeping
		return
	}

	// Detect if there is an update
	if node.Annotations[DesiredMachineConfigAnnotationKey] == node.Annotations[CurrentMachineConfigAnnotationKey] {
		// No actual update to the config
		return
	}

	// The desired machine config has changed, trigger update
	if err := dn.triggerUpdate(); err != nil {
		glog.Errorf("Marking degraded due to: %v", err)
		if errSet := setUpdateDegraded(dn.kubeClient.CoreV1().Nodes(), dn.name); errSet != nil {
			glog.Errorf("Futher error attempting to set the node to degraded: %v", errSet)
		}
		// reboot the node, which will catch the degraded state and sleep
		dn.reboot()
	}

	// we managed to update the machine without rebooting. in this case,
	// continue as usual waiting for the next update
	glog.V(2).Infof("Successfully updated without reboot")
}

// completeUpdate does all the stuff required to finish an update. right now, it
// sets the status annotation to Done and marks the node as schedulable again.
func (dn *Daemon) completeUpdate(dcAnnotation string) error {
	if err := setUpdateDone(dn.kubeClient.CoreV1().Nodes(), dn.name, dcAnnotation); err != nil {
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
// mismatches (e.g. files, units, OS version), as well as the config that was
// evaluated if the state is what the machine wants to be in.
func (dn *Daemon) isDesiredMachineState() (bool, string, error) {
	ccAnnotation, err := getNodeAnnotation(dn.kubeClient.CoreV1().Nodes(), dn.name, CurrentMachineConfigAnnotationKey)
	if err != nil {
		return false, "", err
	}
	dcAnnotation, err := getNodeAnnotation(dn.kubeClient.CoreV1().Nodes(), dn.name, DesiredMachineConfigAnnotationKey)
	if err != nil {
		return false, "", err
	}
	// if the current annotation is equal to the desired annotation,
	// system state is valid.
	if strings.Compare(dcAnnotation, ccAnnotation) == 0 {
		return true, dcAnnotation, nil
	}

	currentConfig, err := getMachineConfig(dn.client.MachineconfigurationV1().MachineConfigs(), ccAnnotation)
	if err != nil {
		return false, "", err
	}
	desiredConfig, err := getMachineConfig(dn.client.MachineconfigurationV1().MachineConfigs(), dcAnnotation)
	if err != nil {
		return false, "", err
	}

	// if we can't reconcile the changes between the old config and the new
	// config, the machine is definitely not in its desired state. this function
	// will return true (meaning they are reconcilable) if there aren't actually
	// changes.
	reconcilable, err := dn.reconcilable(currentConfig, desiredConfig)
	if err != nil {
		return false, "", err
	}
	if !reconcilable {
		return false, "", nil
	}

	isDesiredOS, err := dn.checkOS(desiredConfig.Spec.OSImageURL)
	if err != nil {
		return false, "", err
	}

	if dn.checkFiles(desiredConfig.Spec.Config.Storage.Files) &&
		dn.checkUnits(desiredConfig.Spec.Config.Systemd.Units) &&
		isDesiredOS {
		return true, dcAnnotation, nil
	}

	// error is nil, as we successfully decided that validate is false
	return false, "", nil
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
			if status := checkFileContentsAndMode(path, u.Dropins[j].Contents, DefaultFilePermissions); !status {
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
		if status := checkFileContentsAndMode(path, u.Contents, DefaultFilePermissions); !status {
			return false
		}

	}
	return true
}

// checkFiles validates the contents of  all the files in the
// target config.
func (dn *Daemon) checkFiles(files []ignv2_2types.File) bool {
	for _, f := range files {
		mode := DefaultFilePermissions
		if f.Mode != nil {
			mode = os.FileMode(*f.Mode)
		}
		contents, err := dataurl.DecodeString(f.Contents.Source)
		if err != nil {
			glog.Errorf("couldn't parse file: %v", err)
			return false
		}
		if status := checkFileContentsAndMode(f.Path, string(contents.Data), mode); !status {
			return false
		}
	}
	return true
}

// checkFileContentsAndMode reads the file from the filepath and compares its
// contents and mode with the expectedContent and mode parameters. It logs an
// error in case of an error or mismatch and returns the status of the
// evaluation.
func checkFileContentsAndMode(filePath, expectedContent string, mode os.FileMode) bool {
	fi, err := os.Lstat(filePath)
	if err != nil {
		glog.Errorf("could not stat file: %q, error: %v", filePath, err)
		return false
	}
	if fi.Mode() != mode {
		glog.Errorf("mode mismatch for file: %q; expected: %v; received: %v", filePath, mode, fi.Mode())
		return false
	}
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

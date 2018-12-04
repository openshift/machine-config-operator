package daemon

import (
	"context"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/coreos/go-systemd/login1"
	ignv2 "github.com/coreos/ignition/config/v2_2"
	ignv2_2types "github.com/coreos/ignition/config/v2_2/types"
	"github.com/golang/glog"
	drain "github.com/openshift/kubernetes-drain"
	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	mcfgclientset "github.com/openshift/machine-config-operator/pkg/generated/clientset/versioned"
	mcfgclientv1 "github.com/openshift/machine-config-operator/pkg/generated/clientset/versioned/typed/machineconfiguration.openshift.io/v1"
	"github.com/vincent-petithory/dataurl"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/apimachinery/pkg/util/wait"
	coreinformersv1 "k8s.io/client-go/informers/core/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	clientsetcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	corelisterv1 "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
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
	// recorder sends events to the apiserver
	recorder record.EventRecorder

	// filesystemClient allows interaction with the local filesystm
	fileSystemClient FileSystemClient

	// rootMount is the location for the MCD to chroot in
	rootMount string

	// nodeLister is used to watch for updates via the informer
	nodeLister corelisterv1.NodeLister

	nodeListerSynced cache.InformerSynced
	// onceFrom defines where the source config is to run the daemon once and exit
	onceFrom string

	kubeletHealthzEnabled  bool
	kubeletHealthzEndpoint string

	nodeWriter *NodeWriter

	// channel used by callbacks to signal Run() of an error
	exitCh chan<- error
}

const (
	// pathSystemd is the path systemd modifiable units, services, etc.. reside
	pathSystemd = "/etc/systemd/system"
	// wantsPathSystemd is the path where enabled units should be linked
	wantsPathSystemd = "/etc/systemd/system/multi-user.target.wants/"
	// pathDevNull is the systems path to and endless blackhole
	pathDevNull = "/dev/null"
)

const (
	kubeletHealthzEndpoint         = "http://localhost:10248/healthz"
	kubeletHealthzPollingInterval  = time.Duration(30 * time.Second)
	kubeletHealthzTimeout          = time.Duration(30 * time.Second)
	kubeletHealthzFailureThreshold = 3
)

// New sets up the systemd and kubernetes connections needed to update the
// machine.
func New(
	rootMount string,
	nodeName string,
	operatingSystem string,
	nodeUpdaterClient NodeUpdaterClient,
	fileSystemClient FileSystemClient,
	onceFrom string,
	kubeletHealthzEnabled bool,
	kubeletHealthzEndpoint string,
	nodeWriter *NodeWriter,
	exitCh chan<- error,
) (*Daemon, error) {

	loginClient, err := login1.New()
	if err != nil {
		return nil, fmt.Errorf("Error establishing connection to logind dbus: %v", err)
	}

	osImageURL := ""
	// Only pull the osImageURL from OSTree when we are on RHCOS
	if operatingSystem == MachineConfigDaemonOSRHCOS {
		osImageURL, osVersion, err := nodeUpdaterClient.GetBootedOSImageURL(rootMount)
		if err != nil {
			return nil, fmt.Errorf("Error reading osImageURL from rpm-ostree: %v", err)
		}
		glog.Infof("Booted osImageURL: %s (%s)", osImageURL, osVersion)
	}
	dn := &Daemon{
		name:                   nodeName,
		OperatingSystem:        operatingSystem,
		NodeUpdaterClient:      nodeUpdaterClient,
		loginClient:            loginClient,
		rootMount:              rootMount,
		fileSystemClient:       fileSystemClient,
		bootedOSImageURL:       osImageURL,
		onceFrom:               onceFrom,
		kubeletHealthzEnabled:  kubeletHealthzEnabled,
		kubeletHealthzEndpoint: kubeletHealthzEndpoint,
		nodeWriter:             nodeWriter,
		exitCh:                 exitCh,
	}

	return dn, nil
}

// NewClusterDrivenDaemon sets up the systemd and kubernetes connections needed to update the
// machine.
func NewClusterDrivenDaemon(
	rootMount string,
	nodeName string,
	operatingSystem string,
	nodeUpdaterClient NodeUpdaterClient,
	client mcfgclientset.Interface,
	kubeClient kubernetes.Interface,
	fileSystemClient FileSystemClient,
	onceFrom string,
	nodeInformer coreinformersv1.NodeInformer,
	kubeletHealthzEnabled bool,
	kubeletHealthzEndpoint string,
	nodeWriter *NodeWriter,
	exitCh chan<- error,
) (*Daemon, error) {
	dn, err := New(
		rootMount,
		nodeName,
		operatingSystem,
		nodeUpdaterClient,
		fileSystemClient,
		onceFrom,
		kubeletHealthzEnabled,
		kubeletHealthzEndpoint,
		nodeWriter,
		exitCh,
	)

	if err != nil {
		return nil, err
	}

	dn.kubeClient = kubeClient
	dn.client = client

	eventBroadcaster := record.NewBroadcaster()
	eventBroadcaster.StartLogging(glog.V(2).Infof)
	eventBroadcaster.StartRecordingToSink(&clientsetcorev1.EventSinkImpl{Interface: kubeClient.CoreV1().Events("")})
	dn.recorder = eventBroadcaster.NewRecorder(scheme.Scheme, corev1.EventSource{Component: "machineconfigdaemon", Host: nodeName})

	if err = loadNodeAnnotations(dn.kubeClient.CoreV1().Nodes(), nodeName); err != nil {
		return nil, err
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
func (dn *Daemon) Run(stopCh <-chan struct{}, exitCh <-chan error) error {
	if dn.kubeletHealthzEnabled {
		glog.Info("Enabling Kubelet Healthz Monitor")
		go dn.runKubeletHealthzMonitor(stopCh, dn.exitCh)
	}

	// Catch quickly if we've been asked to run once.
	if dn.onceFrom != "" {
		genericConfig, configType, contentFrom, err := dn.SenseAndLoadOnceFrom()
		if err != nil {
			glog.Warningf("Unable to decipher onceFrom config type: %s", err)
			return err
		}
		if configType == MachineConfigIgnitionFileType {
			glog.V(2).Info("Daemon running directly from Ignition")
			ignConfig := genericConfig.(ignv2_2types.Config)
			return dn.runOnceFromIgnition(ignConfig)
		} else if configType == MachineConfigMCFileType {
			glog.V(2).Info("Daemon running directly from MachineConfig")
			mcConfig := genericConfig.(*(mcfgv1.MachineConfig))
			// this already sets the node as degraded on error in the in-cluster path
			return dn.runOnceFromMachineConfig(*mcConfig, contentFrom)
		}
	}

	if !cache.WaitForCacheSync(stopCh, dn.nodeListerSynced) {
		return dn.nodeWriter.SetUpdateDegradedMsgIgnoreErr("failed to sync cache", dn.kubeClient.CoreV1().Nodes(), dn.name)
	}

	// Block on exit channel. The node informer will send callbacks through
	// handleNodeUpdate(). If a failure happens there, it writes to the channel.
	// The HealthzMonitor goroutine also writes to this channel if the threshold
	// is reached.
	err := <-exitCh

	return dn.nodeWriter.SetUpdateDegradedIgnoreErr(err, dn.kubeClient.CoreV1().Nodes(), dn.name)
}

func (dn *Daemon) runKubeletHealthzMonitor(stopCh <-chan struct{}, exitCh chan<- error) {
	failureCount := 0
	for {
		select {
		case <-stopCh:
			return
		case <-time.After(kubeletHealthzPollingInterval):
			if err := dn.getHealth(); err != nil {
				glog.Warningf("Failed kubelet health check: %v", err)
				failureCount++
				if failureCount >= kubeletHealthzFailureThreshold {
					exitCh <- fmt.Errorf("Kubelet health failure threshold reached")
				}
			} else {
				failureCount = 0 // reset failure count on success
			}
		}
	}
}

func (dn *Daemon) getHealth() error {
	glog.V(2).Info("Kubelet health running")
	ctx, cancel := context.WithTimeout(context.Background(), kubeletHealthzTimeout)
	defer cancel()

	req, err := http.NewRequest("GET", dn.kubeletHealthzEndpoint, nil)
	if err != nil {
		return err
	}
	req = req.WithContext(ctx)

	client := http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	respData, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	if string(respData) != "ok" {
		glog.Warningf("Kubelet Healthz Endpoint returned: %s", string(respData))
		return nil
	}

	glog.V(2).Info("Kubelet health ok")

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
func (dn *Daemon) CheckStateOnBoot() error {
	// sanity check we're not already in a degraded state
	if state, err := getNodeAnnotationExt(dn.kubeClient.CoreV1().Nodes(), dn.name, MachineConfigDaemonStateAnnotationKey, true); err != nil {
		// try to set to degraded... because we failed to check if we're degraded
		return dn.nodeWriter.SetUpdateDegradedIgnoreErr(err, dn.kubeClient.CoreV1().Nodes(), dn.name)
	} else if state == MachineConfigDaemonStateDegraded {
		// just sleep so that we don't clobber output of previous run which
		// probably contains the real reason why we marked the node as degraded
		// in the first place
		glog.Info("Node is degraded; going to sleep")
		select {}
	}

	// validate machine state
	isDesired, dcAnnotation, err := dn.isDesiredMachineState()
	if err != nil {
		return dn.nodeWriter.SetUpdateDegradedIgnoreErr(err, dn.kubeClient.CoreV1().Nodes(), dn.name)
	}

	if isDesired {
		// we got the machine state we wanted. set the update complete!
		if err := dn.completeUpdate(dcAnnotation); err != nil {
			return dn.nodeWriter.SetUpdateDegradedIgnoreErr(err, dn.kubeClient.CoreV1().Nodes(), dn.name)
		}
	} else if err := dn.triggerUpdate(); err != nil {
		return err
	}

	return nil
}

// runOnceFromMachineConfig utilizes a parsed machineConfig and executes in onceFrom
// mode. If the content was remote, it executes cluster calls, otherwise it assumes
// no cluster is present yet.
func (dn *Daemon) runOnceFromMachineConfig(machineConfig mcfgv1.MachineConfig, contentFrom string) error {
	if contentFrom == MachineConfigOnceFromRemoteConfig {
		// NOTE: This case expects a cluster to exists already.
		needUpdate, err := dn.prepUpdateFromCluster()
		if err != nil {
			return dn.nodeWriter.SetUpdateDegradedIgnoreErr(err, dn.kubeClient.CoreV1().Nodes(), dn.name)
		} else if needUpdate == false {
			return nil
		}
		// At this point we have verified we need to update
		if err := dn.executeUpdateFromClusterWithMachineConfig(&machineConfig); err != nil {
			return dn.nodeWriter.SetUpdateDegradedIgnoreErr(err, dn.kubeClient.CoreV1().Nodes(), dn.name)
		}
		return nil

	} else if contentFrom == MachineConfigOnceFromLocalConfig {
		// NOTE: This case expects that the cluster is NOT CREATED YET.
		oldConfig := mcfgv1.MachineConfig{}
		// Execute update without hitting the cluster
		return dn.update(&oldConfig, &machineConfig)
	}
	// Otherwise return an error as the input format is unsupported
	return fmt.Errorf("%s is not a path nor url; can not run once", dn.onceFrom)
}

// runOnceFromIgnition executes MCD's subset of Ignition functionality in onceFrom mode
func (dn *Daemon) runOnceFromIgnition(ignConfig ignv2_2types.Config) error {
	// Execute update without hitting the cluster
	if err := dn.writeFiles(ignConfig.Storage.Files); err != nil {
		return err
	}
	if err := dn.writeUnits(ignConfig.Systemd.Units); err != nil {
		return err
	}
	return dn.reboot()
}

// handleNodeUpdate is the gatekeeper handler for informer callbacks detecting
// node changes. If an update is requested by the controller, we assume that
// that means something changed and pass over to execution methods no matter what.
// Also note that we only care about node updates, not creation or deletion.
func (dn *Daemon) handleNodeUpdate(old, cur interface{}) {
	node := cur.(*corev1.Node)

	// First check if the node that was updated is this daemon's node
	if node.Name == dn.name {
		// Pass to the shared update prep method
		needUpdate, err := dn.prepUpdateFromCluster()
		if err != nil {
			glog.Infof("Unable to prep update: %s", err)
			dn.exitCh<- err
			return
		}
		// Only executeUpdateFromCluster when we need to update
		if needUpdate {
			if err = dn.executeUpdateFromCluster(); err != nil {
				glog.Infof("Unable to apply update: %s", err)
				dn.exitCh<- err
				return
			}
		}
	}
	// The node that was changed was not ours, return out
	return
}

// prepUpdateFromCluster handles the shared update prepping functionality for
// flows that expect the cluster to already be available. Returns true if an
// update is required, false otherwise.
func (dn *Daemon) prepUpdateFromCluster() (bool, error) {
	// Then check we're not already in a degraded state.
	if state, err := getNodeAnnotation(dn.kubeClient.CoreV1().Nodes(), dn.name, MachineConfigDaemonStateAnnotationKey); err != nil {
		return false, err
	} else if state == MachineConfigDaemonStateDegraded {
		return false, fmt.Errorf("state is already degraded")
	}

	// Grab the node instance
	node, err := dn.kubeClient.CoreV1().Nodes().Get(dn.name, metav1.GetOptions{})
	if err != nil {
		return false, err
	}

	// Detect if there is an update
	if node.Annotations[DesiredMachineConfigAnnotationKey] == node.Annotations[CurrentMachineConfigAnnotationKey] {
		// No actual update to the config
		glog.V(2).Info("No updating is required")
		return false, nil
	}
	return true, nil
}

// executeUpdateFromClusterWithMachineConfig starts the actual update process. The provided config
// will be used as the desired config, while the current config will be pulled from the cluster. If
// you want both pulled from the cluster please use executeUpdateFromCluster().
func (dn *Daemon) executeUpdateFromClusterWithMachineConfig(desiredConfig *mcfgv1.MachineConfig) error {
	// The desired machine config has changed, trigger update
	if err := dn.triggerUpdateWithMachineConfig(desiredConfig); err != nil {
		return err
	}

	// we managed to update the machine without rebooting. in this case,
	// continue as usual waiting for the next update
	glog.V(2).Infof("Successfully updated without reboot")
	return nil
}

// executeUpdateFromCluster starts the actual update process using configs from the cluster.
func (dn *Daemon) executeUpdateFromCluster() error {
	return dn.executeUpdateFromClusterWithMachineConfig(nil)
}

// completeUpdate does all the stuff required to finish an update. right now, it
// sets the status annotation to Done and marks the node as schedulable again.
func (dn *Daemon) completeUpdate(dcAnnotation string) error {
	if err := dn.nodeWriter.SetUpdateDone(dn.kubeClient.CoreV1().Nodes(), dn.name, dcAnnotation); err != nil {
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

// triggerUpdateWithMachineConfig starts the update using the desired config and queries the cluster for
// the current config. If all configs should be pulled from the cluster use triggerUpdate().
func (dn *Daemon) triggerUpdateWithMachineConfig(desiredConfig *mcfgv1.MachineConfig) error {
	if err := dn.nodeWriter.SetUpdateWorking(dn.kubeClient.CoreV1().Nodes(), dn.name); err != nil {
		return err
	}

	ccAnnotation, err := getNodeAnnotation(dn.kubeClient.CoreV1().Nodes(), dn.name, CurrentMachineConfigAnnotationKey)
	if err != nil {
		return err
	}
	currentConfig, err := getMachineConfig(dn.client.MachineconfigurationV1().MachineConfigs(), ccAnnotation)
	if err != nil {
		return err
	}
	if desiredConfig == nil {
		dcAnnotation, err := getNodeAnnotation(dn.kubeClient.CoreV1().Nodes(), dn.name, DesiredMachineConfigAnnotationKey)
		if err != nil {
			return err
		}
		desiredConfig, err = getMachineConfig(dn.client.MachineconfigurationV1().MachineConfigs(), dcAnnotation)
		if err != nil {
			return err
		}
	}
	// run the update process. this function doesn't currently return.
	return dn.update(currentConfig, desiredConfig)
}

// triggerUpdate starts the update using the current and the target config.
func (dn *Daemon) triggerUpdate() error {
	return dn.triggerUpdateWithMachineConfig(nil)
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

	isDesiredOS := false
	// We only deal with operating system management on RHCOS
	if dn.OperatingSystem != MachineConfigDaemonOSRHCOS {
		// If we are on anything but RHCOS we set to True as there
		// is nothing to updated.
		isDesiredOS = true
	} else {
		isDesiredOS, err = dn.checkOS(desiredConfig.Spec.OSImageURL)
		if err != nil {
			return false, "", err
		}
	}

	if dn.checkFiles(desiredConfig.Spec.Config.Storage.Files) &&
		dn.checkUnits(desiredConfig.Spec.Config.Systemd.Units) &&
		isDesiredOS {
		return true, dcAnnotation, nil
	}

	// error is nil, as we successfully decided that validate is false
	return false, "", nil
}

// isUnspecifiedOS says whether an osImageURL is "unspecified",
// i.e. we should not try to change the current state.
func (dn *Daemon) isUnspecifiedOS(osImageURL string) bool {
	// The ://dummy syntax is legacy
	return osImageURL == "" || osImageURL == "://dummy";
}

// checkOS validates the OS image URL and returns true if they match.
func (dn *Daemon) checkOS(osImageURL string) (bool, error) {
	// XXX: The installer doesn't pivot yet so for now, just make ""
	// mean "unset, don't pivot". See also: https://github.com/openshift/installer/issues/281
	if dn.isUnspecifiedOS(osImageURL) {
		glog.Infof(`No target osImageURL provided`)
		return true, nil
	}
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
	// Retry for 5 minutes to get a MachineConfig in case of transient errors.
	var mc *mcfgv1.MachineConfig = nil
	err := wait.PollImmediate(10*time.Second, 5*time.Minute, func() (bool, error) {
		var err error
		mc, err = client.Get(name, metav1.GetOptions{})
		if err == nil {
			return true, nil
		}
		glog.Infof("While getting MachineConfig %s, got: %v. Retrying...", name, err)
		return false, nil
	})
	return mc, err
}

// ValidPath attempts to see if the path provided is indeed an acceptable
// filesystem path. This function does not check if the path exists.
func ValidPath(path string) bool {
	for _, validStart := range []string{".", "..", "/"} {
		if strings.HasPrefix(path, validStart) {
			return true
		}
	}
	return false
}

// SenseAndLoadOnceFrom gets a hold of the content for supported onceFrom configurations,
// parses to verify the type, and returns back the genericInterface, the type description,
// if it was local or remote, and error.
func (dn *Daemon) SenseAndLoadOnceFrom() (interface{}, string, string, error) {
	var content []byte
	var err error
	var contentFrom string
	// Read the content from a remote endpoint if requested
	if strings.HasPrefix(dn.onceFrom, "http://") || strings.HasPrefix(dn.onceFrom, "https://") {
		contentFrom = MachineConfigOnceFromRemoteConfig
		resp, err := http.Get(dn.onceFrom)
		if err != nil {
			return nil, "", contentFrom, err
		}
		defer resp.Body.Close()
		// Read the body content from the request
		content, err = dn.fileSystemClient.ReadAll(resp.Body)
		if err != nil {
			return nil, "", contentFrom, err
		}
	} else {
		// Otherwise read it from a local file
		contentFrom = MachineConfigOnceFromLocalConfig
		absoluteOnceFrom, err := filepath.Abs(filepath.Clean(dn.onceFrom))
		if err != nil {
			return nil, "", contentFrom, err
		}

		content, err = dn.fileSystemClient.ReadFile(absoluteOnceFrom)
		if err != nil {
			return nil, "", contentFrom, err
		}
	}

	// Try each supported parser
	ignConfig, _, err := ignv2.Parse(content)
	if err == nil && ignConfig.Ignition.Version != "" {
		glog.V(2).Infof("onceFrom file is of type Ignition")
		return ignConfig, MachineConfigIgnitionFileType, contentFrom, nil
	}

	glog.V(2).Infof("%s is not an Ignition config: %s. Trying MachineConfig.", dn.onceFrom, err)

	// TODO: Add to resource/machineconfig.go as a function?
	mcfgScheme := runtime.NewScheme()
	mcfgv1.AddToScheme(mcfgScheme)
	mcfgCodecs := serializer.NewCodecFactory(mcfgScheme)
	requiredObj, err := runtime.Decode(mcfgCodecs.UniversalDecoder(mcfgv1.SchemeGroupVersion), content)
	if err == nil {
		glog.V(2).Infof("onceFrom file is of type MachineConfig")
		mcConfig := requiredObj.(*mcfgv1.MachineConfig)
		return mcConfig, MachineConfigMCFileType, contentFrom, nil
	}

	return nil, "", "", fmt.Errorf("unable to decipher onceFrom config type: %s", err)
}

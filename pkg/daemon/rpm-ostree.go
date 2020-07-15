package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"time"

	"github.com/golang/glog"
	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	"github.com/openshift/machine-config-operator/pkg/daemon/constants"
	"github.com/pkg/errors"
)

const (
	// the number of times to retry commands that pull data from the network
	numRetriesNetCommands = 5
	// Pull secret.  Written by the machine-config-operator
	kubeletAuthFile = "/var/lib/kubelet/config.json"
)

// rpmOstreeState houses zero or more RpmOstreeDeployments
// Subset of `rpm-ostree status --json`
// https://github.com/projectatomic/rpm-ostree/blob/bce966a9812df141d38e3290f845171ec745aa4e/src/daemon/rpmostreed-deployment-utils.c#L227
type rpmOstreeState struct {
	Deployments []RpmOstreeDeployment
}

// RpmOstreeDeployment represents a single deployment on a node
type RpmOstreeDeployment struct {
	ID                 string   `json:"id"`
	OSName             string   `json:"osname"`
	Serial             int32    `json:"serial"`
	Checksum           string   `json:"checksum"`
	Version            string   `json:"version"`
	Timestamp          uint64   `json:"timestamp"`
	Booted             bool     `json:"booted"`
	Origin             string   `json:"origin"`
	CustomOrigin       []string `json:"custom-origin"`
	RequestedLocalPkgs []string `json:"requested-local-packages"`
}

// imageInspection is motivated from podman upstream podman inspect
// https://github.com/containers/podman/blob/master/pkg/inspect/inspect.go#L13
type containerInspection struct {
	Name        string
	ImageName   string
	Created     *time.Time
	Config      *ImageConfig
	GraphDriver *Data
}

// Data handles the data for a storage driver
type Data struct {
	Name string
	Data map[string]string
}

// ImageConfig defines the execution parameters which should be used as a base when running a container using an image.
type ImageConfig struct {
	Labels map[string]string
}

// NodeUpdaterClient is an interface describing how to interact with the host
// around content deployment
type NodeUpdaterClient interface {
	GetStatus() (string, error)
	GetBootedOSImageURL() (string, string, error)
	RunPivot(string) error
	Rebase(string) (bool, error)
	GetBootedDeployment() (*RpmOstreeDeployment, error)
	PerformRpmOSTreeOperations(string, bool) (bool, error)
}

// RpmOstreeClient provides all RpmOstree related methods in one structure.
// This structure implements DeploymentClient
//
// TODO(runcom): make this private to pkg/daemon!!!
type RpmOstreeClient struct{}

// NewNodeUpdaterClient returns a new instance of the default DeploymentClient (RpmOstreeClient)
func NewNodeUpdaterClient() NodeUpdaterClient {
	return &RpmOstreeClient{}
}

// GetBootedDeployment returns the current deployment found
func (r *RpmOstreeClient) GetBootedDeployment() (*RpmOstreeDeployment, error) {
	var rosState rpmOstreeState
	output, err := runGetOut("rpm-ostree", "status", "--json")
	if err != nil {
		return nil, err
	}

	if err := json.Unmarshal(output, &rosState); err != nil {
		return nil, fmt.Errorf("failed to parse `rpm-ostree status --json` output: %v", err)
	}

	for _, deployment := range rosState.Deployments {
		if deployment.Booted {
			deployment := deployment
			return &deployment, nil
		}
	}

	return nil, fmt.Errorf("not currently booted in a deployment")
}

// GetStatus returns multi-line human-readable text describing system status
func (r *RpmOstreeClient) GetStatus() (string, error) {
	output, err := runGetOut("rpm-ostree", "status")
	if err != nil {
		return "", err
	}

	return string(output), nil
}

// GetBootedOSImageURL returns the image URL as well as the OSTree version (for logging)
// Returns the empty string if the host doesn't have a custom origin that matches pivot://
// (This could be the case for e.g. FCOS, or a future RHCOS which comes not-pivoted by default)
func (r *RpmOstreeClient) GetBootedOSImageURL() (string, string, error) {
	bootedDeployment, err := r.GetBootedDeployment()
	if err != nil {
		return "", "", err
	}

	// the canonical image URL is stored in the custom origin field.
	osImageURL := ""
	if len(bootedDeployment.CustomOrigin) > 0 {
		if strings.HasPrefix(bootedDeployment.CustomOrigin[0], "pivot://") {
			osImageURL = bootedDeployment.CustomOrigin[0][len("pivot://"):]
		}
	}

	return osImageURL, bootedDeployment.Version, nil
}

// podmanRemove kills and removes a container
func podmanRemove(cid string) {
	// Ignore errors here
	exec.Command("podman", "kill", cid).Run()
	exec.Command("podman", "rm", "-f", cid).Run()
}

func readMachineConfigs() (*mcfgv1.MachineConfig, *mcfgv1.MachineConfig, error) {
	var oldCfgFile, newCfgFile *os.File
	var oldCfg, newCfg *mcfgv1.MachineConfig
	var err error

	if oldCfgFile, err = os.Open(constants.OldMachineConfigPath); err != nil {
		return nil, nil, err
	}
	defer oldCfgFile.Close()
	decoder := json.NewDecoder(oldCfgFile)
	if err := decoder.Decode(&oldCfg); err != nil {
		return nil, nil, err
	}

	if newCfgFile, err = os.Open(constants.NewMachineConfigPath); err != nil {
		return nil, nil, err
	}
	defer newCfgFile.Close()
	decoder = json.NewDecoder(newCfgFile)
	if err := decoder.Decode(&newCfg); err != nil {
		return nil, nil, err
	}

	return oldCfg, newCfg, nil
}

func generateExtensionsArgs(oldConfig, newConfig *mcfgv1.MachineConfig) []string {
	removed := []string{}
	added := []string{}

	oldExt := make(map[string]bool)
	for _, ext := range oldConfig.Spec.Extensions {
		oldExt[ext] = true
	}
	newExt := make(map[string]bool)
	for _, ext := range newConfig.Spec.Extensions {
		newExt[ext] = true
	}

	for ext := range oldExt {
		if !newExt[ext] {
			removed = append(removed, ext)
		}
	}
	for ext := range newExt {
		if !oldExt[ext] {
			added = append(added, ext)
		}
	}

	extArgs := []string{"update"}
	for _, ext := range added {
		extArgs = append(extArgs, "--install", ext)
	}
	for _, ext := range removed {
		extArgs = append(extArgs, "--uninstall", ext)
	}

	return extArgs
}

// applyExtensions processes specified extensions if it is supported on RHCOS.
// It deletes an extension if it is no logner available in new rendered MachineConfig.
// Newly requested extension will be installed and existing extensions gets updated
// if we have a new version of the extension available in machine-os-content.
func applyExtensions(oldConfig, newConfig *mcfgv1.MachineConfig) error {
	extensionsEmpty := len(oldConfig.Spec.Extensions) == 0 && len(newConfig.Spec.Extensions) == 0
	if (extensionsEmpty) ||
		(reflect.DeepEqual(oldConfig.Spec.Extensions, newConfig.Spec.Extensions) && oldConfig.Spec.OSImageURL == newConfig.Spec.OSImageURL) {
		return nil
	}
	args := generateExtensionsArgs(oldConfig, newConfig)
	glog.Infof("Applying extensionss : %+q", args)
	if err := exec.Command("rpm-ostree", args...).Run(); err != nil {
		glog.Infof("Failed to execute rpm-ostree %+q : %v", args, err)
		return fmt.Errorf("Failed to execute rpm-ostree %+q : %v", args, err)
	}

	return nil
}

// PerformRpmOSTreeOperations runs all rpm-ostree related operations
func (r *RpmOstreeClient) PerformRpmOSTreeOperations(containerName string, keep bool) (changed bool, err error) {
	var oldConfig, newConfig *mcfgv1.MachineConfig
	// Check if we have reached here through update() or by directly calling m-c-d pivot
	// In the latter case MachineConfig won't get written on host. In such situation let's just Rebase()
	// and return back. Checking just oldconfig should be enough.
	if _, err = os.Stat(constants.OldMachineConfigPath); err != nil {
		if os.IsNotExist(err) {
			glog.Infof("m-c-d pivot got called outside of MCO: Updating OS")
			if changed, err = r.Rebase(containerName); err != nil {
				return
			}
		}
		return
	}

	if oldConfig, newConfig, err = readMachineConfigs(); err != nil {
		return
	}

	// FIXME: moved rebase before extension becasue rpm-ostree fails to rebase on top of applied extensions
	// during firstboot test. Maybe related to https://discussion.fedoraproject.org/t/bus-owner-changed-aborting-when-trying-to-upgrade/1919/
	// We may need to reset package layering before applying rebase.
	if oldConfig.Spec.OSImageURL != newConfig.Spec.OSImageURL {
		glog.Infof("Updating OS")
		if changed, err = r.Rebase(containerName); err != nil {
			return
		}
	}

	// Apply and update extensions
	if err = applyExtensions(oldConfig, newConfig); err != nil {
		return
	}

	// Switch to real time kernel
	if err = switchKernel(oldConfig, newConfig); err != nil {
		return
	}

	return

}

func inspectContainer(containerName string) (*containerInspection, error) {
	inspectArgs := []string{"inspect", "--type=container"}
	inspectArgs = append(inspectArgs, fmt.Sprintf("%s", containerName))
	var output []byte
	output, err := runGetOut("podman", inspectArgs...)
	if err != nil {
		return nil, err
	}
	var imagedataArray []containerInspection
	err = json.Unmarshal(output, &imagedataArray)
	if err != nil {
		err = errors.Wrapf(err, "unmarshaling podman inspect")
		return nil, err
	}
	return &imagedataArray[0], nil

}

// Rebase potentially rebases system if not already rebased.
func (r *RpmOstreeClient) Rebase(containerName string) (changed bool, err error) {
	defaultDeployment, err := r.GetBootedDeployment()
	if err != nil {
		return
	}

	previousPivot := ""
	if len(defaultDeployment.CustomOrigin) > 0 {
		if strings.HasPrefix(defaultDeployment.CustomOrigin[0], "pivot://") {
			previousPivot = defaultDeployment.CustomOrigin[0][len("pivot://"):]
			glog.Infof("Previous pivot: %s", previousPivot)
		} else {
			glog.Infof("Previous custom origin: %s", defaultDeployment.CustomOrigin[0])
		}
	} else {
		glog.Info("Current origin is not custom")
	}

	var imagedata *containerInspection
	if imagedata, err = inspectContainer(containerName); err != nil {
		return
	}

	containerImageName := imagedata.ImageName
	glog.Infof("Container Image is : %s", containerImageName)

	repo := fmt.Sprintf("%s/srv/repo", imagedata.GraphDriver.Data["MergedDir"])
	glog.Infof("Mounted Dir %s", imagedata.GraphDriver.Data["MergedDir"])

	// Now we need to figure out the commit to rebase to

	// Commit label takes priority
	ostreeCsum, ok := imagedata.Config.Labels["com.coreos.ostree-commit"]
	if ok {
		if ostreeVersion, ok := imagedata.Config.Labels["version"]; ok {
			glog.Infof("Pivoting to: %s (%s)", ostreeVersion, ostreeCsum)
		} else {
			glog.Infof("Pivoting to: %s", ostreeCsum)
		}
	} else {
		glog.Infof("No com.coreos.ostree-commit label found in metadata! Inspecting...")
		var refText []byte
		refText, err = runGetOut("ostree", "refs", "--repo", repo)
		if err != nil {
			return
		}
		refs := strings.Split(strings.TrimSpace(string(refText)), "\n")
		if len(refs) == 1 {
			glog.Infof("Using ref %s", refs[0])
			var ostreeCsumBytes []byte
			ostreeCsumBytes, err = runGetOut("ostree", "rev-parse", "--repo", repo, refs[0])
			if err != nil {
				return
			}
			ostreeCsum = strings.TrimSpace(string(ostreeCsumBytes))
		} else if len(refs) > 1 {
			err = errors.New("multiple refs found in repo")
			return
		} else {
			// XXX: in the future, possibly scan the repo to find a unique .commit object
			err = errors.New("No refs found in repo")
			return
		}
	}

	glog.Infof("Updating OS to %s", containerImageName)

	// This will be what will be displayed in `rpm-ostree status` as the "origin spec"
	customURL := fmt.Sprintf("pivot://%s", containerImageName)

	// RPM-OSTree can now directly slurp from the mounted container!
	// https://github.com/projectatomic/rpm-ostree/pull/1732
	err = exec.Command("rpm-ostree", "rebase", "--experimental",
		fmt.Sprintf("%s:%s", repo, ostreeCsum),
		"--custom-origin-url", customURL,
		"--custom-origin-description", "Managed by machine-config-operator").Run()
	if err != nil {
		return
	}

	changed = true
	return
}

// RunPivot executes a pivot from one deployment to another as found in the referenced
// osImageURL. This was originally https://github.com/openshift/pivot but it's now
// imported into the MCD itself.  The reason we execute on the host is due to SELinux;
// see https://github.com/openshift/pivot/pull/31 and
// https://github.com/openshift/machine-config-operator/issues/314
// Basically rpm_ostree_t has mac_admin, container_t doesn't.
func (r *RpmOstreeClient) RunPivot(ContainerName string) error {
	journalStopCh := make(chan time.Time)
	defer close(journalStopCh)
	go followPivotJournalLogs(journalStopCh)

	// These were previously injected by the MCS, let's clean them up if they exist
	for _, p := range []string{constants.EtcPivotFile, "/run/pivot/reboot-needed"} {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return errors.Wrapf(err, "deleting %s", p)
		}
	}

	// We used to start machine-config-daemon-host here, but now we make a dynamic
	// unit because that service was started in too many ways, and the systemd-run
	// model of creating a unit dynamically is much clearer for what we want here;
	// conceptually the service is just a dynamic child of this pod (if we could we'd
	// tie the lifecycle together).  Further, let's shorten our systemd unit names
	// by using the mco- prefix, and we also inject the RPMOSTREE_CLIENT_ID now.
	unitName := "mco-pivot"
	glog.Infof("Executing OS update (pivot) on host via systemd-run unit=%s", unitName)
	err := exec.Command("systemd-run", "--wait", "--collect", "--unit="+unitName,
		"-E", "RPMOSTREE_CLIENT_ID=mco", constants.HostSelfBinary, "pivot", ContainerName).Run()
	if err != nil {
		return errors.Wrapf(err, "failed to run pivot")
	}
	return nil
}

// Proxy pivot and rpm-ostree daemon journal logs until told to stop. Warns if
// we encounter an error.
func followPivotJournalLogs(stopCh <-chan time.Time) {
	cmd := exec.Command("journalctl", "-f", "-b", "-o", "cat",
		"-u", "rpm-ostreed",
		"-u", "mco-pivot")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		glog.Fatal(err)
		MCDPivotErr.WithLabelValues("", err.Error()).SetToCurrentTime()
	}

	go func() {
		<-stopCh
		cmd.Process.Kill()
	}()
}

// runGetOut executes a command, logging it, and return the stdout output.
func runGetOut(command string, args ...string) ([]byte, error) {
	glog.Infof("Running captured: %s %s", command, strings.Join(args, " "))
	cmd := exec.Command(command, args...)
	cmd.Stderr = os.Stderr
	rawOut, err := cmd.Output()
	if err != nil {
		return nil, errors.Wrapf(err, "error running %s %s: %s", command, strings.Join(args, " "), string(rawOut))
	}
	return rawOut, nil
}

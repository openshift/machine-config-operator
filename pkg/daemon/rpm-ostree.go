package daemon

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/golang/glog"
	"github.com/opencontainers/go-digest"
	"github.com/openshift/machine-config-operator/pkg/daemon/constants"
	pivottypes "github.com/openshift/machine-config-operator/pkg/daemon/pivot/types"
	pivotutils "github.com/openshift/machine-config-operator/pkg/daemon/pivot/utils"
	"github.com/pkg/errors"
	"k8s.io/apimachinery/pkg/util/uuid"
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
	Deployments []rpmOstreeDeployment
}

// rpmOstreeDeployment represents a single deployment on a node
type rpmOstreeDeployment struct {
	ID           string   `json:"id"`
	OSName       string   `json:"osname"`
	Serial       int32    `json:"serial"`
	Checksum     string   `json:"checksum"`
	Version      string   `json:"version"`
	Timestamp    uint64   `json:"timestamp"`
	Booted       bool     `json:"booted"`
	Origin       string   `json:"origin"`
	CustomOrigin []string `json:"custom-origin"`
}

// imageInspection is a public implementation of
// https://github.com/containers/skopeo/blob/82186b916faa9c8c70cfa922229bafe5ae024dec/cmd/skopeo/inspect.go#L20-L31
type imageInspection struct {
	Name          string `json:",omitempty"`
	Tag           string `json:",omitempty"`
	Digest        digest.Digest
	RepoDigests   []string
	Created       *time.Time
	DockerVersion string
	Labels        map[string]string
	Architecture  string
	Os            string
	Layers        []string
}

// NodeUpdaterClient is an interface describing how to interact with the host
// around content deployment
type NodeUpdaterClient interface {
	GetStatus() (string, error)
	GetBootedOSImageURL() (string, string, error)
	PullAndRebase(string, bool) (string, bool, error)
	RunPivot(string) error
}

// TODO(runcom): make this private to pkg/daemon!!!
//
// RpmOstreeClient provides all RpmOstree related methods in one structure.
// This structure implements DeploymentClient
type RpmOstreeClient struct{}

// NewNodeUpdaterClient returns a new instance of the default DeploymentClient (RpmOstreeClient)
func NewNodeUpdaterClient() NodeUpdaterClient {
	return &RpmOstreeClient{}
}

// getBootedDeployment returns the current deployment found
func (r *RpmOstreeClient) getBootedDeployment() (*rpmOstreeDeployment, error) {
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
func (r *RpmOstreeClient) GetBootedOSImageURL() (string, string, error) {
	bootedDeployment, err := r.getBootedDeployment()
	if err != nil {
		return "", "", err
	}

	// the canonical image URL is stored in the custom origin field by the pivot tool
	osImageURL := "<not pivoted>"
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

// pullAndRebase potentially rebases system if not already rebased.
func (r *RpmOstreeClient) PullAndRebase(container string, keep bool) (imgid string, changed bool, err error) {
	defaultDeployment, err := r.getBootedDeployment()
	if err != nil {
		return
	}

	previousPivot := ""
	if len(defaultDeployment.CustomOrigin) > 0 {
		if strings.HasPrefix(defaultDeployment.CustomOrigin[0], "pivot://") {
			previousPivot = defaultDeployment.CustomOrigin[0][len("pivot://"):]
			glog.Infof("Previous pivot: %s", previousPivot)
		}
	}

	var authArgs []string
	if _, err := os.Stat(kubeletAuthFile); err == nil {
		authArgs = append(authArgs, "--authfile", kubeletAuthFile)
	}

	// If we're passed a non-canonical image, resolve it to its sha256 now
	isCanonicalForm := true
	if _, err = getRefDigest(container); err != nil {
		isCanonicalForm = false
		// In non-canonical form, we pull unconditionally right now
		args := []string{"pull", "-q"}
		args = append(args, authArgs...)
		args = append(args, container)
		pivotutils.RunExt(false, numRetriesNetCommands, "podman", args...)
	} else {
		var targetMatched bool
		targetMatched, err = compareOSImageURL(previousPivot, container)
		if err != nil {
			return
		}
		if targetMatched {
			changed = false
			return
		}

		// Pull the image
		args := []string{"pull", "-q"}
		args = append(args, authArgs...)
		args = append(args, container)
		pivotutils.RunExt(false, numRetriesNetCommands, "podman", args...)
	}

	inspectArgs := []string{"inspect", "--type=image"}
	inspectArgs = append(inspectArgs, fmt.Sprintf("%s", container))
	var output []byte
	output, err = runGetOut("podman", inspectArgs...)
	if err != nil {
		return
	}
	var imagedataArray []imageInspection
	err = json.Unmarshal(output, &imagedataArray)
	if err != nil {
		err = errors.Wrapf(err, "unmarshaling podman inspect")
		return
	}
	imagedata := imagedataArray[0]
	if !isCanonicalForm {
		imgid = imagedata.RepoDigests[0]
		glog.Infof("Resolved to: %s", imgid)
	} else {
		imgid = container
	}

	containerName := pivottypes.PivotNamePrefix + string(uuid.NewUUID())

	// `podman mount` wants a container, so let's make create a dummy one, but not run it
	var cidBuf []byte
	cidBuf, err = runGetOut("podman", "create", "--net=none", "--annotation=org.openshift.machineconfigoperator.pivot=true", "--name", containerName, imgid)
	if err != nil {
		return
	}
	defer func() {
		// Kill our dummy container
		podmanRemove(containerName)
	}()

	cid := strings.TrimSpace(string(cidBuf))
	// Use the container ID to find its mount point
	var mntBuf []byte
	mntBuf, err = runGetOut("podman", "mount", cid)
	if err != nil {
		return
	}
	mnt := strings.TrimSpace(string(mntBuf))
	repo := fmt.Sprintf("%s/srv/repo", mnt)

	// Now we need to figure out the commit to rebase to

	// Commit label takes priority
	ostreeCsum, ok := imagedata.Labels["com.coreos.ostree-commit"]
	if ok {
		if ostreeVersion, ok := imagedata.Labels["version"]; ok {
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

	// This will be what will be displayed in `rpm-ostree status` as the "origin spec"
	customURL := fmt.Sprintf("pivot://%s", imgid)

	// RPM-OSTree can now directly slurp from the mounted container!
	// https://github.com/projectatomic/rpm-ostree/pull/1732
	err = exec.Command("rpm-ostree", "rebase", "--experimental",
		fmt.Sprintf("%s:%s", repo, ostreeCsum),
		"--custom-origin-url", customURL,
		"--custom-origin-description", "Managed by machine-config-operator").Run()
	if err != nil {
		return
	}

	// By default, delete the image.
	if !keep {
		// Related: https://github.com/containers/libpod/issues/2234
		exec.Command("podman", "rmi", imgid).Run()
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
func (r *RpmOstreeClient) RunPivot(osImageURL string) error {
	if err := os.MkdirAll(filepath.Dir(constants.EtcPivotFile), os.FileMode(0755)); err != nil {
		return fmt.Errorf("error creating leading dirs for %s: %v", constants.EtcPivotFile, err)
	}

	if err := ioutil.WriteFile(constants.EtcPivotFile, []byte(osImageURL), 0644); err != nil {
		return fmt.Errorf("error writing to %s: %v", constants.EtcPivotFile, err)
	}

	journalStopCh := make(chan time.Time)
	defer close(journalStopCh)
	go followPivotJournalLogs(journalStopCh)

	// This is written by code injected by the MCS, but we always want the MCD to be in control of reboots
	if err := os.Remove("/run/pivot/reboot-needed"); err != nil && !os.IsNotExist(err) {
		return errors.Wrap(err, "deleting pivot reboot-needed file")
	}

	service := "machine-config-daemon-host.service"
	// We need to use pivot if it's there, because machine-config-daemon-host.service
	// currently has a ConditionPathExists=!/usr/lib/systemd/system/pivot.service to
	// avoid having *both* pivot and MCD try to update.  This code can be dropped
	// once we don't need to care about compat with older RHCOS.
	var err error
	_, err = os.Stat("/usr/lib/systemd/system/pivot.service")
	if err != nil {
		if !os.IsNotExist(err) {
			return errors.Wrapf(err, "checking pivot service")
		}
	} else {
		service = "pivot.service"
	}
	err = exec.Command("systemctl", "start", service).Run()
	if err != nil {
		return errors.Wrapf(err, "failed to start %s", service)
	}
	return nil
}

// Proxy pivot and rpm-ostree daemon journal logs until told to stop. Warns if
// we encounter an error.
func followPivotJournalLogs(stopCh <-chan time.Time) {
	cmd := exec.Command("journalctl", "-f", "-b", "-o", "cat",
		"-u", "rpm-ostreed",
		"-u", "pivot",
		"-u", "machine-config-daemon-host")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		glog.Fatal(err)
	}

	go func() {
		<-stopCh
		cmd.Process.Kill()
	}()
}

// runGetOut executes a command, logging it, and return the stdout output.
func runGetOut(command string, args ...string) ([]byte, error) {
	glog.Infof("Running captured: %s %s\n", command, strings.Join(args, " "))
	cmd := exec.Command(command, args...)
	cmd.Stderr = os.Stderr
	rawOut, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	return rawOut, nil
}

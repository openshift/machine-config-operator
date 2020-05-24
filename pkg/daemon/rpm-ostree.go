package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
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
	GetBootedDeployment() (*RpmOstreeDeployment, error)
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

// PullAndRebase potentially rebases system if not already rebased.
func (r *RpmOstreeClient) PullAndRebase(container string, keep bool) (imgid string, changed bool, err error) {
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
		if previousPivot != "" {
			var targetMatched bool
			targetMatched, err = compareOSImageURL(previousPivot, container)
			if err != nil {
				return
			}
			if targetMatched {
				changed = false
				return
			}
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
		"-E", "RPMOSTREE_CLIENT_ID=mco", constants.HostSelfBinary, "pivot", osImageURL).Run()
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

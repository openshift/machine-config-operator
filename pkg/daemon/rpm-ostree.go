package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/containers/image/v5/signature"
	rpmostreeclient "github.com/coreos/rpmostree-client-go/pkg/client"
	"gopkg.in/yaml.v2"
	"k8s.io/klog/v2"
)

const imagePolicyTransportContainerStorage = "containers-storage"
const imagePolicyFilePath = "/etc/containers/policy.json"
const rpmOstreeTemporalDropinFile = "/run/systemd/system/rpm-ostreed.service.d/temporal-policy-binding.conf"
const rpmOstreeTemporalPolicyFile = "/run/tmp-rpm-ostree-policy.json"

// RpmOstreeClient provides all RpmOstree related methods in one structure.
// This structure implements DeploymentClient
//
// TODO(runcom): make this private to pkg/daemon!!!
type RpmOstreeClient struct {
	client          rpmostreeclient.Client
	commandRunner   CommandRunner
	podmanInterface PodmanInterface
	systemdManager  SystemdManager
}

// NewNodeUpdaterClient is a wrapper to create an RpmOstreeClient
func NewNodeUpdaterClient(commandRunner CommandRunner, podmanInterface PodmanInterface, systemdManager SystemdManager) RpmOstreeClient {
	return RpmOstreeClient{
		client:          rpmostreeclient.NewClient("machine-config-daemon"),
		commandRunner:   commandRunner,
		podmanInterface: podmanInterface,
		systemdManager:  systemdManager,
	}
}

// Synchronously invoke rpm-ostree, writing its stdout to our stdout,
// and gathering stderr into a buffer which will be returned in err
// in case of error.
func runRpmOstree(args ...string) error {
	return runCmdSync("rpm-ostree", args...)
}

// See https://bugzilla.redhat.com/show_bug.cgi?id=2111817
func (r *RpmOstreeClient) bug2111817Workaround() error {
	targetUnit := "/run/systemd/system/rpm-ostreed.service.d/bug2111817.conf"
	// Do nothing if the file exists
	if _, err := os.Stat(targetUnit); err == nil {
		return nil
	}
	err := os.MkdirAll(filepath.Dir(targetUnit), 0o755)
	if err != nil {
		return err
	}
	dropin := `[Service]
InaccessiblePaths=
`
	if err := writeFileAtomicallyWithDefaults(targetUnit, []byte(dropin)); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	if err := r.systemdManager.DoConnection(ctx, SystemdReloadDaemon()); err != nil {
		return fmt.Errorf("failed to reload daemon for rpm-ostree 2111817 bug : %w", err)
	}
	klog.Infof("Enabled workaround for bug 2111817")
	return nil
}

func (r *RpmOstreeClient) Initialize() error {
	if err := r.bug2111817Workaround(); err != nil {
		return err
	}
	// Ensure the temporal ostree dropin doesn't exist
	// It shouldn't, but it's possible if the MCD container
	// suddenly died before rpm-ostree rebase finished
	if err := r.cleanupTemporalOstreePolicyFiles(); err != nil {
		return err
	}

	// Commands like update and rebase need the pull secrets to pull images and manifests,
	// make sure we get access to them when we Initialize
	err := useMergedPullSecrets(rpmOstreeSystem)
	if err != nil {
		klog.Errorf("error while linking rpm-ostree pull secrets %v", err)
	}

	return nil
}

func (r *RpmOstreeClient) Peel() *rpmostreeclient.Client {
	return &r.client
}

// GetBootedDeployment returns the current deployment found
func (r *RpmOstreeClient) GetBootedAndStagedDeployment() (*rpmostreeclient.Deployment, *rpmostreeclient.Deployment, error) {
	status, err := r.client.QueryStatus()
	if err != nil {
		return nil, nil, err
	}

	booted, err := status.GetBootedDeployment()
	if err != nil {
		return nil, nil, err
	}
	staged := status.GetStagedDeployment()

	return booted, staged, nil
}

// GetBootedOSImageURL returns the image URL as well as the OSTree version(for logging) and the ostree commit (for comparisons)
// Returns the empty string if the host doesn't have a custom origin that matches pivot://
// (This could be the case for e.g. FCOS, or a future RHCOS which comes not-pivoted by default)
func (r *RpmOstreeClient) GetBootedOSImageURL() (string, string, string, error) {
	bootedDeployment, _, err := r.GetBootedAndStagedDeployment()
	if err != nil {
		return "", "", "", err
	}

	// the canonical image URL is stored in the custom origin field.
	osImageURL := ""
	if len(bootedDeployment.CustomOrigin) > 0 {
		if strings.HasPrefix(bootedDeployment.CustomOrigin[0], "pivot://") {
			osImageURL = bootedDeployment.CustomOrigin[0][len("pivot://"):]
		}
	}

	// we have container images now, make sure we can parse those too
	if bootedDeployment.ContainerImageReference != "" {
		// right now remove ostree remote, and transport from container image reference
		ostreeImageReference, err := bootedDeployment.RequireContainerImage()
		if err != nil {
			return "", "", "", err
		}
		osImageURL = ostreeImageReference.Imgref.Image
	}

	baseChecksum := bootedDeployment.GetBaseChecksum()
	return osImageURL, bootedDeployment.Version, baseChecksum, nil
}

// RpmOstreeIsNewEnoughForLayering returns true if the version of rpm-ostree on the
// host system is new enough for layering.
// VersionData represents the static information about rpm-ostree.
type VersionData struct {
	Version  string   `yaml:"Version"`
	Features []string `yaml:"Features"`
	Git      string   `yaml:"Git"`
}

type RpmOstreeVersionData struct {
	Root VersionData `yaml:"rpm-ostree"`
}

// RpmOstreeVersion returns the running rpm-ostree version number
func (r *RpmOstreeClient) rpmOstreeVersion() (*VersionData, error) {
	buf, err := r.commandRunner.RunGetOut("rpm-ostree", "--version")
	if err != nil {
		return nil, err
	}

	var q RpmOstreeVersionData
	if err := yaml.Unmarshal(buf, &q); err != nil {
		return nil, fmt.Errorf("failed to parse `rpm-ostree --version` output: %w", err)
	}

	return &q.Root, nil
}

func (r *RpmOstreeClient) IsNewEnoughForLayering() (bool, error) {
	verdata, err := r.rpmOstreeVersion()
	if err != nil {
		return false, err
	}
	for _, v := range verdata.Features {
		if v == "container" {
			return true, nil
		}
	}
	return false, nil
}

// RebaseLayered rebases system or errors if already rebased.
func (r *RpmOstreeClient) RebaseLayered(imgURL string) error {
	// Try to re-link the merged pull secrets if they exist, since it could have been populated without a daemon reboot
	if err := useMergedPullSecrets(rpmOstreeSystem); err != nil {
		return fmt.Errorf("Error while ensuring access to pull secrets: %w", err)
	}
	klog.Infof("Executing rebase to %s", imgURL)
	return runRpmOstree("rebase", "--experimental", "ostree-unverified-registry:"+imgURL)
}

// RebaseLayeredFromContainerStorage rebases the system from an existing local container storage image.
func (r *RpmOstreeClient) RebaseLayeredFromContainerStorage(podmanImageInfo *PodmanImageInfo) error {
	// Try to re-link the merged pull secrets if they exist, since it could have been populated without a daemon reboot
	if err := useMergedPullSecrets(rpmOstreeSystem); err != nil {
		return fmt.Errorf("Error while ensuring access to pull secrets: %w", err)
	}

	defer func() {
		// Call the cleanup always, just in case there are left-overs of
		// a previous killed MCD (unlikely, but possible)
		if err := r.cleanupTemporalOstreePolicyFiles(); err != nil {
			klog.Errorf("Error deleting temporary MCD temporal policy %v", err)
		}
	}()
	// Temporary patch the containers policies to allow rpm-ostree to pull
	// an image from local storage. Only required if the policies are
	// restrictive and won't allow containers-storage transport pulls.
	if err := r.patchPoliciesForContainerStorage(podmanImageInfo); err != nil {
		// Swallow the error and let it fail in case the user/default defined policies
		// avoids pulling the image
		klog.Errorf("Error writing temporal policy files %v", err)
	}

	klog.Infof("Executing local container storage rebase to %s", podmanImageInfo.RepoDigest)
	return runRpmOstree("rebase", "--experimental", "ostree-unverified-image:containers-storage:"+podmanImageInfo.RepoDigest)
}

// DeploymentVersion wraps rpm-ostree deployment information to provide
// version checking methods similar to the osrelease package.
type DeploymentVersion struct {
	// osName is the osname field from the deployment
	osName string
	// version is the version field from the deployment
	version string
}

// NewTargetOSVersionFromDeployment creates a DeploymentVersion from the given deployment
func NewTargetOSVersionFromDeployment(deployment *rpmostreeclient.Deployment) *DeploymentVersion {
	return &DeploymentVersion{
		osName:  deployment.OSName,
		version: deployment.Version,
	}
}

// BaseVersionMajor returns the first number in a `.` separated version.
func (t *DeploymentVersion) BaseVersionMajor() string {
	return strings.Split(t.version, ".")[0]
}

// IsEL is true if the OS is an Enterprise Linux variant of CoreOS
func (t *DeploymentVersion) IsEL() bool {
	return t.osName == "rhcos" || t.osName == "scos"
}

// IsEL10 is true if the target OS is RHCOS 10 or SCOS 10
func (t *DeploymentVersion) IsEL10() bool {
	return t.IsEL() && (strings.HasPrefix(t.version, "10.") || t.version == "10")
}

// patchPoliciesForContainerStorage temporarily overrides the container image policy visible
// to rpm-ostreed to ensure pulls from the "containers-storage" transport are allowed for the
// given image.
//
// This is necessary for tools like rpm-ostree to function correctly with locally
// stored images, especially in environments with restrictive security policies. A common
// scenario is a user removing the default "insecureAcceptAnything" policy without
// adding an explicit rule for local storage, which is an easily missed implementation detail.
//
// The function is idempotent and will not modify the policy if it's already permissive
// enough for local storage pulls.
//
// To avoid modifying the system's policy file, this function creates a temporary policy file
// at rpmOstreeTemporalPolicyFile and uses a systemd drop-in to bind-mount it over the actual
// policy file for the rpm-ostreed service. The drop-in and temporary policy file are cleaned
// up after the rpm-ostree operation completes.
func (r *RpmOstreeClient) patchPoliciesForContainerStorage(podmanImageInfo *PodmanImageInfo) error {
	url, err := r.generateTransportPolicyKeyForReference(podmanImageInfo)
	if err != nil {
		return err
	}
	_, err = os.Stat(imagePolicyFilePath)
	if err != nil {
		if os.IsNotExist(err) {
			klog.Warningf("No policy file found at %s. Skipping temporal policy generation.", imagePolicyFilePath)
			return nil
		}
		return err
	}

	policyOriginalContent, err := os.ReadFile(imagePolicyFilePath)
	if err != nil {
		return err
	}

	policy, err := signature.NewPolicyFromBytes(policyOriginalContent)
	if err != nil {
		return err
	}

	_, containerStoragePoliciesPresent := policy.Transports[imagePolicyTransportContainerStorage]
	if (reflect.DeepEqual(policy.Default[0], signature.PolicyRequirements{signature.NewPRInsecureAcceptAnything()}) && !containerStoragePoliciesPresent) {
		// Temporary patching the policies.json file can be skipped, with warranties, and without re-implementing the
		// logic that evaluates the policies or importing it under the following circumstances (must match all):
		//  1. The default policy should be "insecureAcceptAnything"
		//  2. Transport-specific policies for containers-storage shouldn't be in place.
		return nil
	}

	// At this point there's no warranty the policy will allow rpm-ostree to fetch the image
	// from local storage -> Add a specific rule to allow the image
	if !containerStoragePoliciesPresent {
		policy.Transports[imagePolicyTransportContainerStorage] = make(map[string]signature.PolicyRequirements)
	}
	policy.Transports[imagePolicyTransportContainerStorage][url] = signature.PolicyRequirements{
		signature.NewPRInsecureAcceptAnything(),
	}

	// Prepare the json patched content
	policyJSON, err := json.MarshalIndent(policy, "", "  ")
	if err != nil {
		return err
	}

	// The temporal policy is written atomically
	if err := writeFileAtomicallyWithDefaults(rpmOstreeTemporalPolicyFile, policyJSON); err != nil {
		return err
	}

	// Give systemd commands 60 seconds to finish
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	if err := r.writeTemporalOstreePolicyFileDropin(ctx); err != nil {
		return err
	}

	klog.Infof("Temporal allow policy added for URL %s", url)
	return nil
}

// generateTransportPolicyKeyForReference creates the reference string used as a key in the
// image policy file for the "containers-storage" transport.
//
// The format of this string is not officially documented and was determined by reverse-engineering
// the policy evaluation logic. It is structured as follows:
// "[<storage-driver>@<graph-root>]<repo-digest>@<image-id>"
//
// The storage driver and graph root details are retrieved from the running Podman
// instance at runtime.
func (r *RpmOstreeClient) generateTransportPolicyKeyForReference(podmanImageInfo *PodmanImageInfo) (string, error) {
	podmanInfo, err := r.podmanInterface.GetPodmanInfo()
	if err != nil {
		return "", fmt.Errorf("failed to get podman info for storage configuration gathering: %w", err)
	}
	return fmt.Sprintf("[%s@%s]%s@%s", podmanInfo.Store.GraphDriverName, podmanInfo.Store.GraphRoot, podmanImageInfo.RepoDigest, podmanImageInfo.ID), nil
}

// writeTemporalOstreePolicyFileDropin creates a systemd drop-in configuration that
// bind-mounts the temporary policy file over the actual policy file for rpm-ostreed.
// This allows rpm-ostree to use the patched policy without modifying the system's
// policy file directly. After writing the drop-in, the function reloads systemd and
// restarts rpm-ostreed to apply the changes.
func (r *RpmOstreeClient) writeTemporalOstreePolicyFileDropin(ctx context.Context) error {
	// Create a temporal dropin to mount the temporal policy into rpm-ostreed process
	if err := writeFileAtomicallyWithDefaults(
		rpmOstreeTemporalDropinFile,
		[]byte(
			fmt.Sprintf(
				"[Service]\nBindReadOnlyPaths=%s:%s", rpmOstreeTemporalPolicyFile, imagePolicyFilePath),
		),
	); err != nil {
		return err
	}
	return r.systemdRpmOstreeReload(ctx)
}

// cleanupTemporalOstreePolicyFiles removes the generated temporal files (systemd drop-in
// and temporal policy.json) created by writeTemporalOstreePolicyFileDropin, restoring
// rpm-ostreed to use the original system policy file. After removing the files, the
// function reloads systemd and restarts rpm-ostreed to apply the changes.
func (r *RpmOstreeClient) cleanupTemporalOstreePolicyFiles() error {
	// Give systemd commands 60 seconds to finish
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	err := os.Remove(rpmOstreeTemporalDropinFile)
	if err != nil && !os.IsNotExist(err) {
		return err
	} else if err == nil {
		// The file existed: Reload
		if err := r.systemdRpmOstreeReload(ctx); err != nil {
			return err
		}
	}

	// The drop-in is gone, remove the temporal file if it exists
	if err := os.Remove(rpmOstreeTemporalPolicyFile); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// systemdRpmOstreeReload notifies systemd of unit configuration changes and restarts
// the rpm-ostreed service if it's currently running.
func (r *RpmOstreeClient) systemdRpmOstreeReload(ctx context.Context) error {
	conn, err := r.systemdManager.NewConnection(ctx)
	if err != nil {
		return fmt.Errorf("error creating connection to systemd: %w", err)
	}
	defer conn.Close()

	// Tell systemd that there are changes in the units
	if err := conn.ReloadDaemon(ctx); err != nil {
		return err
	}

	// In case rpm-ostreed is running restart it to take the latest config
	return conn.TryRestart(ctx, "rpm-ostreed")
}

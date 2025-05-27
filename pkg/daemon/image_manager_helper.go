package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/opencontainers/go-digest"
	pivotutils "github.com/openshift/machine-config-operator/pkg/daemon/pivot/utils"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"
)

const (
	rpmOstreeSystem imageSystem = "rpm-ostree"
	bootcSystem     imageSystem = "bootc"
	// the number of times to retry commands that pull data from the network
	numRetriesNetCommands = 5
	// Default ostreeAuthFile location
	ostreeAuthFile = "/run/ostree/auth.json"
	// Default bootcAuthFile location
	bootcAuthFile = "/etc/ostree/auth.json"
	// Pull secret.  Written by the machine-config-operator
	kubeletAuthFile = "/var/lib/kubelet/config.json"
	// Internal Registry Pull secret + Global Pull secret.  Written by the machine-config-operator.
	internalRegistryAuthFile = "/etc/mco/internal-registry-pull-secret.json"
)

type imageSystem string

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

// BootedImageInfo stores MCO interested bootec image info
type BootedImageInfo struct {
	OSImageURL   string
	ImageVersion string
	BaseChecksum string
}

func podmanInspect(imgURL string) (imgdata *imageInspection, err error) {
	// Pull the container image if not already available
	var authArgs []string
	if _, err := os.Stat(kubeletAuthFile); err == nil {
		authArgs = append(authArgs, "--authfile", kubeletAuthFile)
	}
	args := []string{"pull", "-q"}
	args = append(args, authArgs...)
	args = append(args, imgURL)
	_, err = pivotutils.RunExt(numRetriesNetCommands, "podman", args...)
	if err != nil {
		return
	}

	inspectArgs := []string{"inspect", "--type=image"}
	inspectArgs = append(inspectArgs, fmt.Sprintf("%s", imgURL))
	var output []byte
	output, err = runGetOut("podman", inspectArgs...)
	if err != nil {
		return
	}
	var imagedataArray []imageInspection
	err = json.Unmarshal(output, &imagedataArray)
	if err != nil {
		err = fmt.Errorf("unmarshaling podman inspect: %w", err)
		return
	}
	imgdata = &imagedataArray[0]
	return

}

// runGetOut executes a command, logging it, and return the stdout output.
func runGetOut(command string, args ...string) ([]byte, error) {
	klog.Infof("Running captured: %s %s", command, strings.Join(args, " "))
	cmd := exec.Command(command, args...)
	rawOut, err := cmd.Output()
	if err != nil {
		errtext := ""
		if e, ok := err.(*exec.ExitError); ok {
			// Trim to max of 256 characters
			errtext = fmt.Sprintf("\n%s", truncate(string(e.Stderr), 256))
		}
		return nil, fmt.Errorf("error running %s %s: %s%s", command, strings.Join(args, " "), err, errtext)
	}
	return rawOut, nil
}

// truncate a string using runes/codepoints as limits.
// This specifically will avoid breaking a UTF-8 value.
func truncate(input string, limit int) string {
	asRunes := []rune(input)
	l := len(asRunes)

	if limit >= l {
		return input
	}

	return fmt.Sprintf("%s [%d more chars]", string(asRunes[:limit]), l-limit)
}

// useMergedSecrets gives the rpm-ostree / bootc client access to secrets for the internal registry and the global pull
// secret. It does this by symlinking the merged secrets file into /run/ostree or /etc/ostree. If it fails to find the
// merged secrets, it will use the default pull secret file instead.
func useMergedPullSecrets(system imageSystem) error {

	if err := validateImageSystem(system); err != nil {
		return err
	}

	// check if merged secret file exists
	if _, err := os.Stat(internalRegistryAuthFile); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		klog.Errorf("Merged secret file does not exist; defaulting to cluster pull secret")
		return linkAuthFile(system, kubeletAuthFile)
	}
	// Check that merged secret file is valid JSON
	if file, err := os.ReadFile(internalRegistryAuthFile); err != nil {
		klog.Errorf("Merged secret file could not be read; defaulting to cluster pull secret %v", err)
		return linkAuthFile(system, kubeletAuthFile)
	} else if !json.Valid(file) {
		klog.Errorf("Merged secret file could not be validated; defaulting to cluster pull secret %v", err)
		return linkAuthFile(system, kubeletAuthFile)
	}

	return linkAuthFile(system, internalRegistryAuthFile)
}

func validateImageSystem(imgSys imageSystem) error {
	// Sets provided by https://github.com/kubernetes/apimachinery/tree/master/pkg/util/sets
	// Imported as k8s.io/apimachinery/pkg/util/sets
	valid := sets.New[imageSystem](rpmOstreeSystem, bootcSystem)
	if valid.Has(imgSys) {
		return nil
	}

	return fmt.Errorf("Invalid system %s! Valid systems are: %v", imgSys, sets.List(valid))
}

// linkAuthFile gives rpm-ostree / bootc client access to secrets in the file located at `path` by symlinking so that
// rpm-ostree / bootc can use those secrets to pull images.This can be called multiple times to overwrite an older link.

// Pull secret for 'rpm-ostree' to fetch updates from registry which requires authentication is stored in /run/ostree/auth.json
// Pull secret for 'bootc' to fetch updates from registry which requires authentication is stored in /etc/ostree/auth.json
// per https://github.com/containers/bootc/blob/5e9279d6674b28d2c451baeaf981a92a1aa388ff/docs/src/building/secrets.md?plain=1#L4
func linkAuthFile(system imageSystem, path string) error {
	if err := validateImageSystem(system); err != nil {
		return err
	}

	var authFilePath string
	switch system {
	case rpmOstreeSystem:
		authFilePath = ostreeAuthFile
	case bootcSystem:
		authFilePath = bootcAuthFile
	default:
		return fmt.Errorf("unknown system value %q", system)
	}

	if _, err := os.Lstat(authFilePath); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(authFilePath), 0o544); err != nil {
			return err
		}
	} else {
		// Remove older symlink if it exists since it needs to be overwritten
		if err := os.Remove(authFilePath); err != nil {
			return err
		}
	}

	klog.Infof("Linking %s authfile to %s", system, path)
	return os.Symlink(path, authFilePath)
}

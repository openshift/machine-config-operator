package extended

import (
	"fmt"

	logger "github.com/openshift/machine-config-operator/test/extended-priv/util/logext"
	"github.com/tidwall/gjson"
)

// RemoteImage handles images located remotely in a node
type RemoteImage struct {
	Node      *Node
	ImageName string
}

// NewRemoteImage creates a new instance of RemoteFile
func NewRemoteImage(node *Node, imageName string) *RemoteImage {
	return &RemoteImage{Node: node, ImageName: imageName}
}

// String implements the stringer interface
func (ri RemoteImage) String() string {
	return fmt.Sprintf("image %s in node %s", ri.ImageName, ri.Node.GetName())
}

func (ri RemoteImage) IsPinned() (bool, error) {
	stdout, stderr, err := ri.Node.DebugNodeWithChrootStd("crictl", "images", "--pinned", "-o", "json", ri.ImageName)
	if err != nil {
		logger.Errorf("Error getting pinned imaformation for %s:\nstdout:%s\nstderr:%s", ri, stdout, stderr)
		return false, err
	}

	logger.Debugf("%s:%s", ri, stdout)

	pinned := gjson.Get(stdout, "images.0.pinned")
	if !pinned.Exists() {
		logger.Infof("%s:%s", ri, stdout)
		return false, fmt.Errorf("Could not get pinned information for %s", ri)
	}

	return pinned.Bool(), nil
}

// Rmi executes the rmi command to delete the image
func (ri RemoteImage) Rmi(args ...string) error {
	logger.Infof("Removing image %s from node %s", ri.ImageName, ri.Node.GetName())
	cmd := []string{"crictl", "rmi"}

	if len(args) > 0 {
		cmd = append(cmd, args...)
	}

	cmd = append(cmd, ri.ImageName)
	output, err := ri.Node.DebugNodeWithChroot(cmd...)
	logger.Infof(output)

	return err
}

// Pull pull the image in the node
func (ri RemoteImage) Pull(args ...string) error {
	logger.Infof("Puilling image %s in node %s", ri.ImageName, ri.Node.GetName())
	cmd := []string{"crictl", "pull"}

	if len(args) > 0 {
		cmd = append(cmd, args...)
	}

	cmd = append(cmd, ri.ImageName)
	output, err := ri.Node.DebugNodeWithChroot(cmd...)
	logger.Infof(output)

	return err
}

// Exists return true if the image is present in the node (it has been pulled)
func (ri RemoteImage) Exists() bool {
	logger.Infof("Checking if %s exists", ri)

	stdout, stderr, err := ri.Node.DebugNodeWithChrootStd("crictl", "images", "--pinned", "-o", "json", ri.ImageName)
	if err != nil {
		logger.Errorf("Error getting pinned imaformation for %s:\nstdout:%s\nstderr:%s", ri, stdout, stderr)
		return false
	}

	logger.Debugf("%s:%s", ri, stdout)

	pinned := gjson.Get(stdout, "images.0")

	logger.Infof("%t", pinned.Exists())

	return pinned.Exists()
}

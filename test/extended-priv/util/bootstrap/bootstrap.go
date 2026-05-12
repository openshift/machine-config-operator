package bootstrap

import (
	"fmt"

	util "github.com/openshift/machine-config-operator/test/extended-priv/util"
	logger "github.com/openshift/machine-config-operator/test/extended-priv/util/logext"
)

const (
	// EnvVarSSHCloudPrivKey stores the environment variable for the ssh private key path
	EnvVarSSHCloudPrivKey = "SSH_CLOUD_PRIV_KEY"
	// DefaultSSHUser is the default ssh user for bootstrap machines
	DefaultSSHUser = "core"
)

// InstanceNotFound reports an error because the bootstrap instance is not found. It can be used to skip the test case.
type InstanceNotFound struct{ InstanceName string }

// Error implements the error interface
func (inferr *InstanceNotFound) Error() string {
	return fmt.Sprintf("Instance %s has 'terminated' status", inferr.InstanceName)
}

// BSInfoProvider any struct implementing this interface can be used to create a Bootstrap object.
type BSInfoProvider interface {
	GetIPs(*util.CLI) (*Ips, error)
	GetSSHUser() string
}

// Bootstrap contains the functionality regarding the bootstrap machine
type Bootstrap struct {
	SSH util.SshClient
	IPs Ips
}

// Ips struct to store the public and the private IPs of the bootstrap machine
type Ips struct {
	PrivateIPAddress string
	PublicIPAddress  string
}

// GetBootstrap returns a bootstrap struct pointing to the bootstrap machine if it exists
func GetBootstrap(oc *util.CLI) (*Bootstrap, error) {
	bsInfoProvider, err := GetBSInfoProvider(oc)
	if err != nil {
		return nil, err
	}

	bootstrapIPs, err := bsInfoProvider.GetIPs(oc.AsAdmin())
	if err != nil {
		return nil, err
	}

	user := bsInfoProvider.GetSSHUser()
	return buildBootstrap(user, *bootstrapIPs, 22), nil
}

// GetBSInfoProvider returns a struct implementing BSInfoProvider for the current platform
func GetBSInfoProvider(oc *util.CLI) (BSInfoProvider, error) {
	platform := util.CheckPlatform(oc)
	switch platform {
	case "aws":
		return AWSBSInfoProvider{}, nil
	case "azure":
		return AzureBSInfoProvider{}, nil
	default:
		return nil, fmt.Errorf("platform not supported. Cannot get bootstrap information for platform: %s", platform)
	}
}

func buildBootstrap(user string, bootstrapIPs Ips, port int) *Bootstrap {
	privateKey := util.GetSSHPrivateKey()
	publicIP := bootstrapIPs.PublicIPAddress
	logger.Infof("Creating bootstrap with ip '%s', user: '%s', private key: '%s', port '%d'",
		publicIP, user, privateKey, port)
	return &Bootstrap{
		SSH: util.SshClient{User: user, Host: publicIP, Port: port, PrivateKey: privateKey},
		IPs: bootstrapIPs,
	}
}

package bootstrap

import (
	util "github.com/openshift/machine-config-operator/test/extended-priv/util"
	logger "github.com/openshift/machine-config-operator/test/extended-priv/util/logext"
)

const (
	// EnvVarSSHCloudPrivAzureUser stores the environment variable for the Azure ssh user
	EnvVarSSHCloudPrivAzureUser = "SSH_CLOUD_PRIV_AZURE_USER"
)

// AzureBSInfoProvider implements interface BSInfoProvider for Azure
type AzureBSInfoProvider struct{}

// GetIPs returns the IPs of the bootstrap machine if this machine exists in Azure.
// Azure bootstrap discovery is not yet implemented; returns InstanceNotFound so the test skips.
func (a AzureBSInfoProvider) GetIPs(oc *util.CLI) (*Ips, error) {
	infraName, err := oc.WithoutNamespace().AsAdmin().Run("get").Args("infrastructure", "cluster", "-o=jsonpath={.status.infrastructureName}").Output()
	if err != nil {
		return nil, err
	}
	bootstrapName := infraName + "-bootstrap"
	logger.Infof("Azure bootstrap discovery is not yet implemented, skipping bootstrap machine: %s", bootstrapName)
	return nil, &InstanceNotFound{bootstrapName}
}

// GetSSHUser returns the user needed to connect to the bootstrap machine via ssh
func (a AzureBSInfoProvider) GetSSHUser() string {
	return DefaultSSHUser
}

package bootstrap

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	util "github.com/openshift/machine-config-operator/test/extended-priv/util"
	logger "github.com/openshift/machine-config-operator/test/extended-priv/util/logext"
)

const (
	// EnvVarSSHCloudPrivAWSUser stores the environment variable for the AWS ssh user
	EnvVarSSHCloudPrivAWSUser = "SSH_CLOUD_PRIV_AWS_USER"
)

// AWSBSInfoProvider implements interface BSInfoProvider for AWS
type AWSBSInfoProvider struct{}

// GetIPs returns the IPs of the bootstrap machine if this machine exists in AWS
func (a AWSBSInfoProvider) GetIPs(oc *util.CLI) (*Ips, error) {
	infraName, err := oc.WithoutNamespace().AsAdmin().Run("get").Args("infrastructure", "cluster", "-o=jsonpath={.status.infrastructureName}").Output()
	if err != nil {
		logger.Errorf("Could not get bootstrap's IP in AWS. Unable to get infrastructure's name. Error: %s", err)
		return nil, err
	}

	bootstrapName := infraName + "-bootstrap"

	publicIP, err := awsCLIGetInstancePublicIP(bootstrapName)
	if err != nil {
		return nil, &InstanceNotFound{bootstrapName}
	}
	if publicIP == "" || publicIP == "None" {
		logger.Infof("Bootstrap instance '%s' has no public IP or is terminated", bootstrapName)
		return nil, &InstanceNotFound{bootstrapName}
	}

	privateIP, _ := awsCLIGetInstancePrivateIP(bootstrapName)
	logger.Infof("Bootstrap instance '%s': publicIP=%s, privateIP=%s", bootstrapName, publicIP, privateIP)
	return &Ips{PublicIPAddress: publicIP, PrivateIPAddress: privateIP}, nil
}

// GetSSHUser returns the user needed to connect to the bootstrap machine via ssh
func (a AWSBSInfoProvider) GetSSHUser() string {
	if user, exists := os.LookupEnv(EnvVarSSHCloudPrivAWSUser); exists {
		return user
	}
	return DefaultSSHUser
}

func awsCLIGetInstancePublicIP(instanceName string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "aws", "ec2", "describe-instances",
		"--filters", fmt.Sprintf("Name=tag:Name,Values=%s", instanceName),
		"--query", "Reservations[0].Instances[0].PublicIpAddress",
		"--output", "text",
	).Output()
	if err != nil {
		return "", err
	}
	result := strings.TrimSpace(string(out))
	if result == "null" {
		return "", fmt.Errorf("no instance found with name %s", instanceName)
	}
	return result, nil
}

func awsCLIGetInstancePrivateIP(instanceName string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "aws", "ec2", "describe-instances",
		"--filters", fmt.Sprintf("Name=tag:Name,Values=%s", instanceName),
		"--query", "Reservations[0].Instances[0].PrivateIpAddress",
		"--output", "text",
	).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

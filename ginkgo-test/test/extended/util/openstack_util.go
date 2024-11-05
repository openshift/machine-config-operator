package util

import (
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"strings"

	"github.com/gophercloud/gophercloud"
	"github.com/gophercloud/gophercloud/openstack"
	tokens3 "github.com/gophercloud/gophercloud/openstack/identity/v3/tokens"
	"github.com/gophercloud/gophercloud/openstack/identity/v3/users"
	"github.com/gophercloud/gophercloud/openstack/objectstorage/v1/containers"
	"github.com/gophercloud/gophercloud/openstack/objectstorage/v1/objects"
	"github.com/gophercloud/gophercloud/pagination"
	o "github.com/onsi/gomega"
	yamlv3 "gopkg.in/yaml.v3"
	e2e "k8s.io/kubernetes/test/e2e/framework"
)

// A Osp represents object ...
type Osp struct {
}

// GetOspInstance represents to list osp instance ...
func (osp *Osp) GetOspInstance(instanceName string) (string, error) {
	cmd := fmt.Sprintf("openstack --os-auth-url %s --os-password %s --os-project-id %s --os-username %s --os-domain-name %s server list --name %s -c Name -f value", os.Getenv("OSP_DR_AUTH_URL"), os.Getenv("OSP_DR_PASSWORD"), os.Getenv("OSP_DR_PROJECT_ID"), os.Getenv("OSP_DR_USERNAME"), os.Getenv("OSP_DR_USER_DOMAIN_NAME"), instanceName)
	instance, err := exec.Command("bash", "-c", cmd).Output()
	o.Expect(err).NotTo(o.HaveOccurred())
	if string(instance) == "" {
		return "", fmt.Errorf("VM is not found")
	}
	e2e.Logf("Virtual machines instance found:", strings.Trim(string(instance), "\n"))
	return strings.Trim(string(instance), "\n"), err
}

// GetOspInstanceState represents to list osp instance state ...
func (osp *Osp) GetOspInstanceState(instanceName string) (string, error) {
	cmd := fmt.Sprintf("openstack --os-auth-url %s --os-password %s --os-project-id %s --os-username %s --os-domain-name %s server list --name %s -c Status -f value", os.Getenv("OSP_DR_AUTH_URL"), os.Getenv("OSP_DR_PASSWORD"), os.Getenv("OSP_DR_PROJECT_ID"), os.Getenv("OSP_DR_USERNAME"), os.Getenv("OSP_DR_USER_DOMAIN_NAME"), instanceName)
	instanceState, err := exec.Command("bash", "-c", cmd).Output()
	o.Expect(err).NotTo(o.HaveOccurred())
	if string(instanceState) == "" {
		return "", fmt.Errorf("Not able to get vm instance state")
	}
	return strings.Trim(string(instanceState), "\n"), err
}

// GetStopOspInstance represents to stop osp instance ...
func (osp *Osp) GetStopOspInstance(instanceName string) error {
	cmd := fmt.Sprintf("openstack --os-auth-url %s --os-password %s --os-project-id %s --os-username %s --os-domain-name %s server stop %s", os.Getenv("OSP_DR_AUTH_URL"), os.Getenv("OSP_DR_PASSWORD"), os.Getenv("OSP_DR_PROJECT_ID"), os.Getenv("OSP_DR_USERNAME"), os.Getenv("OSP_DR_USER_DOMAIN_NAME"), instanceName)
	_, err := exec.Command("bash", "-c", cmd).Output()
	e2e.Logf("When trying to stop openstack instance, got error:", err)
	if err != nil {
		return fmt.Errorf("Not able to stop VM")
	}
	return err
}

// GetStartOspInstance represents to start osp instance ...
func (osp *Osp) GetStartOspInstance(instanceName string) error {
	cmd := fmt.Sprintf("openstack --os-auth-url %s --os-password %s --os-project-id %s --os-username %s --os-domain-name %s server start %s", os.Getenv("OSP_DR_AUTH_URL"), os.Getenv("OSP_DR_PASSWORD"), os.Getenv("OSP_DR_PROJECT_ID"), os.Getenv("OSP_DR_USERNAME"), os.Getenv("OSP_DR_USER_DOMAIN_NAME"), instanceName)
	_, err := exec.Command("bash", "-c", cmd).Output()
	if err != nil {
		return fmt.Errorf("Not able to start VM")
	}
	return err
}

// OpenstackCredentials the openstack credentials extracted from cluster
type OpenstackCredentials struct {
	Clouds struct {
		Openstack struct {
			Auth struct {
				AuthURL        string `yaml:"auth_url"`
				Password       string `yaml:"password"`
				ProjectID      string `yaml:"project_id"`
				ProjectName    string `yaml:"project_name"`
				UserDomainName string `yaml:"user_domain_name"`
				Username       string `yaml:"username"`
			} `yaml:"auth"`
			EndpointType       string `yaml:"endpoint_type"`
			IdentityAPIVersion string `yaml:"identity_api_version"`
			RegionName         string `yaml:"region_name"`
			Verify             bool   `yaml:"verify"`
		} `yaml:"openstack"`
	} `yaml:"clouds"`
}

// GetOpenStackCredentials gets credentials from cluster
func GetOpenStackCredentials(oc *CLI) (*OpenstackCredentials, error) {
	cred := &OpenstackCredentials{}
	dirname := "/tmp/" + oc.Namespace() + "-creds"
	defer os.RemoveAll(dirname)
	err := os.MkdirAll(dirname, 0777)
	o.Expect(err).NotTo(o.HaveOccurred())

	_, err = oc.AsAdmin().WithoutNamespace().Run("extract").Args("secret/openstack-credentials", "-n", "kube-system", "--confirm", "--to="+dirname).Output()
	if err != nil {
		return cred, err
	}

	confFile, err := ioutil.ReadFile(dirname + "/clouds.yaml")
	if err == nil {
		err = yamlv3.Unmarshal(confFile, cred)
	}
	return cred, err
}

// NewOpenStackClient initializes an openstack client
// serviceType the type of the client, currently only supports indentity and object-store
func NewOpenStackClient(cred *OpenstackCredentials, serviceType string) *gophercloud.ServiceClient {
	var client *gophercloud.ServiceClient
	opts := gophercloud.AuthOptions{
		IdentityEndpoint: cred.Clouds.Openstack.Auth.AuthURL,
		Username:         cred.Clouds.Openstack.Auth.Username,
		Password:         cred.Clouds.Openstack.Auth.Password,
		TenantID:         cred.Clouds.Openstack.Auth.ProjectID,
		DomainName:       cred.Clouds.Openstack.Auth.UserDomainName,
	}
	provider, err := openstack.AuthenticatedClient(opts)
	o.Expect(err).NotTo(o.HaveOccurred())
	switch serviceType {
	case "identity":
		{
			client, err = openstack.NewIdentityV3(provider, gophercloud.EndpointOpts{Region: cred.Clouds.Openstack.RegionName})
		}
	case "object-store":
		{
			client, err = openstack.NewObjectStorageV1(provider, gophercloud.EndpointOpts{Region: cred.Clouds.Openstack.RegionName})
		}
	}
	o.Expect(err).NotTo(o.HaveOccurred())
	return client
}

// GetAuthenticatedUserID gets current user ID
// some users don't have permission to list users, so here extract user ID from auth response
func GetAuthenticatedUserID(providerClient *gophercloud.ProviderClient) (string, error) {
	//copied from https://github.com/gophercloud/gophercloud/blob/master/auth_result.go
	res := providerClient.GetAuthResult()
	if res == nil {
		//ProviderClient did not use openstack.Authenticate(), e.g. because token
		//was set manually with ProviderClient.SetToken()
		return "", fmt.Errorf("no AuthResult available")
	}
	switch r := res.(type) {
	case tokens3.CreateResult:
		u, err := r.ExtractUser()
		if err != nil {
			return "", err
		}
		return u.ID, nil
	default:
		return "", fmt.Errorf("got unexpected AuthResult type %t", r)
	}
}

// GetOpenStackUserIDAndDomainID returns the user ID and domain ID
func GetOpenStackUserIDAndDomainID(cred *OpenstackCredentials) (string, string) {
	client := NewOpenStackClient(cred, "identity")
	userID, err := GetAuthenticatedUserID(client.ProviderClient)
	o.Expect(err).NotTo(o.HaveOccurred())
	user, err := users.Get(client, userID).Extract()
	o.Expect(err).NotTo(o.HaveOccurred())
	return userID, user.DomainID
}

// CreateOpenStackContainer creates a storage container in openstack
func CreateOpenStackContainer(client *gophercloud.ServiceClient, name string) error {
	pager := containers.List(client, &containers.ListOpts{Full: true, Prefix: name})
	exist := false
	// check if the container exists or not
	err := pager.EachPage(func(page pagination.Page) (bool, error) {
		containerNames, err := containers.ExtractNames(page)
		o.Expect(err).NotTo(o.HaveOccurred())
		for _, n := range containerNames {
			if n == name {
				exist = true
				break
			}
		}
		return true, nil
	})
	if err != nil {
		return err
	}
	if exist {
		err = EmptyOpenStackContainer(client, name)
		o.Expect(err).NotTo(o.HaveOccurred())
	}
	// create the container
	res := containers.Create(client, name, containers.CreateOpts{})
	_, err = res.Extract()
	return err
}

// DeleteOpenStackContainer deletes the storage container from openstack
func DeleteOpenStackContainer(client *gophercloud.ServiceClient, name string) error {
	err := EmptyOpenStackContainer(client, name)
	if err != nil {
		return err
	}
	response := containers.Delete(client, name)
	_, err = response.Extract()
	if err != nil {
		return fmt.Errorf("error deleting container %s: %v", name, err)
	}
	e2e.Logf("Container %s is deleted", name)
	return nil
}

// EmptyOpenStackContainer clear all the objects in storage container
func EmptyOpenStackContainer(client *gophercloud.ServiceClient, name string) error {
	pager := objects.List(client, name, &objects.ListOpts{Full: true})
	err := pager.EachPage(func(page pagination.Page) (bool, error) {
		objectNames, err := objects.ExtractNames(page)
		if err != nil {
			return false, fmt.Errorf("error getting object names: %v", err)
		}
		for _, obj := range objectNames {
			result := objects.Delete(client, name, obj, objects.DeleteOpts{})
			_, err := result.Extract()
			if err != nil {
				return false, fmt.Errorf("hit error when deleting object %s: %v", obj, err)
			}
		}
		return true, nil
	})
	if err != nil {
		return fmt.Errorf("error deleting objects in container %s: %v", name, err)
	}
	e2e.Logf("deleted all object items in the container %s", name)
	return nil
}

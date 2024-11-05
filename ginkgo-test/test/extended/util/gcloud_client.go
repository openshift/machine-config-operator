package util

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"cloud.google.com/go/storage"
	o "github.com/onsi/gomega"
	"google.golang.org/api/iterator"
	e2e "k8s.io/kubernetes/test/e2e/framework"
)

// Gcloud struct
type Gcloud struct {
	ProjectID string
}

// Login logins to the gcloud. This function needs to be used only once to login into the GCP.
// the gcloud client is only used for the cluster which is on gcp platform.
func (gcloud *Gcloud) Login() *Gcloud {
	checkCred, err := exec.Command("bash", "-c", `gcloud auth list --format="value(account)"`).Output()
	o.Expect(err).NotTo(o.HaveOccurred())
	if string(checkCred) != "" {
		return gcloud
	}
	credErr := exec.Command("bash", "-c", "gcloud auth login --cred-file=$GOOGLE_APPLICATION_CREDENTIALS").Run()
	o.Expect(credErr).NotTo(o.HaveOccurred())
	projectErr := exec.Command("bash", "-c", fmt.Sprintf("gcloud config set project %s", gcloud.ProjectID)).Run()
	o.Expect(projectErr).NotTo(o.HaveOccurred())
	return gcloud
}

// GetIntSvcExternalIP returns the int svc external IP
func (gcloud *Gcloud) GetIntSvcExternalIP(infraID string) (string, error) {
	externalIP, err := exec.Command("bash", "-c", fmt.Sprintf(`gcloud compute instances list --filter="%s-int-svc"  --format="value(EXTERNAL_IP)"`, infraID)).Output()
	if string(externalIP) == "" {
		return "", errors.New("additional VM is not found")
	}
	return strings.Trim(string(externalIP), "\n"), err
}

// GetIntSvcInternalIP returns the int svc internal IP
func (gcloud *Gcloud) GetIntSvcInternalIP(infraID string) (string, error) {
	internalIP, err := exec.Command("bash", "-c", fmt.Sprintf(`gcloud compute instances list --filter="%s-int-svc"  --format="value(networkInterfaces.networkIP)"`, infraID)).Output()
	if string(internalIP) == "" {
		return "", errors.New("additional VM is not found")
	}
	return strings.Trim(string(internalIP), "\n"), err
}

// GetFirewallAllowPorts returns firewall allow ports
func (gcloud *Gcloud) GetFirewallAllowPorts(ruleName string) (string, error) {
	ports, err := exec.Command("bash", "-c", fmt.Sprintf(`gcloud compute firewall-rules list --filter="name=(%s)" --format="value(ALLOW)"`, ruleName)).Output()
	return strings.Trim(string(ports), "\n"), err
}

// UpdateFirewallAllowPorts updates the firewall allow ports
func (gcloud *Gcloud) UpdateFirewallAllowPorts(ruleName string, ports string) error {
	return exec.Command("bash", "-c", fmt.Sprintf(`gcloud compute firewall-rules update %s --allow %s`, ruleName, ports)).Run()
}

// GetZone get zone information for an instance
func (gcloud *Gcloud) GetZone(infraID string, workerName string) (string, error) {
	output, err := exec.Command("bash", "-c", fmt.Sprintf(`gcloud compute instances list --filter="%s" --format="value(ZONE)"`, workerName)).Output()
	if string(output) == "" {
		return "", errors.New("Zone info for the instance is not found")
	}
	return string(output), err
}

// StartInstance Bring GCP node/instance back up
func (gcloud *Gcloud) StartInstance(nodeName string, zoneName string) error {
	return exec.Command("bash", "-c", fmt.Sprintf(`gcloud compute instances start %s --zone=%s`, nodeName, zoneName)).Run()
}

// StopInstance Shutdown GCP node/instance
func (gcloud *Gcloud) StopInstance(nodeName string, zoneName string) error {
	return exec.Command("bash", "-c", fmt.Sprintf(`gcloud compute instances stop %s --zone=%s`, nodeName, zoneName)).Run()
}

// GetGcpInstanceByNode returns the instance name
func (gcloud *Gcloud) GetGcpInstanceByNode(nodeIdentity string) (string, error) {
	instanceID, err := exec.Command("bash", "-c", fmt.Sprintf(`gcloud compute instances list --filter="%s" --format="value(name)"`, nodeIdentity)).Output()
	if string(instanceID) == "" {
		return "", fmt.Errorf("VM is not found")
	}
	return strings.Trim(string(instanceID), "\n"), err
}

// GetGcpInstanceStateByNode returns the instance state
func (gcloud *Gcloud) GetGcpInstanceStateByNode(nodeIdentity string) (string, error) {
	instanceState, err := exec.Command("bash", "-c", fmt.Sprintf(`gcloud compute instances list --filter="%s" --format="value(status)"`, nodeIdentity)).Output()
	if string(instanceState) == "" {
		return "", fmt.Errorf("Not able to get instance state")
	}
	return strings.Trim(string(instanceState), "\n"), err
}

// StopInstanceAsync Shutdown GCP node/instance with async
func (gcloud *Gcloud) StopInstanceAsync(nodeName string, zoneName string) error {
	return exec.Command("bash", "-c", fmt.Sprintf(`gcloud compute instances stop %s --async --zone=%s`, nodeName, zoneName)).Run()
}

// CreateGCSBucket creates a GCS bucket in a project
func CreateGCSBucket(projectID, bucketName string) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// initialize the GCS client, the credentials are got from the env var GOOGLE_APPLICATION_CREDENTIALS
	client, err := storage.NewClient(ctx)
	if err != nil {
		return fmt.Errorf("storage.NewClient: %v", err)
	}
	defer client.Close()

	// check if the bucket exists or not
	// if exists, clear all the objects in the bucket
	// if not, create the bucket
	exist := false
	buckets, err := ListGCSBuckets(*client, projectID)
	if err != nil {
		return err
	}
	for _, bu := range buckets {
		if bu == bucketName {
			exist = true
			break
		}
	}
	if exist {
		return EmptyGCSBucket(*client, bucketName)
	}

	bucket := client.Bucket(bucketName)
	if err := bucket.Create(ctx, projectID, &storage.BucketAttrs{}); err != nil {
		return fmt.Errorf("Bucket(%q).Create: %v", bucketName, err)
	}
	fmt.Printf("Created bucket %v\n", bucketName)
	return nil
}

// ListGCSBuckets gets all the bucket names under the projectID
func ListGCSBuckets(client storage.Client, projectID string) ([]string, error) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var buckets []string
	it := client.Buckets(ctx, projectID)
	for {
		battrs, err := it.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, err
		}
		buckets = append(buckets, battrs.Name)
	}
	return buckets, nil
}

// EmptyGCSBucket removes all the objects in the bucket
func EmptyGCSBucket(client storage.Client, bucketName string) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	bucket := client.Bucket(bucketName)
	it := bucket.Objects(ctx, nil)
	for {
		objAttrs, err := it.Next()
		if err != nil && err != iterator.Done {
			return fmt.Errorf("can't get objects in bucket %s: %v", bucketName, err)
		}
		if err == iterator.Done {
			break
		}
		if err := bucket.Object(objAttrs.Name).Delete(ctx); err != nil {
			return fmt.Errorf("Object(%q).Delete: %v", objAttrs.Name, err)
		}
	}
	e2e.Logf("deleted all object items in the bucket %s.", bucketName)
	return nil
}

// DeleteGCSBucket deletes the GCS bucket
func DeleteGCSBucket(bucketName string) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client, err := storage.NewClient(ctx)
	if err != nil {
		return fmt.Errorf("storage.NewClient: %v", err)
	}
	defer client.Close()

	// remove objects
	err = EmptyGCSBucket(*client, bucketName)
	if err != nil {
		return err
	}
	bucket := client.Bucket(bucketName)
	if err := bucket.Delete(ctx); err != nil {
		return fmt.Errorf("Bucket(%q).Delete: %v", bucketName, err)
	}
	e2e.Logf("Bucket %v is deleted\n", bucketName)
	return nil
}

// GetFilestoreInstanceInfo returns filestore instance detailed info from banckend
func (gcloud *Gcloud) GetFilestoreInstanceInfo(pvName string, filterArgs ...string) ([]byte, error) {
	filestoreInfo, err := exec.Command("bash", "-c", fmt.Sprintf(`gcloud filestore instances describe %s %s --format=json`, pvName, strings.Join(filterArgs, " "))).Output()
	if len(filestoreInfo) == 0 {
		return filestoreInfo, errors.New("gcloud filestore instance not found")
	}
	return filestoreInfo, err
}

// GetPdVolumeInfo returns pd volume detailed info from backend
func (gcloud *Gcloud) GetPdVolumeInfo(pvName string, filterArgs ...string) ([]byte, error) {
	pdVolumeInfo, err := exec.Command("bash", "-c", fmt.Sprintf(`gcloud compute disks describe %s %s --format=json`, pvName, strings.Join(filterArgs, " "))).Output()
	if len(pdVolumeInfo) == 0 {
		err = fmt.Errorf(`Couldn't find the pd volume "%s" info`, pvName)
	}
	return pdVolumeInfo, err
}

func (gcloud *Gcloud) GetResourceTags(bucketName string, zone string) ([]byte, error) {
	ResourceTags, err := exec.Command("bash", "-c", fmt.Sprintf(`gcloud resource-manager tags bindings list --parent=//storage.googleapis.com/projects/_/buckets/%s --location=%s`, bucketName, zone)).Output()
	if len(ResourceTags) == 0 {
		err = fmt.Errorf("Couldn't find resourcetags")
	}
	return ResourceTags, err
}

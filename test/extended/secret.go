package extended

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	o "github.com/onsi/gomega"
	exutil "github.com/openshift/machine-config-operator/test/extended/util"
	logger "github.com/openshift/machine-config-operator/test/extended/util/logext"
	"k8s.io/apimachinery/pkg/util/wait"
	e2e "k8s.io/kubernetes/test/e2e/framework"
)

// Secret struct encapsulates the functionalities regarding ocp secrets
type Secret struct {
	Resource
}

// SecretList handles list of secrets
type SecretList struct {
	ResourceList
}

// NewSecret creates a Secret struct
func NewSecret(oc *exutil.CLI, namespace, name string) *Secret {
	return &Secret{Resource: *NewNamespacedResource(oc, "secret", namespace, name)}
}

// NewSecretList creates a new  SecretList struct
func NewSecretList(oc *exutil.CLI, namespace string) *SecretList {
	return &SecretList{*NewNamespacedResourceList(oc, "secret", namespace)}
}

// GetAll returns a []Secret list with all existing secrets
func (sl *SecretList) GetAll() ([]Secret, error) {
	allSecretResources, err := sl.ResourceList.GetAll()
	if err != nil {
		return nil, err
	}
	allSecrets := make([]Secret, 0, len(allSecretResources))

	for _, secretRes := range allSecretResources {
		allSecrets = append(allSecrets, *NewSecret(sl.oc, sl.GetNamespace(), secretRes.name))
	}

	return allSecrets, nil
}

// ExtractToDir extracts the secret's content to a given directory
func (s Secret) ExtractToDir(directory string) error {
	err := s.oc.WithoutNamespace().Run("extract").Args(s.GetKind()+"/"+s.GetName(), "-n", s.GetNamespace(), "--to", directory).Execute()
	if err != nil {
		return err
	}

	return nil
}

// Extract extracts the secret's content to a random directory in the testcase's output directory
func (s Secret) Extract() (string, error) {
	layout := "2006_01_02T15-04-05Z.000"

	directory := filepath.Join(e2e.TestContext.OutputDir, fmt.Sprintf("%s-%s-secret-%s", s.GetNamespace(), s.GetName(), time.Now().Format(layout)))
	os.MkdirAll(directory, os.ModePerm)
	return directory, s.ExtractToDir(directory)
}

// GetDataValue gets the value stored in the secret's key
func (s Secret) GetDataValue(key string) (string, error) {
	templateArg := fmt.Sprintf(`--template={{index .data "%s" | base64decode}}`, key)
	return s.oc.AsAdmin().WithoutNamespace().Run("get").Args(s.GetKind(), s.GetName(), "-n", s.GetNamespace(), templateArg).Output()
}

// GetDecodedDataMap returns the valus in the .data field as a map[string][string] with the values decoded
func (s Secret) GetDecodedDataMap() (map[string]string, error) {
	data := map[string]string{}
	dataJSON, err := s.Get(`{.data}`)
	if err != nil {
		return nil, err
	}

	if err := json.Unmarshal([]byte(dataJSON), &data); err != nil {
		return nil, err
	}

	for k, vb64 := range data {
		v, err := base64.StdEncoding.DecodeString(vb64)
		if err != nil {
			logger.Errorf("The certiifcate provided in the kubeconfig is not base64 encoded")
			return nil, err
		}
		// Replace the original encoded value with the decoded value
		data[k] = string(v)
	}

	return data, nil
}

// GetDataValueOrFail gets the value stored in the secret's key and fail the test if there is any error
func (s Secret) GetDataValueOrFail(key string) string {
	data, err := s.GetDataValue(key)
	o.ExpectWithOffset(1, err).NotTo(o.HaveOccurred(),
		"Cannot get the value of %s from secret %s -n %s",
		key, s.GetName(), s.GetNamespace())
	return data
}

// SetDataValue sets a key/value to store in the secret
func (s Secret) SetDataValue(key, value string) error {
	// silently set the value so that we don't print the secret in the logs leaking sensible information
	s.oc.NotShowInfo()
	defer s.oc.SetShowInfo()
	logger.Debugf("Secret %s -n %s. Setting value: %s=%s", s.GetName(), s.GetNamespace(), key, value)
	// command example: oc secret pull-secret -n openshift-config set data .dockerconfigjson={}
	return s.oc.AsAdmin().WithoutNamespace().Run("set").Args("data", s.GetKind(), s.GetName(),
		"-n", s.GetNamespace(),
		fmt.Sprintf("%s=%s", key, value)).Execute()
}

// GetPullSecret returns the cluster's pull secret
func GetPullSecret(oc *exutil.CLI) *Secret {
	return NewSecret(oc.AsAdmin(), "openshift-config", "pull-secret")
}

// waitUntilSecretHasStableValue polls a data in a secret and returns its value once it has been retrieved "numTries" times with the same value
func waitUntilSecretHasStableValue(secret *Secret, data string, timeout, poll time.Duration, numTries int) (string, error) {
	var (
		count  = 0
		oldVal string
	)

	immediate := true
	waitErr := wait.PollUntilContextTimeout(context.TODO(), poll, timeout, immediate,
		func(_ context.Context) (bool, error) {
			val, err := secret.GetDataValue(data)

			if val == oldVal && err == nil {
				count++
			} else {
				count = 0
				oldVal = val
			}

			if count == numTries {
				return true, nil
			}
			return false, nil

		})

	if waitErr == nil {
		return oldVal, nil
	}

	return "", waitErr
}

// rotateCertificateInSecret rotates the certificates in a Secret. We only return "tls.crt" since currently there is no use case for "tls.key"
func rotateTLSSecretOrFail(secret *Secret) string {

	logger.Infof("Rotating TLS certificate in %s", secret)
	initialCert := secret.GetDataValueOrFail("tls.crt")
	logger.Debugf("Current certificate: %s", initialCert)

	logger.Infof("Patch certificate")
	o.Expect(
		secret.Patch("merge", `{"metadata": {"annotations": {"auth.openshift.io/certificate-not-after": null}}}`),
	).To(o.Succeed(),
		"The secret %s could not be patched in order to rotate the certificate", secret)

	logger.Infof("Wait for certificate rotation")
	o.Eventually(secret.GetDataValueOrFail, "3m", "20s").WithArguments("tls.crt").
		ShouldNot(exutil.Secure(o.Equal(initialCert)),
			"The certificate was not rotated in %s", secret)

	logger.Infof("Wait for the new certificate to be stable (avoid double rotations: OCPQE-20323)")
	newCert, err := waitUntilSecretHasStableValue(secret, "tls.crt", 5*time.Minute, 5*time.Second, 3)
	o.Expect(err).NotTo(o.HaveOccurred(),
		"We cannot get a new stable certificate after the certificate rotation in %s", secret)

	return newCert
}

// verifyMcsCASecretRotateOrFail helps to edit the annotation MCS-CA secret and verify the cert update in *-user-data-* secret and mcs-ca config map.
func verifyMcsCASecretRotateOrFail(mcsCAsecret *Secret, secret ...*Secret) {
	var (
		oc = mcsCAsecret.GetOC()
		cm = NewConfigMap(oc.AsAdmin(), MachineConfigNamespace, "machine-config-server-ca")
	)

	logger.Infof("Get the initial machine-config-server-ca config map ")
	initialCMCert := cm.GetDataValueOrFail("ca-bundle.crt")
	logger.Debugf("Current certificate: %s", initialCMCert)

	logger.Infof("Edit the annotation in %s secret", mcsCAsecret)
	o.Expect(
		mcsCAsecret.Patch("merge", `{"metadata": {"annotations": {"auth.openshift.io/certificate-not-after": null}}}`),
	).To(o.Succeed(),
		"The secret %s could not be patched in order to rotate the certificate", mcsCAsecret)

	logger.Infof("Verify the config map is updated by comparing with itial CM certs")
	o.Eventually(cm.GetDataValueOrFail, "3m", "20s").WithArguments("ca-bundle.crt").
		ShouldNot(exutil.Secure(o.Equal(initialCMCert)),
			"The certificate was not updated in %s", cm)
	logger.Infof("MCS-CA Config Map is updated! ")

	updatedCMCert := cm.GetDataValueOrFail("ca-bundle.crt") // get the latest cert of CM
	base64EncodedCert := base64.StdEncoding.EncodeToString([]byte(updatedCMCert))

	logger.Infof("Verify the config map is updated by comparing it *-userdata-* secret in openshift-machine-api")
	for _, userDataSecret := range secret {
		o.Eventually(userDataSecret.GetDataValueOrFail, "3m", "20s").WithArguments("userData").
			Should(exutil.Secure(o.ContainSubstring(base64EncodedCert)),
				"MCS-CA Config Map cert is not present in %s secret", userDataSecret)
		logger.Infof("MCS-CA Config Map cert is present in %s secret !\n", userDataSecret)
	}
}

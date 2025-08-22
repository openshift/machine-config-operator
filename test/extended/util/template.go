package util

import (
	"context"
	"crypto/rand"
	"fmt"
	"math/big"
	"time"

	o "github.com/onsi/gomega"

	"k8s.io/apimachinery/pkg/util/wait"
	e2e "k8s.io/kubernetes/test/e2e/framework"
)

// CreateClusterResourceFromTemplateWithError create resource from the template and return error if happened.
// For ex: CreateClusterResourceFromTemplateWithError(oc, "--ignore-unknown-parameters=true", "-f", "TEMPLATE LOCATION")
func CreateClusterResourceFromTemplateWithError(oc *CLI, parameters ...string) error {
	return resourceFromTemplate(oc, true, true, "", parameters...)
}

// CreateClusterResourceFromTemplate create resource from the template.
// For ex: CreateClusterResourceFromTemplate(oc, "--ignore-unknown-parameters=true", "-f", "TEMPLATE LOCATION")
func CreateClusterResourceFromTemplate(oc *CLI, parameters ...string) {
	resourceFromTemplate(oc, true, false, "", parameters...)
}

// ApplyClusterResourceFromTemplateWithError apply resource from the template and return error if happened.
// For ex: ApplyClusterResourceFromTemplateWithError(oc, "--ignore-unknown-parameters=true", "-f", "TEMPLATE LOCATION")
func ApplyClusterResourceFromTemplateWithError(oc *CLI, parameters ...string) error {
	return resourceFromTemplate(oc, false, true, "", parameters...)
}

// ApplyClusterResourceFromTemplate apply resource from the template.
// For ex: ApplyClusterResourceFromTemplate(oc, "--ignore-unknown-parameters=true", "-f", "TEMPLATE LOCATION")
func ApplyClusterResourceFromTemplate(oc *CLI, parameters ...string) {
	resourceFromTemplate(oc, false, false, "", parameters...)
}

// CreateNsResourceFromTemplate create ns resource from the template.
// No need to add a namespace parameter in the template file as it can be provided as a function argument.
// For ex: CreateNsResourceFromTemplate(oc, "NAMESPACE", "--ignore-unknown-parameters=true", "-f", "TEMPLATE LOCATION")
func CreateNsResourceFromTemplate(oc *CLI, namespace string, parameters ...string) {
	resourceFromTemplate(oc, true, false, namespace, parameters...)
}

func resourceFromTemplate(oc *CLI, create, returnError bool, namespace string, parameters ...string) error {
	var configFile string
	err := wait.PollUntilContextTimeout(context.Background(), 3*time.Second, 15*time.Second, false, func(_ context.Context) (bool, error) {
		fileName := GetRandomString() + "config.json"
		stdout, _, err := oc.AsAdmin().Run("process").Args(parameters...).OutputsToFiles(fileName)
		if err != nil {
			e2e.Logf("the err:%v, and try next round", err)
			return false, nil
		}

		configFile = stdout
		return true, nil
	})
	if returnError && err != nil {
		e2e.Logf("fail to process %v", parameters)
		return err
	}
	AssertWaitPollNoErr(err, fmt.Sprintf("fail to process %v", parameters))

	e2e.Logf("the file of resource is %s", configFile)

	var resourceErr error
	if create {
		if namespace != "" {
			resourceErr = oc.AsAdmin().WithoutNamespace().Run("create").Args("-f", configFile, "-n", namespace).Execute()
		} else {
			resourceErr = oc.AsAdmin().WithoutNamespace().Run("create").Args("-f", configFile).Execute()
		}
	} else {
		if namespace != "" {
			resourceErr = oc.AsAdmin().WithoutNamespace().Run("apply").Args("-f", configFile, "-n", namespace).Execute()
		} else {
			resourceErr = oc.AsAdmin().WithoutNamespace().Run("apply").Args("-f", configFile).Execute()
		}
	}
	if returnError && resourceErr != nil {
		e2e.Logf("fail to create/apply resource %v", resourceErr)
		return resourceErr
	}
	AssertWaitPollNoErr(resourceErr, fmt.Sprintf("fail to create/apply resource %v", resourceErr))
	return nil
}

// GetRandomString to create random string
func GetRandomString() string {
	chars := "abcdefghijklmnopqrstuvwxyz0123456789"
	buffer := make([]byte, 8)
	for index := range buffer {
		randGen, err := rand.Int(rand.Reader, big.NewInt(int64(len(chars))))
		o.Expect(err).NotTo(o.HaveOccurred())
		buffer[index] = chars[randGen.Int64()]
	}
	return string(buffer)
}

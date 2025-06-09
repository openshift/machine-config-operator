package util

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/openshift/machine-config-operator/test/extended/testdata"
	kapierrs "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	corev1client "k8s.io/client-go/kubernetes/typed/core/v1"
	e2e "k8s.io/kubernetes/test/e2e/framework"
)

// WaitForServiceAccount waits until the named service account gets fully
// provisioned
func WaitForServiceAccount(c corev1client.ServiceAccountInterface, name string, checkSecret bool) error {
	countOutput := -1
	// add Logf for better debug, but it will possible generate many logs because of 100 millisecond
	// so, add countOutput so that it output log every 100 times (10s)
	waitFn := func(_ context.Context) (bool, error) {
		countOutput++
		sc, err := c.Get(context.Background(), name, metav1.GetOptions{})
		if err != nil {
			// If we can't access the service accounts, let's wait till the controller
			// create it.
			if kapierrs.IsNotFound(err) || kapierrs.IsForbidden(err) {
				if countOutput%100 == 0 {
					e2e.Logf("Waiting for service account %q to be available: %v (will retry) ...", name, err)
				}
				return false, nil
			}
			return false, fmt.Errorf("failed to get service account %q: %v", name, err)
		}
		secretNames := []string{}
		var hasDockercfg bool
		for _, s := range sc.Secrets {
			if strings.Contains(s.Name, "dockercfg") {
				hasDockercfg = true
			}
			secretNames = append(secretNames, s.Name)
		}
		if hasDockercfg || !checkSecret {
			return true, nil
		}
		if countOutput%100 == 0 {
			e2e.Logf("Waiting for service account %q secrets (%s) to include dockercfg ...", name, strings.Join(secretNames, ","))
		}
		return false, nil
	}
	return wait.PollUntilContextTimeout(context.Background(), 100*time.Millisecond, 3*time.Minute, false, waitFn)
}

// KubeConfigPath returns the value of KUBECONFIG environment variable
func KubeConfigPath() string {
	// can't use gomega in this method since it is used outside of It()
	return os.Getenv("KUBECONFIG")
}

var (
	fixtureDirLock sync.Once
	fixtureDir     string
)

// FixturePath returns an absolute path to a local copy of a fixture file
func FixturePath(elem ...string) string {
	fixtureDirLock.Do(func() {
		dir, err := os.MkdirTemp("", "fixture-testdata-dir")
		if err != nil {
			panic(err)
		}
		fixtureDir = dir
	})
	relativePath := path.Join(elem...)
	fullPath := path.Join(fixtureDir, relativePath)
	if err := testdata.DumpFile(fixtureDir, relativePath); err != nil {
		panic(err)
	}

	p, err := filepath.Abs(fullPath)
	if err != nil {
		panic(err)
	}
	return p
}

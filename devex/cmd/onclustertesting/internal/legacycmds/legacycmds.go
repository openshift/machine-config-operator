package legacycmds

import (
	"context"
	"fmt"
	"os"
	"time"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	"github.com/openshift/machine-config-operator/test/framework"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog/v2"
)

const (
	DefaultLayeredPoolName         string = "layered"
	CreatedByOnClusterBuildsHelper string = "machineconfiguration.openshift.io/createdByOnClusterBuildsHelper"
)

func ExtractBuildObjects(cs *framework.ClientSet, mcp *mcfgv1.MachineConfigPool, targetDir string) error {
	return extractBuildObjectsForRenderedMC(cs, mcp.Spec.Configuration.Name, targetDir)
}

func GetDir(target string) (string, error) {
	if target != "" {
		return target, nil
	}

	return os.Getwd()
}

func WaitForRenderedConfigs(cs *framework.ClientSet, pool string, mcNames ...string) (string, error) {
	var renderedConfig string
	startTime := time.Now()
	found := make(map[string]bool)

	ctx := context.Background()

	if err := wait.PollUntilContextTimeout(ctx, 2*time.Second, 5*time.Minute, true, func(ctx context.Context) (bool, error) {
		// Set up the list
		for _, name := range mcNames {
			found[name] = false
		}

		// Update found based on the MCP
		mcp, err := cs.MachineConfigPools().Get(ctx, pool, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		for _, mc := range mcp.Spec.Configuration.Source {
			if _, ok := found[mc.Name]; ok {
				found[mc.Name] = true
			}
		}

		// If any are still false, then they weren't included in the MCP
		for _, nameFound := range found {
			if !nameFound {
				return false, nil
			}
		}

		// All the required names were found
		renderedConfig = mcp.Spec.Configuration.Name
		return true, nil
	}); err != nil {
		return "", fmt.Errorf("machine configs %v hasn't been picked by pool %s (waited %s): %w", notFoundNames(found), pool, time.Since(startTime), err)
	}
	klog.Infof("Pool %s has rendered configs %v with %s (waited %v)", pool, mcNames, renderedConfig, time.Since(startTime))
	return renderedConfig, nil
}

func notFoundNames(foundNames map[string]bool) []string {
	out := []string{}
	for name, found := range foundNames {
		if !found {
			out = append(out, name)
		}
	}
	return out
}

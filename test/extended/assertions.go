package extended

import (
	"context"
	"fmt"
	"strings"
	"time"

	exutil "github.com/openshift/origin/test/extended/util"
	compat_otp "github.com/openshift/origin/test/extended/util/compat_otp"
	logger "github.com/openshift/origin/test/extended/util/compat_otp/logext"

	"k8s.io/apimachinery/pkg/util/wait"
)

// AssertAllPodsToBeReadyWithPollerParams assert all pods in NS are in ready state until timeout in a given namespace
// Pros: allow user to customize poller parameters
func AssertAllPodsToBeReadyWithPollerParams(oc *exutil.CLI, namespace string, interval, timeout time.Duration, selector string) {
	err := wait.PollUntilContextTimeout(context.Background(), interval, timeout, false, func(_ context.Context) (bool, error) {

		// get the status flag for all pods
		// except the ones which are in Complete Status.
		// it use 'ne' operator which is only compatible with 4.10+ oc versions
		template := "'{{- range .items -}}{{- range .status.conditions -}}{{- if ne .reason \"PodCompleted\" -}}{{- if eq .type \"Ready\" -}}{{- .status}} {{\" \"}}{{- end -}}{{- end -}}{{- end -}}{{- end -}}'"
		baseArgs := []string{"pods", "-n", namespace}
		if selector != "" {
			baseArgs = append(baseArgs, "-l", selector)
		}
		stdout, err := oc.AsAdmin().Run("get").Args(baseArgs...).Template(template).Output()
		if err != nil {
			logger.Infof("the err:%v, and try next round", err)
			return false, nil
		}
		if strings.Contains(stdout, "False") {
			return false, nil
		}
		return true, nil
	})
	compat_otp.AssertWaitPollNoErr(err, fmt.Sprintf("Some Pods are not ready in NS %s!", namespace))
}

// AssertAllPodsToBeReadyWithSelector assert all pods in NS are in ready state until timeout in a given namespace
// The selector parameter follows the regular oc/kubectl format for the --selector option.
func AssertAllPodsToBeReadyWithSelector(oc *exutil.CLI, namespace, selector string) {
	AssertAllPodsToBeReadyWithPollerParams(oc, namespace, 10*time.Second, 4*time.Minute, selector)
}

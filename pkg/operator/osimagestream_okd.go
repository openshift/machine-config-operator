//go:build fcos || scos

package operator

import (
	configv1 "github.com/openshift/api/config/v1"
	"k8s.io/klog/v2"
)

func (optr *Operator) syncOSImageStream(_ *renderConfig, _ *configv1.ClusterOperator) error {
	klog.V(4).Info("OSImageStream sync skipped")
	return nil
}

package v1

import (
	"fmt"

	"k8s.io/apimachinery/pkg/runtime"
)

// DeepCopyObject copies the CVOConfig into an Object. This doesn't actually
// require a deep copy, but the code generator (and Go itself) isn't advanced
// enough to determine that.
func (c *CVOConfig) DeepCopyObject() runtime.Object {
	out := *c
	c.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	return &out
}

// DeepCopyInto copies the CVOConfig into another CVOConfig. This doesn't
// actually require a deep copy, but the code generator (and Go itself) isn't
// advanced enough to determine that.
func (c *CVOConfig) DeepCopyInto(out *CVOConfig) {
	*out = *c
	c.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
}

func (c CVOConfig) String() string {
	return fmt.Sprintf("{ Upstream: %s Channel: %s ClusterID: %s }", c.Upstream, c.Channel, c.ClusterID)
}

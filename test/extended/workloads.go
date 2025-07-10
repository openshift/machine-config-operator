package extended

import exutil "github.com/openshift/machine-config-operator/test/extended/util"

// Job struct to handle Job resources
type Job struct {
	Resource
}

// NewJob constructs a new Job struct
func NewJob(oc *exutil.CLI, namespace, name string) *Job {
	return &Job{*NewNamespacedResource(oc, "job", namespace, name)}
}

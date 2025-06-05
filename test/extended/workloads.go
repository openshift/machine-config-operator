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

// GetPods returns the pods triggered by this job
func (j Job) GetPods() ([]Pod, error) {
	pl := NewPodList(j.GetOC(), j.GetNamespace())
	pl.ByLabel("job-name=" + j.GetName())
	return pl.GetAll()
}

// GetFirstPod  returns the first pod triggered by the job
func (j Job) GetFirstPod() (*Pod, error) {
	pods, err := j.GetPods()
	if err != nil {
		return nil, err
	}
	if len(pods) == 0 {
		return nil, nil
	}

	return &(pods[0]), nil
}

// GetActive returns the number of active pods for this job
func (j Job) GetActive() (string, error) {
	return j.Get(`{.status.active}`)
}

// GetReady returns the number of ready pods for this job
func (j Job) GetReady() (string, error) {
	return j.Get(`{.status.ready}`)
}

// Pod struct to handle Pod resources
type Pod struct {
	Resource
}

// PodList struct to handle lists of Pod resources
type PodList struct {
	ResourceList
}

// NewPod constructs a new Pod struct
func NewPod(oc *exutil.CLI, namespace, name string) *Pod {
	return &Pod{*NewNamespacedResource(oc, "pod", namespace, name)}
}

// NewPodList constructs a new PodList struct to handle all existing Pods
func NewPodList(oc *exutil.CLI, namespace string) *PodList {
	return &PodList{*NewNamespacedResourceList(oc, "pod", namespace)}
}

// GetAll returns a []Pod slice with all existing nodes
func (pl PodList) GetAll() ([]Pod, error) {
	allMResources, err := pl.ResourceList.GetAll()
	if err != nil {
		return nil, err
	}
	allMs := make([]Pod, 0, len(allMResources))

	for _, mRes := range allMResources {
		allMs = append(allMs, *NewPod(pl.oc, mRes.GetNamespace(), mRes.GetName()))
	}

	return allMs, nil
}

package extended

import (
	"encoding/json"
	"time"

	o "github.com/onsi/gomega"
	exutil "github.com/openshift/machine-config-operator/test/extended-priv/util"
	logger "github.com/openshift/machine-config-operator/test/extended-priv/util/logext"
)

type Pinnedimage struct {
	Name string `json:"name"`
}

// PinnedImageSet resource type declaration
type PinnedImageSet struct {
	Resource
}

// PinnedImageSetList handles list of PinnedImageSet
type PinnedImageSetList struct {
	ResourceList
}

// NewPinnedImageSetList construct a new PinnedImageSet list struct to handle all existing PinnedImageSet
func NewPinnedImageSetList(oc *exutil.CLI) *PinnedImageSetList {
	return &PinnedImageSetList{*NewResourceList(oc, "pinnedimageset")}
}

// NewPinnedImageSet constructor to get PinnedImageSet resource
func NewPinnedImageSet(oc *exutil.CLI, name string) *PinnedImageSet {
	return &PinnedImageSet{Resource: *NewResource(oc, "pinnedimageset", name)}
}

// CreateGenericPinnedImageSet uses a generic template to create a PinnedImageSet resource
func CreateGenericPinnedImageSet(oc *exutil.CLI, name, pool string, images []string) (*PinnedImageSet, error) {
	logger.Infof("Creating PinnedImageSet %s in pool %s with images %s", name, pool, images)
	newPIS := NewPinnedImageSet(oc, name)
	pinnedImages := []Pinnedimage{}
	for i := range images {
		pinnedImages = append(pinnedImages, Pinnedimage{Name: images[i]})
	}

	JSONImages, err := json.Marshal(pinnedImages)

	if err != nil {
		return newPIS, err
	}

	err = NewMCOTemplate(oc, "generic-pinned-image-set.yaml").Create("-p", "NAME="+name, "POOL="+pool, "IMAGES="+string(JSONImages))
	if err != nil {
		return newPIS, err
	}

	return newPIS, nil
}

// GetPools returns the pools where this PinnedImageSet will be applied
func (pis PinnedImageSet) GetPools() ([]*MachineConfigPool, error) {

	returnPools := []*MachineConfigPool{}
	pools, err := NewMachineConfigPoolList(pis.GetOC()).GetAll()
	if err != nil {
		return nil, err
	}

	for _, item := range pools {
		pool := item
		ps, err := pool.GetPinnedImageSets()
		if err != nil {
			return nil, err
		}
		for _, p := range ps {
			if p.GetName() == pis.GetName() {
				returnPools = append(returnPools, pool)
				break
			}
		}
	}

	return returnPools, nil
}

func (pis PinnedImageSet) DeleteAndWait(waitingTime time.Duration) error {
	if !pis.Exists() {
		logger.Infof("%s does not exist! No need to delete it!", pis)
		return nil
	}

	pools, err := pis.GetPools()
	if err != nil {
		return err
	}

	err = pis.Delete()
	if err != nil {
		return err
	}

	for _, pool := range pools {
		err := pool.waitForPinComplete(waitingTime)
		if err != nil {
			return err
		}
	}
	return nil
}

// GetAll returns a []*PinnedImageSet list with all existing pinnedimageset sorted by creation timestamp
func (pisl *PinnedImageSetList) GetAll() ([]*PinnedImageSet, error) {
	pisl.ResourceList.SortByTimestamp()
	allPISResources, err := pisl.ResourceList.GetAll()
	if err != nil {
		return nil, err
	}
	allPISs := make([]*PinnedImageSet, 0, len(allPISResources))

	for _, pisRes := range allPISResources {
		allPISs = append(allPISs, NewPinnedImageSet(pisl.oc, pisRes.name))
	}

	return allPISs, nil
}

// GetAllOrFail returns a []*PinnedImageSet list with all existing pinnedimageset sorted by creation time, if any error happens it fails the test
func (pisl *PinnedImageSetList) GetAllOrFail() []*PinnedImageSet {
	piss, err := pisl.GetAll()
	o.ExpectWithOffset(1, err).NotTo(o.HaveOccurred(), "Error getting the list of existing PinnedImageSet in the cluster")
	return piss
}

package operator

import (
	"sort"
	"sync"

	configv1 "github.com/openshift/api/config/v1"
)

// versionStore stores versions for important components.
type versionStore struct {
	*sync.Mutex

	versions map[string]string
}

func newVersionStore() *versionStore {
	return &versionStore{
		Mutex:    &sync.Mutex{},
		versions: map[string]string{},
	}
}

func (vs *versionStore) Set(name, version string) {
	vs.Lock()
	defer vs.Unlock()
	vs.versions[name] = version
}

func (vs *versionStore) Get(name string) (string, bool) {
	vs.Lock()
	defer vs.Unlock()
	v, ok := vs.versions[name]
	return v, ok
}

func (vs *versionStore) GetAll() []configv1.OperandVersion {
	vs.Lock()
	defer vs.Unlock()
	var opvs []configv1.OperandVersion
	for name, version := range vs.versions {
		opvs = append(opvs, configv1.OperandVersion{Name: name, Version: version})
	}

	sort.Slice(opvs, func(i, j int) bool { return opvs[i].Name < opvs[j].Name })
	return opvs
}

// Equal returns true when all operands in opvs are same version as versionStore.
func (vs *versionStore) Equal(opvs []configv1.OperandVersion) bool {
	vs.Lock()
	defer vs.Unlock()
	for n, v := range vs.versions {
		matched := false
		for _, op := range opvs {
			if op.Name == n && op.Version == v {
				matched = true
				break
			}
		}
		if matched {
			continue
		}
		return false
	}
	return true
}

package utils

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	"github.com/stretchr/testify/assert"
)

func TestUniqueObjects(t *testing.T) {
	mosc1 := &mcfgv1.MachineOSConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: "mosc-1",
			UID:  "mosc-1-uid",
		},
	}

	mosc2 := &mcfgv1.MachineOSConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: "mosc-2",
			UID:  "mosc-2-uid",
		},
	}

	mosb1 := &mcfgv1.MachineOSBuild{
		ObjectMeta: metav1.ObjectMeta{
			Name: "mosb-1",
			UID:  "mosb-1-uid",
		},
	}

	mosb2 := &mcfgv1.MachineOSBuild{
		ObjectMeta: metav1.ObjectMeta{
			Name: "mosb-2",
			UID:  "mosb-2-uid",
		},
	}

	all := []metav1.Object{
		mosc1,
		mosc2,
		mosb1,
		mosb2,
	}

	uniq := NewUniqueObjects()
	uniq.InsertAll(all)
	assert.Equal(t, len(all), uniq.Len())
	assert.Equal(t, len(all), len(uniq.Keys()))

	uniq.InsertAll(all)
	assert.Equal(t, len(all), uniq.Len())
	assert.Equal(t, len(all), len(uniq.Keys()))

	out := uniq.UnsortedSlice()

	for _, item := range out {
		assert.True(t, uniq.Has(item))
		assert.Contains(t, out, item)
	}

	for key, item := range uniq.Map() {
		assert.True(t, uniq.Has(item))

		obj, isFound := uniq.GetByKey(key)
		assert.Contains(t, all, obj)
		assert.True(t, isFound)
	}

	assert.True(t, uniq.HasKey(UniqueObjectKey{
		Name: "mosb-1",
		UID:  "mosb-1-uid",
	}))

	assert.False(t, uniq.HasKey(UniqueObjectKey{
		Name: "mosb-3",
	}))

	obj, isFound := uniq.GetByKey(UniqueObjectKey{
		Name: "mosb-1",
		UID:  "mosb-1-uid",
	})

	assert.Equal(t, mosb1, obj)
	assert.True(t, isFound)

	obj, isFound = uniq.GetByKey(UniqueObjectKey{
		Name: "mosb-3",
	})

	assert.Nil(t, obj)
	assert.False(t, isFound)
}

package utils

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Provides an easy way to ensure that any given object(s) are unique. In this
// case, we determine uniqueness by getting the object name and UID.
// TODO: Figure out how to use generics for this and make it threadsafe.
type UniqueObjects struct {
	objects map[UniqueObjectKey]metav1.Object
}

type UniqueObjectKey struct {
	Name string
	UID  string
}

func NewUniqueObjects() *UniqueObjects {
	return &UniqueObjects{
		objects: map[UniqueObjectKey]metav1.Object{},
	}
}

// Returns a copy of the underlying map.
func (u *UniqueObjects) Map() map[UniqueObjectKey]metav1.Object {
	out := make(map[UniqueObjectKey]metav1.Object, len(u.objects))

	for key, val := range u.objects {
		out[key] = val
	}

	return out
}

// Inserts a single object.
func (u *UniqueObjects) Insert(obj metav1.Object) {
	key := u.computeKey(obj)

	if !u.HasKey(key) {
		u.objects[key] = obj
	}
}

// Inserts multiple objects.
func (u *UniqueObjects) InsertAll(objs []metav1.Object) {
	for _, obj := range objs {
		u.Insert(obj)
	}
}

// Determines if the object already exists.
func (u *UniqueObjects) Has(obj metav1.Object) bool {
	key := u.computeKey(obj)
	return u.HasKey(key)
}

// Retrieves an object given a key, if found.
func (u *UniqueObjects) GetByKey(k UniqueObjectKey) (metav1.Object, bool) {
	obj, ok := u.objects[k]
	return obj, ok
}

// Allows querying by key.
func (u *UniqueObjects) HasKey(k UniqueObjectKey) bool {
	_, ok := u.objects[k]
	return ok
}

// Returns all keys.
func (u *UniqueObjects) Keys() []UniqueObjectKey {
	out := []UniqueObjectKey{}

	for key := range u.objects {
		out = append(out, key)
	}

	return out
}

// Computes the key for the object.
func (u *UniqueObjects) computeKey(obj metav1.Object) UniqueObjectKey {
	return UniqueObjectKey{
		Name: obj.GetName(),
		UID:  string(obj.GetUID()),
	}
}

// Gets the length of the underlying map.
func (u *UniqueObjects) Len() int {
	return len(u.objects)
}

// Returns an unsorted slice of the items in the map.
func (u *UniqueObjects) UnsortedSlice() []metav1.Object {
	out := []metav1.Object{}

	for _, obj := range u.objects {
		out = append(out, obj)
	}

	return out
}

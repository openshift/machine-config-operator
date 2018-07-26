package controller

import "k8s.io/client-go/tools/cache"

var (
	// KeyFunc is returns key for an object
	KeyFunc = cache.DeletionHandlingMetaNamespaceKeyFunc
)

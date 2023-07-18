package common

import "k8s.io/client-go/tools/record"

// Controller is the common interface all controllers implement
type Controller interface {
	Run(workers int, stopCh <-chan struct{}, healthEvents record.EventRecorder)
}

package common

import "context"

// Controller is the common interface all controllers implement
type Controller interface {
	Run(ctx context.Context, workers int)
	Name() string
}

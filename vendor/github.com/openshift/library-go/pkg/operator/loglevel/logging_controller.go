package loglevel

import (
	"context"

	operatorv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/library-go/pkg/controller/factory"
	"github.com/openshift/library-go/pkg/operator/events"
	"github.com/openshift/library-go/pkg/operator/management"
	operatorv1helpers "github.com/openshift/library-go/pkg/operator/v1helpers"
	"k8s.io/apimachinery/pkg/api/errors"
)

type LogLevelController struct {
	operatorClient operatorv1helpers.OperatorClient

	// for unit tests only
	setLogLevelFn func(operatorv1.LogLevel) error
	getLogLevelFn func() (operatorv1.LogLevel, bool)

	defaultLogLevel operatorv1.LogLevel
}

// NewClusterOperatorLoggingController sets a klog level for the operator based on the operator config.
// If the loglevel is not set the default "Normal" level will be used.
// This controller supports removable operands, as configured in pkg/operator/management and uses level "Normal"
// if the operator CR is missing.
func NewClusterOperatorLoggingController(operatorClient operatorv1helpers.OperatorClient, recorder events.Recorder) factory.Controller {
	return NewClusterOperatorLoggingControllerWithLogLevel(operatorClient, operatorv1.Normal, recorder)
}

// NewClusterOperatorLoggingControllerWithLogLevel sets a klog level for the operator based on the operator config, using the given default log level if the operator config does not specify anything
func NewClusterOperatorLoggingControllerWithLogLevel(operatorClient operatorv1helpers.OperatorClient, defaultLogLevel operatorv1.LogLevel, recorder events.Recorder) factory.Controller {
	c := &LogLevelController{
		operatorClient:  operatorClient,
		setLogLevelFn:   SetLogLevel,
		getLogLevelFn:   GetLogLevel,
		defaultLogLevel: defaultLogLevel,
	}
	return factory.New().
		WithInformers(operatorClient.Informer()).
		WithSync(c.sync).
		ToController(
			"LoggingSyncer", // don't change what is passed here unless you also remove the old FooDegraded condition
			recorder,
		)
}

// sync reacts to a change in prereqs by finding information that is required to match another value in the cluster. This
// must be information that is logically "owned" by another component.
func (c LogLevelController) sync(ctx context.Context, syncCtx factory.SyncContext) error {
	detailedSpec, _, _, err := c.operatorClient.GetOperatorState()
	if errors.IsNotFound(err) && management.IsOperatorRemovable() {
		return nil
	}
	if err != nil {
		return err
	}

	currentLogLevel, isUnknown := c.getLogLevelFn()
	desiredLogLevel := detailedSpec.OperatorLogLevel

	if len(desiredLogLevel) == 0 {
		desiredLogLevel = c.defaultLogLevel
	}

	if !ValidLogLevel(desiredLogLevel) {
		syncCtx.Recorder().Warningf("OperatorLogLevelInvalid", "Invalid logLevel %q, falling back to %q", desiredLogLevel, c.defaultLogLevel)
		desiredLogLevel = c.defaultLogLevel
	}

	// correct log level is set and it matches the expected log level from operator operatorSpec, do nothing.
	if !isUnknown && currentLogLevel == desiredLogLevel {
		return nil
	}

	// log level is not specified in operatorSpec and the log verbosity is not set (0), default the log level to V(2).
	if len(desiredLogLevel) == 0 {
		desiredLogLevel = currentLogLevel
	}

	// Set the new loglevel if the operator operatorSpec changed
	if err := c.setLogLevelFn(desiredLogLevel); err != nil {
		syncCtx.Recorder().Warningf("OperatorLogLevelChangeFailed", "Unable to change operator log level from %q to %q: %v", currentLogLevel, desiredLogLevel, err)
		return err
	}

	// Do not fire event on every restart.
	if isUnknown {
		return nil
	}

	syncCtx.Recorder().Eventf("OperatorLogLevelChange", "Operator log level changed from %q to %q", currentLogLevel, desiredLogLevel)
	return nil
}

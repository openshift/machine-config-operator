package daemon

import (
	"github.com/golang/glog"
)

/*
	Differences/drift/conflict between OCP and OKD should be codified here.
	RHCOS/OCP defaults are the rule for the MCD.

	Important NOTE: the MCD is most likely the wrong place to put differences.
		Most changes can and should be placed in https://github.com/openshift/okd-machine-os

	As a general rule, the MCO/MCD should only understand the differences of how
	to render for OCP and OKD. If the configuration is a base configuration
	then it most likely should go in the `okd-machine-os` repo.

*/

// SSH Keys for user "core" will only be written at /home/core/.ssh
const fcosDefaultCoreUserSSHPath = ".ssh/authorized_keys.d/ignition"

// okdBooterFunc are specific boot up functions for OKD.
type okdBooterFunc func() error

// okdBoot is an okdBooterFunc that defaults to a nooperFunc
// if the logic starts getting more complicated then we should consider
// an interface to handle this instead of runtime set function.
var okdBoot okdBooterFunc = nooperFunc

// Set the difference for OKD. This file will only be compiled when
// the OKD build flag is set.
func init() {
	o := OperatingSystem{}
	if !o.IsFCOS() {
		setOkd()
	}
}

// setOkd sets the OKD defaults. It is called either in an init()
// or in okd_test.go.
func setOkd() {
	glog.Info("Node is an FCOS node")
	coreSSHKeyFilePath = fcosDefaultCoreUserSSHPath
	okdBoot = okdBooter
}

// unsetOkd resets the defaults.
func unsetOkd() {
	coreSSHKeyFilePath = defaultCoreSSHKeyFilePath
	userLookup = defaultUserLookup
	okdBoot = nooperFunc
}

// okdBooter is an okdBooterFunc. This is a stub function for now to document
// where to put the differences.
func okdBooter() error {
	glog.Info("Using OKD specific rules")
	return nil
}

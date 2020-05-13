package internal

import (
	"flag"
	"os"

	"github.com/golang/glog"
	"k8s.io/klog"

	"github.com/openshift/machine-config-operator/pkg/version"
)

// InitLogging parses options already registered to the flag package,
// setting up glog, klog, and any additional flags which the
// machine-config command may have configured.  It also logs the
// current machine-config version.
func InitLogging() {
	flag.Set("logtostderr", "true")
	flag.Parse()

	klogFlags := flag.NewFlagSet("klog", flag.ExitOnError)
	klog.InitFlags(klogFlags)

	flag.CommandLine.VisitAll(func(f1 *flag.Flag) {
		f2 := klogFlags.Lookup(f1.Name)
		if f2 != nil {
			value := f1.Value.String()
			f2.Value.Set(value)
		}
	})

	// To help debugging, immediately log version
	releaseVersion := os.Getenv("RELEASE_VERSION")
	if releaseVersion == "" {
		glog.Infof("Version: %s (%s)", version.Raw, version.Hash)
	} else {
		glog.Infof("Version: %s (Raw: %s, Hash: %s)", releaseVersion, version.Raw, version.Hash)
	}
}

package utils

import (
	"flag"
	"fmt"
	"os"
	"os/exec"

	aggerrs "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/klog/v2"
)

func CheckForBinaries(bins []string) error {
	errs := []error{}

	for _, bin := range bins {
		if _, err := exec.LookPath(bin); err != nil {
			errs = append(errs, fmt.Errorf("required binary %q not found: %w", bin, err))
		}
	}

	return aggerrs.NewAggregate(errs)
}

func ToEnvVars(in map[string]string) []string {
	out := os.Environ()

	for key, val := range in {
		envVar := fmt.Sprintf("%s=%s", key, val)
		out = append(out, envVar)
	}

	return out
}

func ParseFlags() {
	flag.Set("v", "4")
	flag.Set("logtostderr", "true")
	flag.Parse()
}

func ParseFlagsAndPrintOpts(opts interface{}) {
	ParseFlags()

	klog.Infof("Options parsed: %+v", opts)
}

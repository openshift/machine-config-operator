package extended

import (
	exutil "github.com/openshift/machine-config-operator/test/extended/util"
	logger "github.com/openshift/machine-config-operator/test/extended/util/logext"
)

func getImageFromReleaseInfo(oc *exutil.CLI, imageName, dockerConfigFile string) (string, error) {
	stdout, stderr, err := oc.Run("adm").Args("release", "info", "--insecure", "--image-for", imageName,
		"--registry-config", dockerConfigFile).Outputs()
	if err != nil {
		logger.Errorf("STDOUT: %s", stdout)
		logger.Errorf("STDERR: %s", stderr)
		return stdout + stderr, err
	}

	return stdout, nil
}

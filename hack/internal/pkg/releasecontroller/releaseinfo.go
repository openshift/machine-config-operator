package releasecontroller

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// There is a way to do this in pure Go, but I'm lazy :P.
func GetComponentPullspecForRelease(componentName, releasePullspec string) (string, error) {
	template := fmt.Sprintf("{{range .references.spec.tags}}{{if eq .name %q}}{{.from.name}}{{end}}{{end}}", componentName)

	outBuf := bytes.NewBuffer([]byte{})

	cmd := exec.Command("oc", "adm", "release", "info", "-o=template="+template, releasePullspec)
	cmd.Stdout = outBuf
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("could not get pullspec for component %q from release pullspec %q: %w", componentName, releasePullspec, err)
	}

	return strings.TrimSpace(outBuf.String()), nil
}

func GetReleaseInfo(releasePullspec string) ([]byte, error) {
	outBuf := bytes.NewBuffer([]byte{})
	stderrBuf := bytes.NewBuffer([]byte{})

	cmd := exec.Command("oc", "adm", "release", "info", "-o=json", releasePullspec)
	cmd.Stdout = outBuf
	cmd.Stderr = stderrBuf

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("could not run %s, got output: %s %s", cmd, outBuf.String(), stderrBuf.String())
	}

	return outBuf.Bytes(), nil
}

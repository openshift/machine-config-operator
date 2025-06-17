package extended

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"strings"

	logger "github.com/openshift/machine-config-operator/test/extended/util/logext"
	e2e "k8s.io/kubernetes/test/e2e/framework"
)

// SkopeoCLI provides function to run the docker command
type SkopeoCLI struct {
	execPath        string
	ExecCommandPath string
	globalArgs      []string
	commandArgs     []string
	finalArgs       []string
	verbose         bool
	stdin           *bytes.Buffer
	stdout          io.Writer
	stderr          io.Writer
	showInfo        bool
	UnsetProxy      bool
	env             []string
	authFile        string
}

type ExitError struct {
	Cmd    string
	StdErr string
	*exec.ExitError
}

// NewSkopeoCLI initialize the docker cli framework
func NewSkopeoCLI() *SkopeoCLI {
	newclient := &SkopeoCLI{}
	newclient.execPath = "skopeo"
	newclient.showInfo = true
	newclient.UnsetProxy = false
	return newclient
}

// Run executes given skopeo command
func (c *SkopeoCLI) Run(commands ...string) *SkopeoCLI {
	in, out, errout := &bytes.Buffer{}, &bytes.Buffer{}, &bytes.Buffer{}
	skopeo := &SkopeoCLI{
		execPath:        c.execPath,
		ExecCommandPath: c.ExecCommandPath,
		UnsetProxy:      c.UnsetProxy,
		showInfo:        c.showInfo,
		env:             c.env,
	}

	skopeo.globalArgs = commands
	if c.authFile != "" {
		skopeo.globalArgs = append(skopeo.globalArgs, "--authfile", c.authFile)
	}
	skopeo.stdin, skopeo.stdout, skopeo.stderr = in, out, errout
	return skopeo.setOutput(c.stdout)
}

// Output executes the command and returns stdout combined into one string
func (c *SkopeoCLI) Output() (string, error) {
	if c.verbose {
		logger.Infof("DEBUG: skopeo %s\n", c.printCmd())
	}
	cmd := exec.Command(c.execPath, c.finalArgs...)
	cmd.Env = os.Environ()
	if c.UnsetProxy {
		var envCmd []string
		for _, envIndex := range cmd.Env {
			if !(strings.Contains(strings.ToUpper(envIndex), "HTTP_PROXY") || strings.Contains(strings.ToUpper(envIndex), "HTTPS_PROXY") || strings.Contains(strings.ToUpper(envIndex), "NO_PROXY")) {
				envCmd = append(envCmd, envIndex)
			}
		}
		cmd.Env = envCmd
	}
	if c.env != nil {
		cmd.Env = append(cmd.Env, c.env...)
	}
	if c.ExecCommandPath != "" {
		logger.Infof("set exec command path is %s\n", c.ExecCommandPath)
		cmd.Dir = c.ExecCommandPath
	}
	cmd.Stdin = c.stdin
	if c.showInfo {
		logger.Infof("Running '%s %s'", c.execPath, strings.Join(c.finalArgs, " "))
	}
	out, err := cmd.Output()
	trimmed := strings.TrimSpace(string(out))
	switch e := err.(type) {
	case nil:
		c.stdout = bytes.NewBuffer(out)
		return trimmed, nil
	case *exec.ExitError:
		c.stdout = bytes.NewBuffer(out)
		c.stderr = bytes.NewBuffer(e.Stderr)
		logger.Errorf("Error running %v:\nSTDOUT:%s\nSTDERR:%s", cmd, trimmed, string(e.Stderr))
		return trimmed, &ExitError{ExitError: e, Cmd: c.execPath + " " + strings.Join(c.finalArgs, " "), StdErr: trimmed}
	default:
		e2e.Failf("unable to execute %q: %v", c.execPath, err)
		return "", nil
	}
}

func (c *SkopeoCLI) printCmd() string {
	return strings.Join(c.finalArgs, " ")
}

// Args sets the additional arguments for the skopeo CLI command
func (c *SkopeoCLI) Args(args ...string) *SkopeoCLI {
	c.commandArgs = args
	c.finalArgs = c.globalArgs
	c.finalArgs = append(c.finalArgs, c.commandArgs...)

	return c
}

// setOutput allows to override the default command output
func (c *SkopeoCLI) setOutput(out io.Writer) *SkopeoCLI {
	c.stdout = out
	return c
}

// SetAuthFile sets a file to be used to authorize skopeo. If an authFile is set, all commands will be executed with '--authfile authFile' parameters
func (c *SkopeoCLI) SetAuthFile(authFile string) *SkopeoCLI {
	c.authFile = authFile
	return c
}

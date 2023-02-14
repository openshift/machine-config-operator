package template

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"text/template"

	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/stretchr/testify/require"
)

// This test does the following:
// 1. Render all of the files contained in the template dir for all available configs.
// 2. Filter the ones which contain shell (Bash) scripts.
// 3. Render the inner templates (if needed).
// 4. Run each rendered script through the Shellcheck command.
//
// The end-goal is to get static analysis and syntax checking for all template
// config changes as fast as possible.
func TestTemplateShellcheck(t *testing.T) {
	rootTestName := t.Name()

	// If we don't have the shellcheck binary in our path, skip this test unless we're in CI.
	shellcheckPath, err := exec.LookPath("shellcheck")
	if errors.Is(err, exec.ErrNotFound) {
		if _, inCI := os.LookupEnv("OPENSHIFT_CI"); !inCI {
			t.Skip("Shellcheck not found in path, skipping test!")
		} else {
			t.Fatalf("Shellcheck not found in path")
		}
	}

	// Log the current shellcheck version.
	cmd := exec.Command(shellcheckPath, "--version")
	out := bytes.NewBuffer([]byte{})
	cmd.Stdout = out
	if err := cmd.Run(); err != nil {
		t.Fatalf("could not run shellcheck (%s): %s", shellcheckPath, err)
	} else {
		t.Log(out.String())
	}

	// Each of the different configs generates a slightly different set of scripts.
	for configName, config := range configs {
		// Run each config as a subtest so we can parallelize them.
		t.Run(configName, func(t *testing.T) {
			t.Parallel()
			controllerConfig, err := controllerConfigFromFile(config)
			if err != nil {
				t.Fatalf("failed to get controllerconfig config: %v", err)
			}

			cfgs, err := generateTemplateMachineConfigs(&RenderConfig{&controllerConfig.Spec, `{"dummy":"dummy"}`, nil, nil}, templateDir)
			if err != nil {
				t.Fatalf("failed to generate machine configs: %v", err)
			}

			// Everything up until this point resembles the setup of
			// TestGenerateMachineConfigs. This is intentional because we want to
			// ensure as many corner cases as possible are covered for rendering
			// scripts.
			for _, cfg := range cfgs {
				cfg := cfg
				t.Run(cfg.GetName(), func(t *testing.T) {
					ign, err := ctrlcommon.ParseAndConvertConfig(cfg.Spec.Config.Raw)
					if err != nil {
						t.Errorf("Failed to parse Ignition config")
					}

					// We put the t.Parallel() call in here because the Shellcheck tests
					// are the ones we care the most about running in parallel due to
					// speed.
					t.Parallel()

					for _, ignFile := range ign.Storage.Files {
						ignFile := ignFile
						decoded, err := ctrlcommon.DecodeIgnitionFileContents(ignFile.Contents.Source, ignFile.Contents.Compression)
						if err != nil {
							t.Errorf("could not decode ignition file contents: %s", err)
						}

						// We're only interested in script files for this test. In the
						// future, we could potentially hook in other static analysis tools
						// to handle systemd units and others.
						if !bytes.HasPrefix(decoded, []byte("#!/bin/bash")) {
							continue
						}

						// Some files contain a template within a template.
						// I'm sure there's something else that comes along and renders the
						// inner tmplate after the outer template has already been
						// rendered; I just don't know where it is!
						if strings.Contains(ignFile.Path, "tmpl") {
							inner, err := renderInnerTemplate(decoded)
							if err != nil {
								t.Fatalf("could not render inner template with dummy data: %s", err)
							}

							decoded = inner
						}

						t.Run("Syntax Check"+ignFile.Path, func(t *testing.T) {
							t.Parallel()
							tmpDir := t.TempDir()
							filename := filepath.Join(tmpDir, filepath.Base(ignFile.Path))
							require.NoError(t, os.WriteFile(filename, []byte(decoded), 0o755))

							cmd := exec.Command("/bin/bash", "-n", filename)
							cmd.Stdout = os.Stdout
							cmd.Stderr = os.Stderr

							if err := cmd.Run(); err != nil {
								t.Fail()
							}
						})

						t.Run("Shellcheck"+ignFile.Path, func(t *testing.T) {
							t.Parallel()
							shellcheckArgs := []string{
								"--color=always", // Colors are nice and look great in CI output.
								"--shell=bash",   // Specify the shell dialect for shellcheck.
								// "--severity=warning", // We should error on warnings. TODO: Wire this up to a CLI flag or env var.
								"-", // Read from stdin.
							}

							cmd := exec.Command(shellcheckPath, shellcheckArgs...)

							cmd.Stdin = bytes.NewBuffer(decoded)
							// Both cmd.Stdin and cmd.Stdout accept an io.Reader interface which
							// bytes.Buffer implements. However, the io.Reader interface does not include a
							// .String() method, which we'll need for capturing the output. This is why we
							// need to store a reference to the actual bytes.Buffer object.
							out := bytes.NewBuffer([]byte{})
							cmd.Stdout = out

							if err := cmd.Run(); err != nil {
								t.Logf("Shellcheck errors detected for rendered file (%s):\n", ignFile.Path)
								// When reading from stdin, shellcheck outputs, "In -" which
								// isn't very helpful in determining which file the Shellcheck
								// occured in. So we substitute it with the path specified in
								// the template file.
								outStr := strings.ReplaceAll(out.String(), "In -", "In "+ignFile.Path)
								t.Logf(outStr)

								// If we have the ARTIFACT_DIR env var, that (usually) means
								// we're in CI. So lets write the rendered files and the
								// shellcheck reports to the ARTIFACT_DIR so this information
								// is easily surfaceable.
								if artifactDir, ok := os.LookupEnv("ARTIFACT_DIR"); ok {
									subTestPath := filepath.Join(artifactDir, "shellcheck-results", strings.ReplaceAll(t.Name(), rootTestName, ""))
									subTestPath = strings.ReplaceAll(subTestPath, "Shellcheck/", "")
									require.NoError(t, os.MkdirAll(subTestPath, 0o755))
									t.Logf("Wrote rendered data to %s", filepath.Join(subTestPath, "rendered"))
									require.NoError(t, os.WriteFile(filepath.Join(subTestPath, "rendered"), decoded, 0o755))
									t.Logf("Wrote shellcheck report to: %s", filepath.Join(subTestPath, "shellcheck-report.txt"))
									require.NoError(t, os.WriteFile(filepath.Join(subTestPath, "shellcheck-report.txt"), []byte(outStr), 0o755))
								} else {
									t.Log("ARTIFACT_DIR not set, not writing shellcheck results to disk.")
								}

								// Ideally, we should fail whenever shellcheck does not exit
								// cleanly bceause if you were running shellcheck in CI, this
								// would cause the overall check to fail.
								t.Fail()
							}
						})
					}
				})
			}
		})
	}
}

func renderInnerTemplate(contents []byte) ([]byte, error) {
	dummy := map[string]interface{}{
		"LBConfig": map[string]string{
			"LbPort":  "8080",
			"ApiPort": "9090",
		},
		"NonVirtualIP": "12.34.56.78",
	}

	tmpl, err := template.New("inner").Parse(string(contents))
	if err != nil {
		return nil, fmt.Errorf("inner template parse failed: %w", err)
	}

	out := bytes.NewBuffer([]byte{})

	if err := tmpl.Execute(out, dummy); err != nil {
		return nil, fmt.Errorf("inner template render failed: %w", err)
	}

	return out.Bytes(), nil
}

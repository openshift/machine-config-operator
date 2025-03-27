package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"

	ign3types "github.com/coreos/ignition/v2/config/v3_5/types"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/test/framework"
	"github.com/spf13/cobra"
	"golang.org/x/sync/errgroup"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
	"sigs.k8s.io/yaml"
)

var (
	diffCmd = &cobra.Command{
		Use:   "diff",
		Short: "Diffs MachineConfigs",
		Long:  "",
		RunE: func(_ *cobra.Command, args []string) error {
			return diffMCs(args)
		},
	}

	convertToYAML bool
	keepFiles     bool
)

func init() {
	rootCmd.AddCommand(diffCmd)
	diffCmd.PersistentFlags().BoolVar(&convertToYAML, "convert-to-yaml", false, "Converts any JSON payloads that are found into YAML before diffing")
	diffCmd.PersistentFlags().BoolVar(&keepFiles, "keep-files", false, "Keeps the files used for diffing")
}

func diffMCs(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("no MachineConfigs given")
	}

	if len(args) == 1 {
		return fmt.Errorf("only one MachineConfig given")
	}

	cs := framework.NewClientSet("")

	eg := errgroup.Group{}

	mc1 := args[0]
	mc2 := args[1]

	dirname := ""
	if keepFiles {
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}

		dirname = cwd
	} else {

		tempdir, err := os.MkdirTemp("", "")
		if err != nil {
			return err
		}

		defer os.RemoveAll(tempdir)

		dirname = tempdir
	}

	eg.Go(func() error {
		return getMCAndWriteToFile(cs, dirname, mc1)
	})

	eg.Go(func() error {
		return getMCAndWriteToFile(cs, dirname, mc2)
	})

	if err := eg.Wait(); err != nil {
		return err
	}

	klog.Infof("Running dyff command")
	out := exec.Command("dyff", "between", getMCFilename(dirname, mc1), getMCFilename(dirname, mc2))
	out.Stdout = os.Stdout
	out.Stderr = os.Stderr

	return out.Run()
}

func getMCAndWriteToFile(cs *framework.ClientSet, dirname, name string) error {
	mc, err := cs.MachineConfigs().Get(context.TODO(), name, metav1.GetOptions{})
	if err != nil {
		return err
	}

	outBytes, err := yaml.Marshal(mc)
	if err != nil {
		return err
	}

	genericized := map[string]interface{}{}

	if err := yaml.Unmarshal(outBytes, &genericized); err != nil {
		return err
	}

	parsedIgnConfig, err := getParsedIgnConfig(mc)
	if err != nil {
		return err
	}

	genericized["spec"].(map[string]interface{})["config"] = parsedIgnConfig

	filename := getMCFilename(dirname, name)

	outBytes, err = yaml.Marshal(genericized)
	if err != nil {
		return err
	}

	if err := os.WriteFile(filename, outBytes, 0o755); err != nil {
		return err
	}

	klog.Infof("Wrote %s", filename)

	return nil
}

func getMCFilename(dirname, mcName string) string {
	return filepath.Join(dirname, fmt.Sprintf("%s.yaml", mcName))
}

func getParsedIgnConfig(mc *mcfgv1.MachineConfig) (*ign3types.Config, error) {
	// Convert the raw Ignition bytes into an Ignition struct.
	ignConfig, err := ctrlcommon.ParseAndConvertConfig(mc.Spec.Config.Raw)
	if err != nil {
		return nil, err
	}

	for i, file := range ignConfig.Storage.Files {
		// Decode each files contents
		decoded, err := ctrlcommon.DecodeIgnitionFileContents(file.Contents.Source, file.Contents.Compression)
		if err != nil {
			return nil, err
		}

		if file.Contents.Source != nil {
			if convertToYAML {
				decoded, err = yaml.JSONToYAML(decoded)
				if err != nil {
					return nil, err
				}
			}

			out := string(decoded)
			ignConfig.Storage.Files[i].Contents.Source = &out
		}
	}

	return &ignConfig, nil
}

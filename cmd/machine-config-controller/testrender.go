package main

import (
	"io/ioutil"
	"fmt"
	"path/filepath"
	"github.com/pkg/errors"
	"os"
	"flag"
	"encoding/json"

	"github.com/golang/glog"
	"github.com/spf13/cobra"
	"github.com/ghodss/yaml"

	ctconfig "github.com/coreos/container-linux-config-transpiler/config"
	cttypes "github.com/coreos/container-linux-config-transpiler/config/types"
	igntypes "github.com/coreos/ignition/config/v2_2/types"

	mtmpl "github.com/openshift/machine-config-operator/pkg/controller/template"
)

var (
	renderCmd = &cobra.Command{
		Use:   "testrender",
		Short: "Given a template directory, generate MachineConfig",
		Long:  "",
		Run:   runRenderCmd,
	}

	renderOpts struct {
		srcDir   string
	}
)

func init() {
	rootCmd.AddCommand(renderCmd)
	renderCmd.PersistentFlags().StringVar(&renderOpts.srcDir, "srcdir", "", "The source directory for templates")
}

func pInt(x int) *int {
	return &x
}

func runRender(dir string) (*igntypes.Config, error) {
	var ctCfg cttypes.Config
	var foundCt, foundInline bool

	ctWalkFn := func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			return nil
		}

		filedata, err := ioutil.ReadFile(path)
		if err != nil {
			return err
		}

		f := new(cttypes.File)
		if err := yaml.Unmarshal([]byte(filedata), f); err != nil {
			return errors.Wrapf(err, "failed to unmarshal file into struct")
		}

		// Add the file to the config
		ctCfg.Storage.Files = append(ctCfg.Storage.Files, *f)

		return nil
	}

	ctPath := filepath.Join(dir, "ct")
	if _, err := os.Stat(ctPath); err == nil {
		err := filepath.Walk(ctPath, ctWalkFn)
		if err != nil {
			return nil, errors.Wrapf(err, "Processing ct/")
		}
		foundCt = true
	}

	inlinePath := filepath.Join(dir, "inline")
	inlineWalkFn := func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			return nil
		}

		filedata, err := ioutil.ReadFile(path)
		if err != nil {
			return err
		}

		relpath, err := filepath.Rel(inlinePath, path)
		if err != nil {
			return err
		}

		f := cttypes.File {
			Filesystem: "root",
			Path: "/" + relpath,
			Mode: pInt(420),
			Contents: cttypes.FileContents{
				Inline: string(filedata),
			},
		}
		// Add the file to the config
		ctCfg.Storage.Files = append(ctCfg.Storage.Files, f)

		return nil
	}

	if _, err := os.Stat(inlinePath); err == nil {
		err := filepath.Walk(inlinePath, inlineWalkFn)
		if err != nil {
			return nil, errors.Wrapf(err, "Processing inline/")
		}
		foundInline = true
	}

	if !(foundCt || foundInline) {
		return nil, fmt.Errorf("No ct/ or inline/ subdirectories found")
	}

	ignCfg, rep := ctconfig.Convert(ctCfg, "", nil)
	if rep.IsFatal() {
		return nil, fmt.Errorf("failed to convert config to Ignition config: %s", rep)
	}
	return &ignCfg, nil
}

func runRenderToStdout(dir string) error {
	cfg, err := runRender(dir)
	if err != nil {
		return errors.Wrapf(err, "render")
	}

	mc := mtmpl.MachineConfigFromIgnConfig("worker", "testmc", cfg)
	json.NewEncoder(os.Stdout).Encode(&mc)
	return nil
}

func runRenderCmd(cmd *cobra.Command, args []string) {
	flag.Set("logtostderr", "true")
	flag.Parse()

	err := runRenderToStdout(renderOpts.srcDir)
	if err != nil {
		glog.Fatal(err)
	}
}

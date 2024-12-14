package legacycmds

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/openshift/machine-config-operator/devex/internal/pkg/utils"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/test/framework"
	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
)

var (
	extractOpts struct {
		poolName      string
		machineConfig string
		targetDir     string
		noConfigMaps  bool
	}
)

func ExtractCommand() *cobra.Command {
	extractCmd := &cobra.Command{
		Use:   "extract",
		Short: "Extracts the Dockerfile and MachineConfig from an on-cluster build",
		Long:  "",
		RunE:  runExtractCmd,
	}

	extractCmd.PersistentFlags().StringVar(&extractOpts.poolName, "pool", DefaultLayeredPoolName, "Pool name to extract")
	extractCmd.PersistentFlags().StringVar(&extractOpts.machineConfig, "machineconfig", "", "MachineConfig name to extract")
	extractCmd.PersistentFlags().StringVar(&extractOpts.targetDir, "dir", "", "Dir to store extract build objects")

	return extractCmd
}

func runExtractCmd(_ *cobra.Command, _ []string) error {
	utils.ParseFlags()

	if extractOpts.poolName != "" && extractOpts.machineConfig != "" {
		return fmt.Errorf("either pool name or MachineConfig must be provided; not both")
	}

	targetDir, err := GetDir(extractOpts.targetDir)
	if err != nil {
		return err
	}

	cs := framework.NewClientSet("")

	if extractOpts.machineConfig != "" {
		return extractBuildObjectsForRenderedMC(cs, extractOpts.machineConfig, targetDir)
	}

	if extractOpts.poolName != "" {
		return extractBuildObjectsForTargetPool(cs, extractOpts.poolName, targetDir)
	}

	return fmt.Errorf("no pool name or MachineConfig name provided")
}

func extractBuildObjectsForTargetPool(cs *framework.ClientSet, targetPool, targetDir string) error {
	mcp, err := cs.MachineConfigPools().Get(context.TODO(), targetPool, metav1.GetOptions{})
	if err != nil {
		return err
	}

	return ExtractBuildObjects(cs, mcp, targetDir)
}

func extractBuildObjectsForRenderedMC(cs *framework.ClientSet, mcName, targetDir string) error {
	ctx := context.Background()

	dockerfileCM, err := cs.CoreV1Interface.ConfigMaps(ctrlcommon.MCONamespace).Get(ctx, "dockerfile-"+mcName, metav1.GetOptions{})
	if err != nil {
		return err
	}

	mcCM, err := cs.CoreV1Interface.ConfigMaps(ctrlcommon.MCONamespace).Get(ctx, "mc-"+mcName, metav1.GetOptions{})
	if err != nil {
		return err
	}

	klog.Infof("Extracted Dockerfile from %q", dockerfileCM.Name)
	klog.Infof("Extracted MachineConfig %s from %q", mcName, mcCM.Name)

	return storeBuildObjectsOnDisk(dockerfileCM.Data["Dockerfile"], mcCM.Data["machineconfig.json.gz"], filepath.Join(targetDir, "build-objects-"+mcName))
}

func storeBuildObjectsOnDisk(dockerfile, machineConfig, targetDir string) error {
	mcDirName := filepath.Join(targetDir, "machineconfig")
	dockerfileName := filepath.Join(targetDir, "Dockerfile")
	mcFilename := filepath.Join(targetDir, "machineconfig.json.gz")

	if err := os.MkdirAll(mcDirName, 0o755); err != nil {
		return err
	}

	if err := os.WriteFile(dockerfileName, []byte(dockerfile), 0o755); err != nil {
		return err
	}

	klog.Infof("Wrote Dockerfile to %s", dockerfileName)

	if err := os.WriteFile(mcFilename, []byte(machineConfig), 0o755); err != nil {
		return err
	}

	klog.Infof("Wrote MachineConfig to %s", mcFilename)

	return nil
}

package main

import (
	"context"
	"os"
	"os/signal"
	"runtime"
	"syscall"

	"github.com/spf13/cobra"
	"k8s.io/component-base/cli"
)

func main() {
	var inputPath string
	var outputPath string
	var workerCount int

	rootCmd := &cobra.Command{
		Use:   "mco-sanitize",
		Short: "Removes MCO sensitive information from a must-gather report",
		Long:  "",
		RunE: func(_ *cobra.Command, _ []string) error {
			ctx, cancelFn := signal.NotifyContext(
				context.Background(),
				os.Interrupt,
				syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT,
			)
			defer cancelFn()
			config, err := GetConfig()
			if err != nil {
				return err
			}
			if err := sanitize(ctx, inputPath, workerCount, config); err != nil {
				return err
			}
			// Encrypted archiving cannot be disabled for now
			// When we validate that the tool works in CI this feature will be disabled by default
			if outputPath != "" {
				if err := Archive(inputPath, outputPath); err != nil {
					return err
				}
			}
			return nil
		},
	}

	rootCmd.PersistentFlags().StringVar(&inputPath, "input", "", "Path to the must-gather directory.")
	rootCmd.PersistentFlags().StringVar(&outputPath, "output", "", "Path to where the tar.gz output should be saved.")
	rootCmd.PersistentFlags().IntVar(&workerCount, "workers", runtime.NumCPU(), "Worker count. Defaults to CPU core count.")
	_ = rootCmd.MarkPersistentFlagRequired("input")

	os.Exit(cli.Run(rootCmd))
}

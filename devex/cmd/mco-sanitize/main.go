package main

import (
	"context"
	"github.com/spf13/cobra"
	"k8s.io/component-base/cli"
	"os"
	"os/signal"
	"runtime"
	"syscall"
)

func main() {
	var inputPath string
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

			return sanitize(ctx, inputPath, workerCount)
		},
	}

	rootCmd.PersistentFlags().StringVar(&inputPath, "input", "", "Path to the must-gather directory.")
	rootCmd.PersistentFlags().IntVar(&workerCount, "workers", runtime.NumCPU(), "Worker count. Defaults to CPU core count.")
	_ = rootCmd.MarkPersistentFlagRequired("input")

	os.Exit(cli.Run(rootCmd))
}

package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"k8s.io/component-base/cli"
)

// defaultBranch is the branch queried on both the MCO and openshift/installer side when
// --branch isn't set.
const defaultBranch = "main"

// runTimeout bounds the whole check: a handful of paginated GitHub API calls plus one `aws`
// CLI subprocess per product spec. Generous headroom over the happy-path cost so a hung network
// call or subprocess can't block forever, without needing per-call timeouts of its own.
const runTimeout = 2 * time.Minute

func main() {
	var (
		region  string
		profile string
		branch  string
		jsonOut bool
	)

	rootCmd := &cobra.Command{
		Use:   "check-aws-marketplace-skew",
		Short: "Checks published AWS Marketplace RHCOS AMIs against the MCO boot-image skew band.",
		RunE: func(_ *cobra.Command, _ []string) error {
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			ctx, cancel := context.WithTimeout(ctx, runTimeout)
			defer cancel()
			return run(ctx, region, profile, branch, jsonOut)
		},
	}

	rootCmd.PersistentFlags().StringVar(&region, "region", "us-east-1", "AWS region to query DescribeImages against. A single region is sufficient: the version signal lives in the AMI Name/Description text, which is consistent across every region a Marketplace listing replicates to.")
	rootCmd.PersistentFlags().StringVar(&profile, "profile", "", "named AWS profile to use (default: whatever the default credential chain resolves, same as the aws CLI)")
	rootCmd.PersistentFlags().StringVar(&branch, "branch", defaultBranch, "release branch to check, applied to both the MCO skew-limit history and the openshift/installer RHCOS ceiling — both fetched live from GitHub, no local checkout of either repo needed")
	rootCmd.PersistentFlags().BoolVar(&jsonOut, "json", false, "emit structured JSON instead of a human-readable table")

	os.Exit(cli.Run(rootCmd))
}

func run(ctx context.Context, region, profile, branch string, jsonOut bool) error {
	floor, err := HistoricalSkewLimits(ctx, branch, time.Now())
	if err != nil {
		return err
	}

	ceilings, err := FetchInstallerCeilings(ctx, branch)
	if err != nil {
		return err
	}

	report := Report{Floor: floor, Ceiling: ceilings}
	for _, product := range allProductSpecs() {
		result, err := CheckProduct(ctx, region, profile, product, floor.RHCOS, ceilings[product.Arch])
		if err != nil {
			return fmt.Errorf("checking product %s (%s): %w", product.Name, product.ID, err)
		}
		report.Results = append(report.Results, result)
	}

	if jsonOut {
		if err := report.WriteJSON(os.Stdout); err != nil {
			return err
		}
	} else if err := report.WriteTable(os.Stdout); err != nil {
		return err
	}

	if report.AnyFailed() {
		failed := 0
		for _, res := range report.Results {
			if !res.Pass {
				failed++
			}
		}
		return fmt.Errorf("%d of %d product codes failed the skew band check", failed, len(report.Results))
	}
	return nil
}

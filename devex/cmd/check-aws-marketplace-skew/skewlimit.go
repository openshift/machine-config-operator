package main

import (
	"context"
	"fmt"
	"regexp"
	"time"

	"k8s.io/klog/v2"
)

// SkewLimits holds the reconstructed value of MCO's boot-image skew-limit constants at a point in time.
type SkewLimits struct {
	RHCOS string // e.g. "9.2" — the operative floor for AMI-token comparison
	OCP   string // e.g. "4.13.0" — surfaced for context/output only
}

const (
	// graceMonths gives Marketplace publishers time to catch up after MCO bumps its skew limit.
	graceMonths = 4
	// skewLimitsPath is relative to the MCO repo root.
	skewLimitsPath = "pkg/controller/common/constants.go"
)

// HistoricalSkewLimits reconstructs the RHCOSVersionBootImageSkewLimit/OCPVersionBootImageSkewLimit
// constants (pkg/controller/common/constants.go) as they stood graceMonths before asOf on branch, by
// querying openshift/machine-config-operator's GitHub history rather than a local checkout. This
// anchors the grace period to when the constants actually held a given value, so a double-bump
// within the window can't reset the clock the way anchoring to "time since last bump" would.
func HistoricalSkewLimits(ctx context.Context, branch string, asOf time.Time) (SkewLimits, error) {
	commits, err := githubCommitsForPath(ctx, "openshift", "machine-config-operator", branch, skewLimitsPath)
	if err != nil {
		return SkewLimits{}, err
	}

	cutoff := asOf.AddDate(0, -graceMonths, 0)
	rev, usedFallback, err := selectHistoricalCommit(commits, cutoff)
	if err != nil {
		return SkewLimits{}, err
	}

	src, err := fetchRawGitHubFile(ctx, "openshift", "machine-config-operator", rev, skewLimitsPath)
	if err != nil {
		return SkewLimits{}, err
	}

	limits, err := parseSkewLimitConstants(string(src))
	if err != nil {
		return SkewLimits{}, fmt.Errorf("skew-limit constants not present in %s as of commit %s (cutoff %s): %w", skewLimitsPath, rev, cutoff.Format(time.RFC3339), err)
	}

	if usedFallback {
		klog.Warningf("no commit predates the %d-month grace window; using the oldest available value of the skew-limit constants (commit %s)", graceMonths, rev)
	}

	return limits, nil
}

// selectHistoricalCommit picks the commit whose state was in effect as of cutoff from commits
// (assumed newest-first, as returned by the GitHub API): the most recent commit older than cutoff,
// or — if none predates the window (the file/branch is younger than graceMonths) — the oldest
// commit available, since there's no earlier data to grant a grace period against.
func selectHistoricalCommit(commits []ghCommit, cutoff time.Time) (sha string, usedFallback bool, err error) {
	if len(commits) == 0 {
		return "", false, fmt.Errorf("no commit history found for %s", skewLimitsPath)
	}
	for _, c := range commits {
		if c.Date.Before(cutoff) {
			return c.SHA, false, nil
		}
	}
	return commits[len(commits)-1].SHA, true, nil
}

var (
	rhcosSkewLimitRe = regexp.MustCompile(`RHCOSVersionBootImageSkewLimit\s*=\s*"([^"]+)"`)
	ocpSkewLimitRe   = regexp.MustCompile(`OCPVersionBootImageSkewLimit\s*=\s*"([^"]+)"`)
)

// parseSkewLimitConstants extracts the two skew-limit string-literal constants from a copy of
// constants.go's source text. Deliberately does not build/vet the historical revision — that's
// slow, fragile (an old revision may not compile standalone against current vendor state), and
// unnecessary for extracting two string literals.
func parseSkewLimitConstants(src string) (SkewLimits, error) {
	rhcosMatch := rhcosSkewLimitRe.FindStringSubmatch(src)
	ocpMatch := ocpSkewLimitRe.FindStringSubmatch(src)
	if rhcosMatch == nil || ocpMatch == nil {
		return SkewLimits{}, fmt.Errorf("could not find both RHCOSVersionBootImageSkewLimit and OCPVersionBootImageSkewLimit constants")
	}
	return SkewLimits{RHCOS: rhcosMatch[1], OCP: ocpMatch[1]}, nil
}

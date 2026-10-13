package template

// gcpPublicHealthCheckSourceRanges are the prober ranges for public GCP.
var gcpPublicHealthCheckSourceRanges = []string{
	"35.191.0.0/16",
	"130.211.0.0/22",
}

// gcdHealthCheckSourceRanges maps a GCD region to its prober ranges. Sourced
// from the "Probe IP ranges" section of each region's
// load-balancing/docs/firewall-rules (u-france-east1: https://documentation.s3ns.fr).
var gcdHealthCheckSourceRanges = map[string][]string{
	"u-germany-northeast1": {
		"34.3.144.0/23",
		"34.3.151.0/26",
		"34.3.151.64/26",
		"136.124.104.0/22",
		"136.124.108.0/22",
	},
	"u-france-east1": {
		"177.222.80.0/23",
		"177.222.87.0/26",
		"177.222.87.64/26",
		"136.124.104.0/22",
		"136.124.108.0/22",
	},
}

// gcpHealthCheckSourceRanges returns the health-check prober source ranges to
// drop for the cluster's region. Only GCD regions have specific ranges.
func gcpHealthCheckSourceRanges(cfg RenderConfig) []string {
	if ps := cfg.Infra.Status.PlatformStatus; ps != nil && ps.GCP != nil {
		if ranges, ok := gcdHealthCheckSourceRanges[ps.GCP.Region]; ok {
			return ranges
		}
	}
	return gcpPublicHealthCheckSourceRanges
}

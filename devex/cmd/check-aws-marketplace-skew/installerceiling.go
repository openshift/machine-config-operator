package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	coreosstream "github.com/coreos/stream-metadata-go/stream"
)

// installerStreamPath is openshift/installer's pinned RHCOS stream metadata — the same file the
// installer itself uses to select RHCOS for a release.
const installerStreamPath = "data/data/coreos/coreos-rhel-10.json"

// marketplaceArchToStreamArch maps a Marketplace product's arch label to the stream metadata's
// architecture key.
var marketplaceArchToStreamArch = map[string]string{
	"x86_64": "x86_64",
	"arm64":  "aarch64",
}

// FetchInstallerCeilings fetches openshift/installer's pinned RHCOS stream metadata for branch
// once and returns the "release" field's major.minor token (e.g. "10.2") for every supported
// Marketplace architecture, keyed by Marketplace arch label — the ceiling of the acceptable skew
// band, since a Marketplace AMI newer than this indicates Marketplace is serving the wrong image
// for this branch. Both archs live in the same stream-metadata file, so this fetches it once
// rather than once per arch.
func FetchInstallerCeilings(ctx context.Context, branch string) (map[string]string, error) {
	body, err := fetchRawGitHubFile(ctx, "openshift", "installer", branch, installerStreamPath)
	if err != nil {
		return nil, err
	}

	ceilings := make(map[string]string, len(marketplaceArchToStreamArch))
	for marketplaceArch, streamArch := range marketplaceArchToStreamArch {
		token, _, err := parseStreamCeiling(body, streamArch)
		if err != nil {
			return nil, fmt.Errorf("branch %s arch %s: %w", branch, marketplaceArch, err)
		}
		ceilings[marketplaceArch] = token
	}
	return ceilings, nil
}

// parseStreamCeiling extracts the aws artifact's release token from raw stream metadata JSON for
// streamArch, split out from FetchInstallerCeilings so it can be tested against a fixture without a
// live network call.
func parseStreamCeiling(body []byte, streamArch string) (token, fullRelease string, err error) {
	var streamData coreosstream.Stream
	if err := json.Unmarshal(body, &streamData); err != nil {
		return "", "", fmt.Errorf("failed to parse stream metadata: %w", err)
	}

	arch, err := streamData.GetArchitecture(streamArch)
	if err != nil {
		return "", "", err
	}

	awsArtifact, ok := arch.Artifacts["aws"]
	if !ok {
		return "", "", fmt.Errorf("stream metadata has no aws artifact for %s", streamArch)
	}

	fullRelease = awsArtifact.Release
	token, err = releaseToken(fullRelease)
	if err != nil {
		return "", "", err
	}
	return token, fullRelease, nil
}

// releaseToken derives the major.minor token from a full RHCOS release string, e.g.
// "10.2.20260423-0" -> "10.2".
func releaseToken(release string) (string, error) {
	parts := strings.SplitN(release, ".", 3)
	if len(parts) < 2 {
		return "", fmt.Errorf("unexpected RHCOS release string format: %q", release)
	}
	return parts[0] + "." + parts[1], nil
}

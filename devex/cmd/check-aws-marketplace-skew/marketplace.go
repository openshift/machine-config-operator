package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"

	bootimagemarketplace "github.com/openshift/machine-config-operator/pkg/controller/bootimage/marketplace"
)

// CLIImage is the subset of `aws ec2 describe-images --output json` fields this tool needs. The
// AWS CLI's JSON output uses the same field names as the API/Go SDK (ImageId, Name, Description,
// CreationDate), so no translation layer is needed beyond ignoring the fields we don't use.
type CLIImage struct {
	ImageID     string `json:"ImageId"`
	Name        string
	Description string
}

type describeImagesOutput struct {
	Images []CLIImage
}

// DescribeMarketplaceAMIs returns every published Marketplace AMI whose name contains productID,
// by shelling out to the aws CLI rather than depending on the AWS Go SDK. This keeps the tool free
// of any AWS SDK dependency — it just needs whatever credentials already make `aws` CLI commands
// work for the engineer running it.
func DescribeMarketplaceAMIs(ctx context.Context, region, profile, productID string) ([]CLIImage, error) {
	args := []string{
		"ec2", "describe-images",
		"--owners", "aws-marketplace",
		"--filters", "Name=name,Values=*" + productID + "*",
		"--region", region,
		"--output", "json",
	}
	if profile != "" {
		args = append(args, "--profile", profile)
	}

	cmd := exec.CommandContext(ctx, "aws", args...)
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("aws %v failed: %w: %s", args, err, exitErr.Stderr)
		}
		return nil, fmt.Errorf("failed to run aws CLI (is it installed and on PATH?): %w", err)
	}

	var parsed describeImagesOutput
	if err := json.Unmarshal(out, &parsed); err != nil {
		return nil, fmt.Errorf("failed to parse aws ec2 describe-images output: %w", err)
	}
	return parsed.Images, nil
}

// tokenInBand reports whether token falls within [floor, ceiling], inclusive.
func tokenInBand(token, floor, ceiling string) bool {
	return bootimagemarketplace.CmpVersionToken(token, floor) >= 0 && bootimagemarketplace.CmpVersionToken(token, ceiling) <= 0
}

// AMIMatch describes the Marketplace AMI, if any, that satisfied a product's band check.
type AMIMatch struct {
	ImageID, Name, Description, Version, Token string
}

// ProductResult is the pass/fail outcome of the band check for a single Marketplace product.
type ProductResult struct {
	ProductID, ProductName string
	Pass                   bool
	MatchedAMI             *AMIMatch
	CandidateCount         int    // how many AMIs were found in total, for FAIL diagnostics
	Reason                 string // set on FAIL or error
}

// CheckProduct is existential, not singular: it enumerates every published Marketplace AMI for
// product and passes if at least one falls within [floor, ceiling]. Marketplace may keep multiple
// AMI versions live at once, so the default/latest one being out of band doesn't mean customers
// have no compliant option.
func CheckProduct(ctx context.Context, region, profile string, product ProductSpec, floor, ceiling string) (ProductResult, error) {
	result := ProductResult{ProductID: product.ID, ProductName: product.Name}

	images, err := DescribeMarketplaceAMIs(ctx, region, profile, product.ID)
	if err != nil {
		return ProductResult{}, err
	}
	result.CandidateCount = len(images)

	var best *AMIMatch
	for _, img := range images {
		fullVersion, token, ok := bootimagemarketplace.ExtractVersionFromDescription(img.Description)
		if !ok {
			continue
		}
		if !tokenInBand(token, floor, ceiling) {
			continue
		}
		if best == nil || bootimagemarketplace.CmpVersionToken(token, best.Token) > 0 {
			best = &AMIMatch{
				ImageID:     img.ImageID,
				Name:        img.Name,
				Description: img.Description,
				Version:     fullVersion,
				Token:       token,
			}
		}
	}

	if best == nil {
		result.Pass = false
		result.Reason = fmt.Sprintf("no published AMI in band [%s, %s] out of %d candidate(s)", floor, ceiling, result.CandidateCount)
		return result, nil
	}

	result.Pass = true
	result.MatchedAMI = best
	return result, nil
}

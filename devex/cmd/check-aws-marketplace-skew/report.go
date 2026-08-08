package main

import (
	"encoding/json"
	"fmt"
	"io"
	"text/tabwriter"
)

// Report is the outcome of a check-aws-marketplace-skew run, across all Marketplace products checked.
type Report struct {
	Floor   SkewLimits        `json:"floor"`
	Ceiling map[string]string `json:"ceiling"` // arch -> token
	Results []ProductResult   `json:"results"`
}

// AnyFailed reports whether any product failed its band check.
func (r Report) AnyFailed() bool {
	for _, res := range r.Results {
		if !res.Pass {
			return true
		}
	}
	return false
}

// WriteTable renders a human-readable summary table.
func (r Report) WriteTable(w io.Writer) error {
	if _, err := fmt.Fprintf(w, "Skew-limit floor: RHCOS %s (OCP %s)\n", r.Floor.RHCOS, r.Floor.OCP); err != nil {
		return err
	}
	for arch, token := range r.Ceiling {
		if _, err := fmt.Fprintf(w, "Installer ceiling (%s): %s\n", arch, token); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintln(w); err != nil {
		return err
	}

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, "PRODUCT\tPRODUCT ID\tRESULT\tMATCHED AMI\tDETAIL"); err != nil {
		return err
	}
	for _, res := range r.Results {
		result := "PASS"
		if !res.Pass {
			result = "FAIL"
		}
		matched := ""
		detail := res.Reason
		if res.MatchedAMI != nil {
			matched = res.MatchedAMI.ImageID
			detail = res.MatchedAMI.Version
		}
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", res.ProductName, res.ProductID, result, matched, detail); err != nil {
			return err
		}
	}
	return tw.Flush()
}

// WriteJSON renders the report as structured JSON, for a future CI wrapper to consume when
// deciding whether to file/update a Jira bug.
func (r Report) WriteJSON(w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

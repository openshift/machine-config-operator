package report

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/openshift/machine-config-operator/devex/cmd/mcdiff/internal/diff"
	"github.com/openshift/machine-config-operator/devex/cmd/mcdiff/internal/scanner"
)

// ScanOptions control how a whole-node scan is printed.
type ScanOptions struct {
	// Format is "text" (default) or "json".
	Format string
	// ShowDiffs includes unified diffs for mismatched files.
	ShowDiffs bool
	// MustGather is the --must-gather path when operating offline.
	MustGather string
}

// WriteScan prints a whole-node scan result.
func WriteScan(w io.Writer, result *scanner.Result, opts ScanOptions) error {
	if result == nil {
		return fmt.Errorf("scan result is nil")
	}
	format := opts.Format
	if format == "" {
		format = "text"
	}
	switch format {
	case "text":
		_, err := io.WriteString(w, formatScanText(result, opts))
		return err
	case "json":
		return json.NewEncoder(w).Encode(toScanJSON(result, opts))
	default:
		return fmt.Errorf("unknown output format %q (want text or json)", format)
	}
}

func formatScanText(result *scanner.Result, opts ScanOptions) string {
	var b strings.Builder
	fmt.Fprintf(&b, "MachineConfig Node Scan\n%s\n\n", separator)
	fmt.Fprintf(&b, "Node:             %s\n", result.Node)
	fmt.Fprintf(&b, "Pool:             %s\n", result.Pool)
	fmt.Fprintf(&b, "Rendered MC:      %s\n", result.Rendered)
	if opts.MustGather != "" {
		fmt.Fprintf(&b, "Archive:          Must-Gather Archive (%s)\n", opts.MustGather)
	}
	fmt.Fprintf(&b, "Scanned Files:    %d\n", result.Scanned)
	fmt.Fprintf(&b, "Matching:         %d\n", result.Matching)
	fmt.Fprintf(&b, "Mismatched:       %d\n", result.Mismatched)
	fmt.Fprintf(&b, "Missing:          %d\n", result.Missing)
	if result.Errors > 0 {
		fmt.Fprintf(&b, "Unreadable:       %d\n", result.Errors)
	}
	fmt.Fprintf(&b, "Status:           %s\n", scanStatusLine(result))

	writeFindingList(&b, "Mismatched Files", result.MismatchedFiles, true, opts.ShowDiffs)
	writeFindingList(&b, "Missing Files", result.MissingFiles, false, false)
	writeErrorList(&b, result.ErrorFiles)
	return b.String()
}

func scanStatusLine(result *scanner.Result) string {
	switch result.Status() {
	case "clean":
		return "CLEAN"
	case "error":
		return fmt.Sprintf("ERRORS (%s)", fileCount(result.Errors, "unreadable"))
	default:
		var parts []string
		if result.Mismatched > 0 {
			parts = append(parts, fileCount(result.Mismatched, "modified"))
		}
		if result.Missing > 0 {
			parts = append(parts, fileCount(result.Missing, "missing"))
		}
		if result.Errors > 0 {
			parts = append(parts, fileCount(result.Errors, "unreadable"))
		}
		return "DRIFT DETECTED (" + strings.Join(parts, ", ") + ")"
	}
}

func fileCount(n int, adjective string) string {
	if n == 1 {
		return fmt.Sprintf("1 file %s", adjective)
	}
	return fmt.Sprintf("%d files %s", n, adjective)
}

func writeFindingList(b *strings.Builder, title string, findings []scanner.Finding, withSizes, showDiffs bool) {
	if len(findings) == 0 {
		return
	}
	fmt.Fprintf(b, "\n%s:\n", title)
	for i, f := range findings {
		fmt.Fprintf(b, "%d. %s\n", i+1, f.Path)
		if !withSizes {
			fmt.Fprintf(b, "   Status: MISSING ON NODE\n")
		}
		if withSizes {
			fmt.Fprintf(b, "   Expected: %d bytes | Actual: %d bytes\n", f.ExpectedSize, f.ActualSize)
			if f.ModeMismatch {
				fmt.Fprintf(b, "   Mode: expected %s | actual %s\n", formatModeOctal(diff.EffectiveMode(f.ExpectedMode)), formatMode(f.ActualMode))
			}
		}
		last := f.LastWriter
		if last == "" {
			last = "(unknown)"
		}
		fmt.Fprintf(b, "   Last Writer: %s\n", last)
		if showDiffs && f.Diff != "" {
			fmt.Fprintf(b, "\n   Unified diff:\n")
			for _, line := range strings.Split(strings.TrimSuffix(f.Diff, "\n"), "\n") {
				fmt.Fprintf(b, "   %s\n", line)
			}
		}
		if i < len(findings)-1 {
			fmt.Fprintf(b, "\n")
		}
	}
}

func writeErrorList(b *strings.Builder, findings []scanner.Finding) {
	if len(findings) == 0 {
		return
	}
	fmt.Fprintf(b, "\nUnreadable Files:\n")
	for i, f := range findings {
		fmt.Fprintf(b, "%d. %s\n", i+1, f.Path)
		last := f.LastWriter
		if last == "" {
			last = "(unknown)"
		}
		fmt.Fprintf(b, "   Last Writer: %s\n", last)
		fmt.Fprintf(b, "   Error: %s\n", f.Error)
		if i < len(findings)-1 {
			fmt.Fprintf(b, "\n")
		}
	}
}

type scanJSON struct {
	Node                  string        `json:"node"`
	Pool                  string        `json:"pool"`
	RenderedMachineConfig string        `json:"renderedMachineConfig"`
	Configuration         string        `json:"configuration"`
	ConfigurationSource   string        `json:"configurationSource"`
	ScannedFiles          int           `json:"scannedFiles"`
	Matching              int           `json:"matching"`
	Mismatched            int           `json:"mismatched"`
	Missing               int           `json:"missing"`
	Unreadable            int           `json:"unreadable"`
	Status                string        `json:"status"`
	MismatchedFiles       []findingJSON `json:"mismatchedFiles"`
	MissingFiles          []findingJSON `json:"missingFiles"`
	UnreadableFiles       []findingJSON `json:"unreadableFiles,omitempty"`
	MustGatherDir         string        `json:"mustGatherDir,omitempty"`
}

type findingJSON struct {
	Path         string `json:"path"`
	ExpectedSize int    `json:"expectedSize"`
	ActualSize   *int   `json:"actualSize,omitempty"`
	ExpectedMode *int   `json:"expectedMode,omitempty"`
	ActualMode   *int   `json:"actualMode,omitempty"`
	ModeMismatch bool   `json:"modeMismatch,omitempty"`
	LastWriter   string `json:"lastWriter"`
	Diff         string `json:"diff,omitempty"`
	Error        string `json:"error,omitempty"`
}

func toScanJSON(result *scanner.Result, opts ScanOptions) scanJSON {
	out := scanJSON{
		Node:                  result.Node,
		Pool:                  result.Pool,
		RenderedMachineConfig: result.Rendered,
		Configuration:         result.Origin.Kind,
		ConfigurationSource:   result.Origin.Source,
		ScannedFiles:          result.Scanned,
		Matching:              result.Matching,
		Mismatched:            result.Mismatched,
		Missing:               result.Missing,
		Unreadable:            result.Errors,
		Status:                result.Status(),
		MismatchedFiles:       findingJSONList(result.MismatchedFiles, true, opts.ShowDiffs),
		MissingFiles:          findingJSONList(result.MissingFiles, false, false),
		MustGatherDir:         opts.MustGather,
	}
	if out.Configuration == "" {
		out.Configuration = "current"
	}
	if out.ConfigurationSource == "" {
		out.ConfigurationSource = "MCP status.configuration"
	}
	if out.MismatchedFiles == nil {
		out.MismatchedFiles = []findingJSON{}
	}
	if out.MissingFiles == nil {
		out.MissingFiles = []findingJSON{}
	}
	if result.Errors > 0 {
		out.UnreadableFiles = findingJSONList(result.ErrorFiles, false, false)
	}
	return out
}

func findingJSONList(findings []scanner.Finding, withActual, showDiffs bool) []findingJSON {
	if len(findings) == 0 {
		return []findingJSON{}
	}
	out := make([]findingJSON, 0, len(findings))
	for _, f := range findings {
		item := findingJSON{
			Path:         f.Path,
			ExpectedSize: f.ExpectedSize,
			ExpectedMode: f.ExpectedMode,
			LastWriter:   f.LastWriter,
			Error:        f.Error,
			ModeMismatch: f.ModeMismatch,
		}
		if withActual {
			size := f.ActualSize
			item.ActualSize = &size
			item.ActualMode = f.ActualMode
		}
		if showDiffs {
			item.Diff = f.Diff
		}
		out = append(out, item)
	}
	return out
}

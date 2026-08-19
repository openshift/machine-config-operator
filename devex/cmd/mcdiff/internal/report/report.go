package report

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/openshift/machine-config-operator/devex/cmd/mcdiff/internal/cluster"
	"github.com/openshift/machine-config-operator/devex/cmd/mcdiff/internal/diff"
)

const separator = "────────────────────────────────────────"

// Options control how a PoolFile is printed.
type Options struct {
	// ShowContent includes expected file bytes. Default is metadata only.
	ShowContent bool
	// Format is "text" (default) or "json".
	Format string
	// FromFile is the local path passed to --from-file. Empty means no comparison.
	FromFile string
	// Node is the node name passed to --node. Empty means no live-node comparison.
	Node string
	// Actual is the compared file bytes when FromFile or Node is set and the file exists.
	Actual []byte
	// ActualMissing is true when --node was set and the path does not exist on the node.
	ActualMissing bool
	// Diff is the expected-vs-actual comparison. Nil when there is no comparison
	// (no --from-file/--node, unmanaged path, or node file missing).
	Diff *diff.Result
	// MustGather is the --must-gather path when operating offline.
	MustGather string
}

// Write prints pf according to opts.
func Write(w io.Writer, pf *cluster.PoolFile, opts Options) error {
	if pf == nil {
		return fmt.Errorf("pool file result is nil")
	}
	format := opts.Format
	if format == "" {
		format = "text"
	}
	switch format {
	case "text":
		_, err := io.WriteString(w, formatText(pf, opts))
		return err
	case "json":
		return json.NewEncoder(w).Encode(toJSON(pf, opts))
	default:
		return fmt.Errorf("unknown output format %q (want text or json)", format)
	}
}

func formatText(pf *cluster.PoolFile, opts Options) string {
	var b strings.Builder
	fmt.Fprintf(&b, "MachineConfig File\n%s\n\n", separator)
	fmt.Fprintf(&b, "Pool:             %s\n", poolName(pf))
	fmt.Fprintf(&b, "Configuration:    %s\n", originKind(pf))
	fmt.Fprintf(&b, "Source:           %s\n", originSource(pf))
	if opts.MustGather != "" {
		fmt.Fprintf(&b, "Archive:          Must-Gather Archive (%s)\n", opts.MustGather)
	}
	fmt.Fprintf(&b, "Rendered MC:      %s\n\n", renderedName(pf))
	fmt.Fprintf(&b, "File:             %s\n", pf.Path)

	if !pf.Found {
		fmt.Fprintf(&b, "Exists:           no\n\n")
		fmt.Fprintf(&b, "This path is not managed by the rendered MachineConfig.\n")
		writeAttribution(&b, pf)
		writeUnmanagedActual(&b, opts)
		return b.String()
	}

	fmt.Fprintf(&b, "Exists:           yes\n")
	fmt.Fprintf(&b, "Mode:             %s\n", formatMode(pf.Mode))
	fmt.Fprintf(&b, "Expected size:    %d bytes\n", len(pf.Expected))
	writeAttribution(&b, pf)
	writeComparison(&b, opts)

	if compared(opts) && !opts.ShowContent {
		return b.String()
	}
	if !opts.ShowContent {
		fmt.Fprintf(&b, "\nExpected content: omitted (pass --show-content to print)\n")
		return b.String()
	}

	fmt.Fprintf(&b, "\nExpected content:\n%s\n", separator)
	if len(pf.Expected) == 0 {
		fmt.Fprintf(&b, "<empty>\n")
	} else {
		b.Write(pf.Expected)
		if pf.Expected[len(pf.Expected)-1] != '\n' {
			b.WriteByte('\n')
		}
	}
	fmt.Fprintf(&b, "%s\n", separator)
	return b.String()
}

func compared(opts Options) bool {
	return opts.FromFile != "" || opts.Node != ""
}

func writeUnmanagedActual(b *strings.Builder, opts Options) {
	if !compared(opts) {
		return
	}
	fmt.Fprintf(b, "\n")
	switch {
	case opts.Node != "" && opts.ActualMissing:
		fmt.Fprintf(b, "Node:             %s\n", opts.Node)
		fmt.Fprintf(b, "Node file:        MISSING ON NODE\n")
	case opts.Node != "":
		fmt.Fprintf(b, "Node:             %s\n", opts.Node)
		fmt.Fprintf(b, "Node file:        exists (%d bytes)\n", len(opts.Actual))
	default:
		fmt.Fprintf(b, "Local file:       %s (%d bytes)\n", opts.FromFile, len(opts.Actual))
	}
	fmt.Fprintf(b, "No content comparison was performed because this path is not managed by the rendered MachineConfig.\n")
}

func writeComparison(b *strings.Builder, opts Options) {
	if !compared(opts) {
		return
	}
	fmt.Fprintf(b, "\n")
	if opts.Node != "" {
		fmt.Fprintf(b, "Node:             %s\n", opts.Node)
	}
	if opts.ActualMissing {
		fmt.Fprintf(b, "Node file:        MISSING ON NODE\n")
		fmt.Fprintf(b, "\nFile exists in rendered MC, but is MISSING ON NODE %s.\n", opts.Node)
		return
	}
	if opts.Diff == nil {
		return
	}
	d := opts.Diff
	fmt.Fprintf(b, "Comparison:       %s\n", comparisonLabel(d))
	if opts.FromFile != "" {
		fmt.Fprintf(b, "From file:        %s\n", opts.FromFile)
	}
	fmt.Fprintf(b, "Expected size:    %d bytes\n", d.ExpectedSize)
	fmt.Fprintf(b, "Actual size:      %d bytes\n", d.ActualSize)
	if d.ExpectedSize != d.ActualSize {
		fmt.Fprintf(b, "Size:             expected %d bytes, got %d bytes\n", d.ExpectedSize, d.ActualSize)
	}
	writeModeDelta(b, d)
	if d.Match || d.UnifiedDiff == "" {
		return
	}
	fmt.Fprintf(b, "\nUnified diff:\n%s\n", separator)
	b.WriteString(d.UnifiedDiff)
	if !strings.HasSuffix(d.UnifiedDiff, "\n") {
		b.WriteByte('\n')
	}
	fmt.Fprintf(b, "%s\n", separator)
}

func writeAttribution(b *strings.Builder, pf *cluster.PoolFile) {
	fmt.Fprintf(b, "\n")
	if pf.AttributionErr != nil {
		fmt.Fprintf(b, "Attribution:      unavailable\n")
		fmt.Fprintf(b, "Reason:           %s\n", pf.AttributionErr.Error())
		return
	}
	writers := pf.WriterNames()
	if len(writers) == 0 {
		fmt.Fprintf(b, "Writers:          (none)\n")
		fmt.Fprintf(b, "Last writer:      (none)\n")
		return
	}
	fmt.Fprintf(b, "Writers:\n")
	for _, name := range writers {
		fmt.Fprintf(b, "  %s\n", name)
	}
	fmt.Fprintf(b, "\nLast writer:\n  %s\n", pf.LastWriterName())
}

func comparisonLabel(d *diff.Result) string {
	switch {
	case d.Match && d.ModeMatch:
		return "MATCH"
	case d.Match && !d.ModeMatch:
		return "MODE MISMATCH"
	case !d.Match && d.ModeMatch:
		return "CONTENT MISMATCH"
	default:
		return "CONTENT AND MODE MISMATCH"
	}
}

func writeModeDelta(b *strings.Builder, d *diff.Result) {
	if d == nil || d.ModeMatch {
		return
	}
	fmt.Fprintf(b, "Mode:             expected %s, actual %s\n", formatModeOctal(diff.EffectiveMode(d.ExpectedMode)), formatMode(d.ActualMode))
}

func formatModeOctal(mode int) string {
	return fmt.Sprintf("%#o", mode)
}

func formatMode(mode *int) string {
	if mode == nil {
		return "unspecified"
	}
	return fmt.Sprintf("%#o", *mode)
}

func poolName(pf *cluster.PoolFile) string {
	if pf.Pool == nil {
		return ""
	}
	return pf.Pool.Name
}

func renderedName(pf *cluster.PoolFile) string {
	if pf.Rendered == nil {
		return ""
	}
	return pf.Rendered.Name
}

func originKind(pf *cluster.PoolFile) string {
	if pf.Origin.Kind == "" {
		return cluster.ConfigurationCurrent
	}
	return pf.Origin.Kind
}

func originSource(pf *cluster.PoolFile) string {
	if pf.Origin.Source == "" {
		return "MCP status.configuration"
	}
	return pf.Origin.Source
}

type fileJSON struct {
	Pool                  string   `json:"pool"`
	Configuration         string   `json:"configuration"`
	ConfigurationSource   string   `json:"configurationSource"`
	RenderedMachineConfig string   `json:"renderedMachineConfig"`
	Path                  string   `json:"path"`
	Found                 bool     `json:"found"`
	Mode                  *int     `json:"mode,omitempty"`
	ExpectedSize          int      `json:"expectedSize"`
	Writers               []string `json:"writers"`
	LastWriter            string   `json:"lastWriter"`
	AttributionAvailable  bool     `json:"attributionAvailable"`
	AttributionError      string   `json:"attributionError,omitempty"`
	ExpectedContent       string   `json:"expectedContent,omitempty"`
	FromFile              string   `json:"fromFile,omitempty"`
	Node                  string   `json:"node,omitempty"`
	NodeFileFound         *bool    `json:"nodeFileFound,omitempty"`
	Match                 *bool    `json:"match,omitempty"`
	ModeMatch             *bool    `json:"modeMatch,omitempty"`
	ActualMode            *int     `json:"actualMode,omitempty"`
	ActualSize            *int     `json:"actualSize,omitempty"`
	Diff                  string   `json:"diff,omitempty"`
	MustGatherDir         string   `json:"mustGatherDir,omitempty"`
}

func toJSON(pf *cluster.PoolFile, opts Options) fileJSON {
	out := fileJSON{
		Pool:                  poolName(pf),
		Configuration:         originKind(pf),
		ConfigurationSource:   originSource(pf),
		RenderedMachineConfig: renderedName(pf),
		Path:                  pf.Path,
		Found:                 pf.Found,
		Mode:                  pf.Mode,
		ExpectedSize:          len(pf.Expected),
		Writers:               pf.WriterNames(),
		LastWriter:            pf.LastWriterName(),
		AttributionAvailable:  pf.Attribution != nil && pf.AttributionErr == nil,
		MustGatherDir:         opts.MustGather,
	}
	if out.Writers == nil {
		out.Writers = []string{}
	}
	if pf.AttributionErr != nil {
		out.AttributionError = pf.AttributionErr.Error()
	}
	if opts.ShowContent && pf.Found {
		out.ExpectedContent = string(pf.Expected)
	}
	if opts.FromFile != "" {
		out.FromFile = opts.FromFile
		size := len(opts.Actual)
		out.ActualSize = &size
		attachDiffJSON(&out, opts.Diff)
	}
	if opts.Node != "" {
		out.Node = opts.Node
		found := !opts.ActualMissing
		out.NodeFileFound = &found
		if !opts.ActualMissing {
			size := len(opts.Actual)
			out.ActualSize = &size
		}
		attachDiffJSON(&out, opts.Diff)
	}
	return out
}

func attachDiffJSON(out *fileJSON, d *diff.Result) {
	if d == nil {
		return
	}
	match := d.Match
	out.Match = &match
	out.Diff = d.UnifiedDiff
	if d.ActualMode != nil || !d.ModeMatch {
		modeMatch := d.ModeMatch
		out.ModeMatch = &modeMatch
		out.ActualMode = d.ActualMode
	}
}

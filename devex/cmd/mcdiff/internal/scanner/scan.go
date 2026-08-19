package scanner

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/openshift/machine-config-operator/devex/cmd/mcdiff/internal/attribution"
	"github.com/openshift/machine-config-operator/devex/cmd/mcdiff/internal/cluster"
	"github.com/openshift/machine-config-operator/devex/cmd/mcdiff/internal/diff"
	"github.com/openshift/machine-config-operator/devex/cmd/mcdiff/internal/ignition"
	"github.com/openshift/machine-config-operator/devex/cmd/mcdiff/internal/node"
)

// Options control pool selection for a whole-node scan.
type Options struct {
	// Pool overrides automatic pool detection from the node's labels.
	Pool string
}

// Result is the aggregate of comparing every Ignition file in the rendered
// MachineConfig against the node's on-disk copy.
type Result struct {
	Node     string
	Pool     string
	Rendered string
	Origin   cluster.ConfigurationOrigin

	Scanned    int
	Matching   int
	Mismatched int
	Missing    int
	Errors     int

	MismatchedFiles []Finding
	MissingFiles    []Finding
	ErrorFiles      []Finding
}

// Status is "clean" when every managed file matches, "drift" when any file
// mismatches or is missing, and "error" when the only problems are unreadable files.
func (r *Result) Status() string {
	if r == nil {
		return "clean"
	}
	if r.Mismatched > 0 || r.Missing > 0 {
		return "drift"
	}
	if r.Errors > 0 {
		return "error"
	}
	return "clean"
}

// Finding is one managed path that did not match the rendered MachineConfig.
type Finding struct {
	Path         string
	ExpectedSize int
	ActualSize   int
	ExpectedMode *int
	ActualMode   *int
	ModeMismatch bool
	LastWriter   string
	Diff         string
	Error        string
}

// Scan enumerates every file in the node's rendered MachineConfig and compares
// each against the on-disk copy from reader.
func Scan(ctx context.Context, g cluster.Getter, nodes node.Getter, reader node.Reader, nodeName string, opts Options) (*Result, error) {
	if g == nil {
		return nil, fmt.Errorf("getter is nil")
	}
	if reader == nil {
		return nil, fmt.Errorf("node reader is not configured")
	}
	if nodeName == "" {
		return nil, fmt.Errorf("node name must not be empty")
	}

	poolName, err := resolvePoolName(ctx, g, nodes, nodeName, opts.Pool)
	if err != nil {
		return nil, err
	}

	rp, err := cluster.LoadRenderedPool(ctx, g, poolName)
	if err != nil {
		return nil, err
	}

	files, err := ignition.ExtractAll(rp.Rendered)
	if err != nil {
		return nil, err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })

	out := &Result{
		Node:     nodeName,
		Pool:     poolName,
		Rendered: rp.Rendered.Name,
		Origin:   rp.Origin,
		Scanned:  len(files),
	}

	for _, f := range files {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		kind, finding, err := scanFile(ctx, rp, reader, nodeName, f)
		if err != nil {
			return nil, err
		}
		switch kind {
		case findingMatch:
			out.Matching++
		case findingMismatch:
			out.Mismatched++
			out.MismatchedFiles = append(out.MismatchedFiles, finding)
		case findingMissing:
			out.Missing++
			out.MissingFiles = append(out.MissingFiles, finding)
		case findingError:
			out.Errors++
			out.ErrorFiles = append(out.ErrorFiles, finding)
		}
	}
	return out, nil
}

type findingKind int

const (
	findingMatch findingKind = iota
	findingMismatch
	findingMissing
	findingError
)

func scanFile(ctx context.Context, rp *cluster.RenderedPool, reader node.Reader, nodeName string, f ignition.File) (findingKind, Finding, error) {
	lastWriter := lastWriterFor(rp, f.Path)

	if f.Err != nil {
		return findingError, Finding{Path: f.Path, LastWriter: lastWriter, Error: f.Err.Error()}, nil
	}

	actual, actualMode, err := reader.ReadFile(ctx, nodeName, f.Path)
	if err != nil {
		if errors.Is(err, node.ErrFileNotFound) {
			return findingMissing, Finding{
				Path:         f.Path,
				ExpectedSize: len(f.Contents),
				ExpectedMode: copyMode(f.Mode),
				LastWriter:   lastWriter,
			}, nil
		}
		if errors.Is(err, node.ErrNodeNotFound) || errors.Is(err, node.ErrMCDUnavailable) {
			return 0, Finding{}, fmt.Errorf("failed to read files from node %q: %w", nodeName, err)
		}
		return findingError, Finding{Path: f.Path, LastWriter: lastWriter, Error: err.Error()}, nil
	}

	cmp := diff.WithModes(diff.Compare(f.Contents, actual, f.Path, "node:"+nodeName), f.Mode, actualMode)
	if cmp.Match && cmp.ModeMatch {
		return findingMatch, Finding{}, nil
	}
	return findingMismatch, Finding{
		Path:         f.Path,
		ExpectedSize: cmp.ExpectedSize,
		ActualSize:   cmp.ActualSize,
		ExpectedMode: cmp.ExpectedMode,
		ActualMode:   cmp.ActualMode,
		ModeMismatch: !cmp.ModeMatch,
		LastWriter:   lastWriter,
		Diff:         cmp.UnifiedDiff,
	}, nil
}

func copyMode(mode *int) *int {
	if mode == nil {
		return nil
	}
	copied := *mode
	return &copied
}

func lastWriterFor(rp *cluster.RenderedPool, path string) string {
	if rp == nil || rp.AttributionErr != nil {
		return ""
	}
	attr, err := attribution.Attribute(path, rp.Sources)
	if err != nil || attr == nil || attr.LastWriter == nil {
		return ""
	}
	return attr.LastWriter.MachineConfigName
}

func resolvePoolName(ctx context.Context, g cluster.Getter, nodes node.Getter, nodeName, poolOverride string) (string, error) {
	if poolOverride != "" {
		return poolOverride, nil
	}
	if nodes == nil {
		return "", fmt.Errorf("cannot detect MachineConfigPool for node %q; pass --pool", nodeName)
	}
	n, err := nodes.GetNode(ctx, nodeName)
	if err != nil {
		return "", err
	}
	pools, err := g.ListMachineConfigPools(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to list MachineConfigPools: %w", err)
	}
	pool, err := ResolvePrimaryPool(n, pools)
	if err != nil {
		return "", err
	}
	return pool.Name, nil
}

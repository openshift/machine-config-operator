package cluster

import (
	"context"
	"errors"
	"fmt"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	"github.com/openshift/machine-config-operator/devex/cmd/mcdiff/internal/attribution"
	"github.com/openshift/machine-config-operator/devex/cmd/mcdiff/internal/ignition"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

const (
	// ConfigurationCurrent is MCP status.configuration (applied to the pool).
	ConfigurationCurrent = "current"
	// ConfigurationTarget is MCP spec.configuration (desired/target).
	ConfigurationTarget = "target"
)

// ConfigurationOrigin records whether LoadPoolFile used status or spec.
type ConfigurationOrigin struct {
	// Kind is ConfigurationCurrent or ConfigurationTarget.
	Kind string
	// Source is the MCP field path, e.g. "MCP status.configuration".
	Source string
}

// PoolFile is the rendered-MC view of one Ignition path for a pool, plus
// optional last-writer attribution from the pool's configuration.source list.
type PoolFile struct {
	Pool     *mcfgv1.MachineConfigPool
	Rendered *mcfgv1.MachineConfig
	Origin   ConfigurationOrigin
	Path     string
	// Expected is the decoded file from Rendered. Nil only when Found is false.
	Expected []byte
	// Found is true when Path exists on the rendered MachineConfig, including
	// when Expected is empty.
	Found bool
	Mode  *int

	Attribution    *attribution.Result
	AttributionErr error
}

// WriterNames returns contributing MachineConfig names in merge order.
func (p *PoolFile) WriterNames() []string {
	if p == nil || p.Attribution == nil {
		return nil
	}
	names := make([]string, 0, len(p.Attribution.Writers))
	for _, w := range p.Attribution.Writers {
		names = append(names, w.MachineConfigName)
	}
	return names
}

// LastWriterName returns the last-writer MachineConfig name, or empty.
func (p *PoolFile) LastWriterName() string {
	if p == nil || p.Attribution == nil || p.Attribution.LastWriter == nil {
		return ""
	}
	return p.Attribution.LastWriter.MachineConfigName
}

// RenderedPool is a pool's rendered MachineConfig plus the source fragments
// used for last-writer attribution. Expected bytes always come from Rendered.
type RenderedPool struct {
	Pool     *mcfgv1.MachineConfigPool
	Rendered *mcfgv1.MachineConfig
	Origin   ConfigurationOrigin
	// Sources are configuration.source MachineConfigs, excluding the rendered object.
	// Nil when AttributionErr is set.
	Sources        []*mcfgv1.MachineConfig
	AttributionErr error
}

// LoadRenderedPool resolves poolName's rendered MachineConfig and loads the
// source fragments named in the same configuration object.
//
// Status.configuration is used when it has a name (applied/current). Otherwise
// spec.configuration is used (desired/target). Origin records which was chosen.
func LoadRenderedPool(ctx context.Context, g Getter, poolName string) (*RenderedPool, error) {
	if g == nil {
		return nil, fmt.Errorf("getter is nil")
	}
	if poolName == "" {
		return nil, fmt.Errorf("pool name must not be empty")
	}

	pool, err := g.GetMachineConfigPool(ctx, poolName)
	if err != nil {
		return nil, err
	}

	renderedName, sourceRefs, err := renderedConfiguration(pool)
	if err != nil {
		return nil, wrapNoRendered(poolName)
	}

	rendered, err := g.GetMachineConfig(ctx, renderedName)
	if err != nil {
		if apierrors.IsNotFound(err) || errors.Is(err, ErrRenderedNotFound) {
			return nil, wrapRenderedNotFound(poolName, renderedName, err)
		}
		return nil, fmt.Errorf("failed to resolve rendered MachineConfig %q for pool %q: %w", renderedName, poolName, err)
	}

	out := &RenderedPool{
		Pool:     pool,
		Rendered: rendered,
		Origin:   originFromPool(pool),
	}

	sources, missing, getErr := loadSourceMachineConfigs(ctx, g, renderedName, sourceRefs)
	if getErr != nil {
		out.AttributionErr = wrapSourceUnavailable(poolName, missing, getErr)
		return out, nil
	}
	out.Sources = sources
	return out, nil
}

// LoadPoolFile resolves poolName's rendered MachineConfig, decodes path from
// that object, and attributes the path across configuration.source.
//
// Status.configuration is used when it has a name (applied/current). Otherwise
// spec.configuration is used (desired/target). Origin records which was chosen.
//
// Expected bytes always come from the rendered MachineConfig, never from a
// client-side re-merge of source fragments.
func LoadPoolFile(ctx context.Context, g Getter, poolName, path string) (*PoolFile, error) {
	if g == nil {
		return nil, fmt.Errorf("getter is nil")
	}
	if poolName == "" {
		return nil, fmt.Errorf("pool name must not be empty")
	}
	if path == "" {
		return nil, fmt.Errorf("path must not be empty")
	}

	rp, err := LoadRenderedPool(ctx, g, poolName)
	if err != nil {
		return nil, err
	}

	extracted, err := ignition.ExtractFile(rp.Rendered, path)
	if err != nil {
		return nil, err
	}

	out := &PoolFile{
		Pool:     rp.Pool,
		Rendered: rp.Rendered,
		Origin:   rp.Origin,
		Path:     path,
		Expected: extracted.Contents,
		Found:    extracted.Found,
		Mode:     extracted.Mode,
	}

	if rp.AttributionErr != nil {
		out.AttributionErr = rp.AttributionErr
		return out, nil
	}

	attr, err := attribution.Attribute(path, rp.Sources)
	if err != nil {
		out.AttributionErr = fmt.Errorf("failed to attribute file %q for pool %q: %w", path, poolName, err)
		return out, nil
	}
	out.Attribution = attr
	return out, nil
}

// renderedConfiguration returns the rendered MC name and the source refs that
// generated it. Status is the current applied configuration; spec is the
// targeted configuration and is used only when status has no name yet.
func renderedConfiguration(pool *mcfgv1.MachineConfigPool) (string, []corev1.ObjectReference, error) {
	if pool.Status.Configuration.Name != "" {
		return pool.Status.Configuration.Name, pool.Status.Configuration.Source, nil
	}
	if pool.Spec.Configuration.Name != "" {
		return pool.Spec.Configuration.Name, pool.Spec.Configuration.Source, nil
	}
	return "", nil, ErrNoRenderedConfiguration
}

func originFromPool(pool *mcfgv1.MachineConfigPool) ConfigurationOrigin {
	if pool.Status.Configuration.Name != "" {
		return ConfigurationOrigin{Kind: ConfigurationCurrent, Source: "MCP status.configuration"}
	}
	return ConfigurationOrigin{Kind: ConfigurationTarget, Source: "MCP spec.configuration"}
}

func loadSourceMachineConfigs(ctx context.Context, g Getter, renderedName string, refs []corev1.ObjectReference) ([]*mcfgv1.MachineConfig, []string, error) {
	var (
		sources []*mcfgv1.MachineConfig
		missing []string
		first   error
	)
	for _, ref := range refs {
		if ref.Name == "" || ref.Name == renderedName {
			continue
		}
		mc, err := g.GetMachineConfig(ctx, ref.Name)
		if err != nil {
			if first == nil {
				first = err
			}
			missing = append(missing, ref.Name)
			continue
		}
		sources = append(sources, mc)
	}
	if first != nil {
		return nil, missing, first
	}
	return sources, nil, nil
}

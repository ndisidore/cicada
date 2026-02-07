// Package builder converts validated pipelines into BuildKit LLB definitions.
package builder

import (
	"context"
	"fmt"
	"strings"

	"github.com/moby/buildkit/client/llb"

	"github.com/ndisidore/ciro/pkg/pipeline"
)

// Result holds the LLB definitions for a pipeline, one per step in topological order.
type Result struct {
	// Definitions contains one LLB definition per step, ordered by dependency.
	Definitions []*llb.Definition
	// StepNames maps each definition index to its step name.
	StepNames []string
}

// Build converts a validated pipeline to BuildKit LLB definitions.
// Each step produces its own definition; dependency ordering is returned via
// topological sort order so the runner can execute them sequentially.
func Build(ctx context.Context, p pipeline.Pipeline) (Result, error) {
	order := p.TopoSort()
	result := Result{
		Definitions: make([]*llb.Definition, 0, len(order)),
		StepNames:   make([]string, 0, len(order)),
	}

	for _, idx := range order {
		step := &p.Steps[idx]
		def, err := buildStep(ctx, step)
		if err != nil {
			return Result{}, fmt.Errorf("building step %q: %w", step.Name, err)
		}
		result.Definitions = append(result.Definitions, def)
		result.StepNames = append(result.StepNames, step.Name)
	}

	return result, nil
}

func buildStep(ctx context.Context, step *pipeline.Step) (*llb.Definition, error) {
	st := llb.Image(step.Image)

	if step.Workdir != "" {
		st = st.Dir(step.Workdir)
	}

	cmd := strings.Join(step.Run, " && ")
	runOpts := []llb.RunOption{
		llb.Args([]string{"/bin/sh", "-c", cmd}),
		llb.WithCustomName(step.Name),
	}

	for _, m := range step.Mounts {
		runOpts = append(runOpts, llb.AddMount(
			m.Target,
			llb.Local("context"),
			llb.SourcePath(m.Source),
			llb.Readonly,
		))
	}

	for _, c := range step.Caches {
		runOpts = append(runOpts, llb.AddMount(
			c.Target,
			llb.Scratch(),
			llb.AsPersistentCacheDir(c.ID, llb.CacheMountShared),
		))
	}

	def, err := st.Run(runOpts...).Root().Marshal(ctx)
	if err != nil {
		return nil, fmt.Errorf("marshaling: %w", err)
	}
	return def, nil
}

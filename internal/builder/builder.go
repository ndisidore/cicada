// Package builder converts validated pipelines into BuildKit LLB definitions.
package builder

import (
	"context"
	"fmt"
	"strings"

	"github.com/containerd/platforms"
	"github.com/moby/buildkit/client/llb"

	"github.com/ndisidore/cicada/pkg/pipeline"
)

// Result holds the LLB definitions for a pipeline, one per step in topological order.
type Result struct {
	// Definitions contains one LLB definition per step, ordered by dependency.
	Definitions []*llb.Definition
	// StepNames maps each definition index to its step name.
	StepNames []string
}

// BuildOpts configures the LLB build process.
type BuildOpts struct {
	// NoCache disables BuildKit cache for all operations when true.
	NoCache bool
	// ExcludePatterns are glob patterns to exclude from local context mounts.
	ExcludePatterns []string
	// MetaResolver resolves OCI image config (ENV, WORKDIR, platform) at build time.
	MetaResolver llb.ImageMetaResolver
}

// stepOpts holds pre-computed LLB options applied to every step.
type stepOpts struct {
	imgOpts         []llb.ImageOption
	runOpts         []llb.RunOption
	excludePatterns []string
}

// Build converts a pipeline to BuildKit LLB definitions.
// It reuses a cached topological order when available, otherwise validates first.
func Build(ctx context.Context, p pipeline.Pipeline, opts BuildOpts) (Result, error) {
	order := p.TopoOrder
	if !validOrder(order, len(p.Steps)) {
		var err error
		order, err = p.Validate()
		if err != nil {
			return Result{}, fmt.Errorf("validating pipeline: %w", err)
		}
	}

	so := stepOpts{excludePatterns: opts.ExcludePatterns}
	if opts.MetaResolver != nil {
		so.imgOpts = append(so.imgOpts, llb.WithMetaResolver(opts.MetaResolver))
	}
	if opts.NoCache {
		so.imgOpts = append(so.imgOpts, llb.IgnoreCache)
		so.runOpts = append(so.runOpts, llb.IgnoreCache)
	}

	result := Result{
		Definitions: make([]*llb.Definition, 0, len(order)),
		StepNames:   make([]string, 0, len(order)),
	}

	states := make(map[string]llb.State, len(order))
	for _, idx := range order {
		step := &p.Steps[idx]
		def, st, err := buildStep(ctx, step, states, so)
		if err != nil {
			return Result{}, fmt.Errorf("building step %q: %w", step.Name, err)
		}
		result.Definitions = append(result.Definitions, def)
		result.StepNames = append(result.StepNames, step.Name)
		states[step.Name] = st
	}

	return result, nil
}

// validOrder checks that order is a complete, unique permutation of [0, n).
func validOrder(order []int, n int) bool {
	if len(order) != n {
		return false
	}
	seen := make([]bool, n)
	for _, idx := range order {
		if idx < 0 || idx >= n || seen[idx] {
			return false
		}
		seen[idx] = true
	}
	return true
}

func buildStep(
	ctx context.Context,
	step *pipeline.Step,
	depStates map[string]llb.State,
	opts stepOpts,
) (*llb.Definition, llb.State, error) {
	cmd := strings.Join(step.Run, " && ")
	if strings.TrimSpace(cmd) == "" {
		return nil, llb.State{}, fmt.Errorf("step %q: %w", step.Name, pipeline.ErrMissingRun)
	}

	imgOpts := append([]llb.ImageOption(nil), opts.imgOpts...)
	if step.Platform != "" {
		plat, err := platforms.Parse(step.Platform)
		if err != nil {
			return nil, llb.State{}, fmt.Errorf("step %q: parsing platform %q: %w", step.Name, step.Platform, err)
		}
		imgOpts = append(imgOpts, llb.Platform(plat))
	}
	st := llb.Image(step.Image, imgOpts...)

	if step.Workdir != "" {
		st = st.Dir(step.Workdir)
	}

	runOpts := append([]llb.RunOption{
		llb.Args([]string{"/bin/sh", "-c", cmd}),
		llb.WithCustomName(step.Name),
	}, opts.runOpts...)

	for _, dep := range step.DependsOn {
		depSt, ok := depStates[dep]
		if !ok {
			return nil, llb.State{}, fmt.Errorf(
				"step %q: missing dependency state for %q", step.Name, dep,
			)
		}
		runOpts = append(runOpts, llb.AddMount(
			"/deps/"+dep,
			depSt,
			llb.Readonly,
		))
	}

	// All mounts share a single named local context ("context"). Each m.Source
	// is a path within that shared context, not a distinct source directory.
	localOpts := []llb.LocalOption{llb.SharedKeyHint("context")}
	if len(opts.excludePatterns) > 0 {
		localOpts = append(localOpts, llb.ExcludePatterns(opts.excludePatterns))
	}

	for _, m := range step.Mounts {
		mountOpts := []llb.MountOption{llb.SourcePath(m.Source)}
		if m.ReadOnly {
			mountOpts = append(mountOpts, llb.Readonly)
		}
		runOpts = append(runOpts, llb.AddMount(
			m.Target,
			llb.Local("context", localOpts...),
			mountOpts...,
		))
	}

	for _, c := range step.Caches {
		runOpts = append(runOpts, llb.AddMount(
			c.Target,
			llb.Scratch(),
			llb.AsPersistentCacheDir(c.ID, llb.CacheMountShared),
		))
	}

	execState := st.Run(runOpts...).Root()
	def, err := execState.Marshal(ctx)
	if err != nil {
		return nil, llb.State{}, fmt.Errorf("marshaling: %w", err)
	}
	return def, execState, nil
}

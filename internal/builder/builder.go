// Package builder converts validated pipelines into BuildKit LLB definitions.
package builder

import (
	"context"
	"fmt"
	"path"
	"strings"

	"github.com/containerd/platforms"
	"github.com/moby/buildkit/client/llb"

	"github.com/ndisidore/cicada/pkg/pipeline"
)

// LocalExport pairs an LLB definition containing exported files with the
// target host path for the local exporter.
type LocalExport struct {
	Definition *llb.Definition
	StepName   string
	Local      string // host path target
	Dir        bool   // true when exporting a directory (trailing / on container path)
}

// Result holds the LLB definitions for a pipeline, one per step in topological order.
type Result struct {
	// Definitions contains one LLB definition per step, ordered by dependency.
	Definitions []*llb.Definition
	// StepNames maps each definition index to its step name.
	StepNames []string
	// Exports contains LLB definitions for steps with local export paths.
	Exports []LocalExport
}

// BuildOpts configures the LLB build process.
type BuildOpts struct {
	// NoCache disables BuildKit cache for all operations when true.
	NoCache bool
	// NoCacheFilter selectively disables cache for specific steps by name.
	NoCacheFilter map[string]struct{}
	// ExcludePatterns are glob patterns to exclude from local context mounts.
	ExcludePatterns []string
	// MetaResolver resolves OCI image config (ENV, WORKDIR, platform) at build time.
	MetaResolver llb.ImageMetaResolver
	// ExposeDeps mounts full dependency root filesystems at /deps/{name} (legacy).
	ExposeDeps bool
}

// stepOpts holds pre-computed LLB options applied to every step.
type stepOpts struct {
	imgOpts         []llb.ImageOption
	runOpts         []llb.RunOption
	excludePatterns []string
	pipelineEnv     []pipeline.EnvVar
	exposeDeps      bool
	globalNoCache   bool
	noCacheFilter   map[string]struct{}
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

	so := stepOpts{
		excludePatterns: opts.ExcludePatterns,
		pipelineEnv:     p.Env,
		exposeDeps:      opts.ExposeDeps,
		globalNoCache:   opts.NoCache,
		noCacheFilter:   opts.NoCacheFilter,
	}
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

		// Build export definitions for steps with local exports.
		for _, exp := range step.Exports {
			exportDef, err := buildExportDef(ctx, st, exp.Path)
			if err != nil {
				return Result{}, fmt.Errorf("building export for step %q: %w", step.Name, err)
			}
			result.Exports = append(result.Exports, LocalExport{
				Definition: exportDef,
				StepName:   step.Name,
				Local:      exp.Local,
				Dir:        strings.HasSuffix(exp.Path, "/"),
			})
		}
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

// stepCacheOpts returns per-step image and run options that disable caching
// when the step is targeted by NoCacheFilter or has NoCache set. It
// short-circuits when globalNoCache is already active to avoid redundant opts.
func stepCacheOpts(step *pipeline.Step, opts stepOpts) ([]llb.ImageOption, []llb.RunOption) {
	imgOpts := append([]llb.ImageOption(nil), opts.imgOpts...)
	runOpts := append([]llb.RunOption(nil), opts.runOpts...)
	if opts.globalNoCache {
		return imgOpts, runOpts
	}
	_, filtered := opts.noCacheFilter[step.Name]
	if filtered || step.NoCache {
		imgOpts = append(imgOpts, llb.IgnoreCache)
		runOpts = append(runOpts, llb.IgnoreCache)
	}
	return imgOpts, runOpts
}

//revive:disable-next-line:function-length,cognitive-complexity buildStep is a linear pipeline of LLB operations; splitting it further hurts readability.
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

	imgOpts, runOpts := stepCacheOpts(step, opts)

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

	// Apply env vars: pipeline-level first, then step-level (step overrides).
	for _, e := range opts.pipelineEnv {
		st = st.AddEnv(e.Key, e.Value)
	}
	for _, e := range step.Env {
		st = st.AddEnv(e.Key, e.Value)
	}
	st = st.File(llb.Mkdir("/cicada", 0o755))
	// Set after user env vars so it cannot be overridden; the output sourcing
	// preamble and dep mounts depend on this exact path.
	st = st.AddEnv("CICADA_OUTPUT", "/cicada/output")

	// Import artifacts from dependency steps.
	for _, art := range step.Artifacts {
		depSt, ok := depStates[art.From]
		if !ok {
			return nil, llb.State{}, fmt.Errorf(
				"step %q: missing dependency state for artifact from %q", step.Name, art.From,
			)
		}
		st = st.File(llb.Copy(depSt, art.Source, art.Target))
	}

	// For steps with dependencies, prepend output sourcing preamble so
	// env vars exported by dependencies via $CICADA_OUTPUT are available.
	// Dependency output files must contain valid shell KEY=VALUE pairs;
	// malformed content will cause the source (.) command to fail.
	if len(step.DependsOn) > 0 {
		preamble := `for __f in /cicada/deps/*/output; do [ -f "$__f" ] && { set -a; . "$__f"; set +a; }; done` + "\n"
		cmd = preamble + cmd
	}

	runOpts = append(runOpts,
		llb.Args([]string{"/bin/sh", "-c", cmd}),
		llb.WithCustomName(step.Name),
	)

	depMounts, err := depMountOpts(step, depStates, opts.exposeDeps)
	if err != nil {
		return nil, llb.State{}, err
	}
	runOpts = append(runOpts, depMounts...)

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

// depMountOpts builds run options for dependency mounts: /cicada/deps/{name}
// for output sourcing (always), and /deps/{name} for legacy full-FS access
// (only when exposeDeps is true).
//
//revive:disable-next-line:flag-parameter exposeDeps controls a clear behavioral branch.
func depMountOpts(step *pipeline.Step, depStates map[string]llb.State, exposeDeps bool) ([]llb.RunOption, error) {
	var opts []llb.RunOption
	for _, dep := range step.DependsOn {
		depSt, ok := depStates[dep]
		if !ok {
			return nil, fmt.Errorf(
				"step %q: missing dependency state for %q", step.Name, dep,
			)
		}
		opts = append(opts, llb.AddMount(
			"/cicada/deps/"+dep,
			depSt,
			llb.SourcePath("/cicada"),
			llb.Readonly,
		))
		if exposeDeps {
			opts = append(opts, llb.AddMount(
				"/deps/"+dep,
				depSt,
				llb.Readonly,
			))
		}
	}
	return opts, nil
}

// buildExportDef creates an LLB definition that extracts a file or directory
// from the exec state onto a scratch mount, suitable for solving with the local
// exporter. A trailing "/" on containerPath indicates a directory export; the
// contents are copied into the scratch mount root. Otherwise the single file is
// copied by basename.
// It uses a lightweight exec with GetMount rather than a FileOp Copy, because
// Copy from Run().Root() in a separate solve session can resolve the exec's
// input snapshot instead of its output.
// The step image must provide cp (coreutils); scratch or distroless images
// that lack it will fail at solve time.
func buildExportDef(ctx context.Context, execState llb.State, containerPath string) (*llb.Definition, error) {
	cleaned := path.Clean(containerPath)
	if cleaned == "." || cleaned == "/" {
		return nil, fmt.Errorf("invalid export path: %q", containerPath)
	}

	// For directories (trailing /), append /. so cp copies contents, not the
	// directory itself. For files, copy by basename.
	var src, dest string
	if strings.HasSuffix(containerPath, "/") {
		src = cleaned + "/."
		dest = "/cicada/export/"
	} else {
		src = cleaned
		dest = "/cicada/export/" + path.Base(cleaned)
	}

	exportRun := execState.Run(
		llb.Args([]string{"cp", "-a", src, dest}),
		llb.AddMount("/cicada/export", llb.Scratch()),
		llb.WithCustomName("export:"+cleaned),
	)
	def, err := exportRun.GetMount("/cicada/export").Marshal(ctx)
	if err != nil {
		return nil, fmt.Errorf("marshaling export for %q: %w", containerPath, err)
	}
	return def, nil
}

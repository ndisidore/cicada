// Package builder converts validated pipelines into BuildKit LLB definitions.
package builder

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"

	"github.com/containerd/platforms"
	"github.com/moby/buildkit/client/llb"

	"github.com/ndisidore/cicada/pkg/pipeline"
)

// errMissingDepState indicates a dependency's LLB state was not found during build.
var errMissingDepState = errors.New("missing dependency state")

// LocalExport pairs an LLB definition containing exported files with the
// target host path for the local exporter.
type LocalExport struct {
	Definition *llb.Definition
	JobName    string
	Local      string // host path target
	Dir        bool   // true when exporting a directory (trailing / on container path)
}

// ImageExport pairs an LLB definition with image publishing metadata.
type ImageExport struct {
	Definition *llb.Definition
	JobName    string
	Publish    pipeline.Publish
	Platform   string
}

// Result holds the LLB definitions for a pipeline, one per job in topological order.
type Result struct {
	// Definitions contains one LLB definition per job, ordered by dependency.
	Definitions []*llb.Definition
	// JobNames maps each definition index to its job name.
	JobNames []string
	// Exports contains LLB definitions for jobs with local export paths.
	Exports []LocalExport
	// ImageExports contains image publish targets for jobs with Publish set.
	ImageExports []ImageExport
	// OutputDefs maps job names to LLB definitions that extract /cicada/output.
	OutputDefs map[string]*llb.Definition
}

// BuildOpts configures the LLB build process.
type BuildOpts struct {
	// NoCache disables BuildKit cache for all operations when true.
	NoCache bool
	// NoCacheFilter selectively disables cache for specific jobs by name.
	NoCacheFilter map[string]struct{}
	// ExcludePatterns are glob patterns to exclude from local context mounts.
	ExcludePatterns []string
	// MetaResolver resolves OCI image config (ENV, WORKDIR, platform) at build time.
	MetaResolver llb.ImageMetaResolver
	// ExposeDeps mounts full dependency root filesystems at /deps/{name} (legacy).
	ExposeDeps bool
}

// jobOpts holds pre-computed LLB options applied to every job.
type jobOpts struct {
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
	if !validOrder(order, len(p.Jobs)) {
		var err error
		order, err = p.Validate()
		if err != nil {
			return Result{}, fmt.Errorf("validating pipeline: %w", err)
		}
	}

	jo := jobOpts{
		excludePatterns: opts.ExcludePatterns,
		pipelineEnv:     p.Env,
		exposeDeps:      opts.ExposeDeps,
		globalNoCache:   opts.NoCache,
		noCacheFilter:   opts.NoCacheFilter,
	}
	if opts.MetaResolver != nil {
		jo.imgOpts = append(jo.imgOpts, llb.WithMetaResolver(opts.MetaResolver))
	}
	if opts.NoCache {
		jo.imgOpts = append(jo.imgOpts, llb.IgnoreCache)
		jo.runOpts = append(jo.runOpts, llb.IgnoreCache)
	}

	result := Result{
		Definitions: make([]*llb.Definition, 0, len(order)),
		JobNames:    make([]string, 0, len(order)),
		OutputDefs:  make(map[string]*llb.Definition, len(order)),
	}

	states := make(map[string]llb.State, len(order))
	for _, idx := range order {
		job := &p.Jobs[idx]
		def, st, err := buildJob(ctx, job, states, jo)
		if err != nil {
			return Result{}, fmt.Errorf("building job %q: %w", job.Name, err)
		}
		result.Definitions = append(result.Definitions, def)
		result.JobNames = append(result.JobNames, job.Name)
		states[job.Name] = st

		// Build output extraction definition for deferred when conditions.
		outputDef, err := buildExportDef(ctx, st, "/cicada/output")
		if err != nil {
			return Result{}, fmt.Errorf("building output def for job %q: %w", job.Name, err)
		}
		result.OutputDefs[job.Name] = outputDef

		// Collect exports from job-level and step-level, all from final state.
		allExports := collectExports(job)
		for _, exp := range allExports {
			exportDef, err := buildExportDef(ctx, st, exp.Path)
			if err != nil {
				return Result{}, fmt.Errorf("building export for job %q: %w", job.Name, err)
			}
			result.Exports = append(result.Exports, LocalExport{
				Definition: exportDef,
				JobName:    job.Name,
				Local:      exp.Local,
				Dir:        strings.HasSuffix(exp.Path, "/"),
			})
		}

		if job.Publish != nil {
			result.ImageExports = append(result.ImageExports, ImageExport{
				Definition: def,
				JobName:    job.Name,
				Publish:    *job.Publish,
				Platform:   job.Platform,
			})
		}
	}

	return result, nil
}

// collectExports gathers all export declarations from a job (job-level + step-level).
func collectExports(job *pipeline.Job) []pipeline.Export {
	n := len(job.Exports)
	for i := range job.Steps {
		n += len(job.Steps[i].Exports)
	}
	if n == 0 {
		return nil
	}
	exports := make([]pipeline.Export, 0, n)
	exports = append(exports, job.Exports...)
	for i := range job.Steps {
		exports = append(exports, job.Steps[i].Exports...)
	}
	return exports
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

// jobCacheOpts returns per-job image and run options that disable caching
// when the job is targeted by NoCacheFilter or has NoCache set. It
// short-circuits when globalNoCache is already active to avoid redundant opts.
func jobCacheOpts(job *pipeline.Job, opts jobOpts) ([]llb.ImageOption, []llb.RunOption) {
	imgOpts := append([]llb.ImageOption(nil), opts.imgOpts...)
	runOpts := append([]llb.RunOption(nil), opts.runOpts...)
	if opts.globalNoCache {
		return imgOpts, runOpts
	}
	_, filtered := opts.noCacheFilter[job.Name]
	if filtered || job.NoCache {
		imgOpts = append(imgOpts, llb.IgnoreCache)
		runOpts = append(runOpts, llb.IgnoreCache)
	}
	return imgOpts, runOpts
}

// buildJob creates a single LLB definition for a job by chaining sequential
// step Run operations. Each step produces a new ExecOp; .Root() returns the
// output state for the next step.
//
//revive:disable-next-line:function-length,cognitive-complexity,cyclomatic buildJob is a linear pipeline of LLB operations; splitting it further hurts readability.
func buildJob(
	ctx context.Context,
	job *pipeline.Job,
	depStates map[string]llb.State,
	opts jobOpts,
) (*llb.Definition, llb.State, error) {
	imgOpts, baseRunOpts := jobCacheOpts(job, opts)

	if job.Platform != "" {
		plat, err := platforms.Parse(job.Platform)
		if err != nil {
			return nil, llb.State{}, fmt.Errorf("job %q: parsing platform %q: %w", job.Name, job.Platform, err)
		}
		imgOpts = append(imgOpts, llb.Platform(plat))
	}
	st := llb.Image(job.Image, imgOpts...)

	if job.Workdir != "" {
		st = st.Dir(job.Workdir)
	}

	// Apply env vars: pipeline-level first, then job-level (job overrides).
	for _, e := range opts.pipelineEnv {
		st = st.AddEnv(e.Key, e.Value)
	}
	for _, e := range job.Env {
		st = st.AddEnv(e.Key, e.Value)
	}
	st = st.File(llb.Mkdir("/cicada", 0o755))
	st = st.File(llb.Mkfile("/cicada/output", 0o644, nil))
	// Set after user env vars so it cannot be overridden.
	st = st.AddEnv("CICADA_OUTPUT", "/cicada/output")

	// Import job-level artifacts from dependency jobs.
	for _, art := range job.Artifacts {
		depSt, ok := depStates[art.From]
		if !ok {
			return nil, llb.State{}, fmt.Errorf(
				"artifact from %q: %w", art.From, errMissingDepState,
			)
		}
		st = st.File(llb.Copy(depSt, art.Source, art.Target))
	}

	// Build dep output sourcing preamble (prepended to first step only).
	var preamble string
	if len(job.DependsOn) > 0 {
		preamble = `for __f in /cicada/deps/*/output; do [ -f "$__f" ] && { set -a; . "$__f"; set +a; }; done` + "\n"
	}

	// Build dep mount options (shared across all steps).
	depMounts, err := depMountOpts(job, depStates, opts.exposeDeps)
	if err != nil {
		return nil, llb.State{}, err
	}

	// Build local context for bind mounts.
	localOpts := []llb.LocalOption{llb.SharedKeyHint("context")}
	if len(opts.excludePatterns) > 0 {
		localOpts = append(localOpts, llb.ExcludePatterns(opts.excludePatterns))
	}

	// Pre-build job-level mount and cache run options (reused per step).
	jobMountOpts := buildMountRunOpts(job.Mounts, job.Caches, localOpts)

	// Determine whether job-level caching is already disabled in baseRunOpts.
	_, jobFiltered := opts.noCacheFilter[job.Name]
	jobNoCached := opts.globalNoCache || job.NoCache || jobFiltered

	// Execute steps sequentially, chaining state.
	for i := range job.Steps {
		step := &job.Steps[i]

		// Step-level artifacts: insert Copy before this step's Run.
		for _, art := range step.Artifacts {
			depSt, ok := depStates[art.From]
			if !ok {
				return nil, llb.State{}, fmt.Errorf(
					"step %q: artifact from %q: %w",
					step.Name, art.From, errMissingDepState,
				)
			}
			st = st.File(llb.Copy(depSt, art.Source, art.Target))
		}

		// Step workdir override.
		if step.Workdir != "" {
			st = st.Dir(step.Workdir)
		}

		// Step-scoped env (additive to job env already in state).
		for _, e := range step.Env {
			st = st.AddEnv(e.Key, e.Value)
		}

		if len(step.Run) == 0 {
			return nil, llb.State{}, fmt.Errorf("job %q step %q: %w", job.Name, step.Name, pipeline.ErrMissingRun)
		}

		// Pre-compute step-level mount/cache run options (shared across all commands).
		stepMountOpts := buildMountRunOpts(step.Mounts, step.Caches, localOpts)

		for j, cmd := range step.Run {
			if strings.TrimSpace(cmd) == "" {
				return nil, llb.State{}, fmt.Errorf("job %q step %q run[%d]: %w", job.Name, step.Name, j, pipeline.ErrEmptyRunCommand)
			}

			// Preamble sources dependency output env vars into the shell.
			// Only applied to the first step: each command runs in a fresh
			// shell, so every command in step 0 needs it. Later steps
			// intentionally omit it — cross-step env should use the `env`
			// directive (which sets LLB-level env) or write to $CICADA_OUTPUT.
			shellCmd := cmd
			if i == 0 && preamble != "" {
				shellCmd = preamble + cmd
			}

			runOpts := append([]llb.RunOption(nil), baseRunOpts...)
			runOpts = append(runOpts,
				llb.Args([]string{"/bin/sh", "-c", shellCmd}),
				llb.WithCustomName(job.Name+"/"+step.Name+"/"+cmd),
			)
			runOpts = append(runOpts, depMounts...)
			runOpts = append(runOpts, jobMountOpts...)
			runOpts = append(runOpts, stepMountOpts...)

			if step.NoCache && !jobNoCached {
				runOpts = append(runOpts, llb.IgnoreCache)
			}

			st = st.Run(runOpts...).Root()
		}
	}

	def, err := st.Marshal(ctx)
	if err != nil {
		return nil, llb.State{}, fmt.Errorf("marshaling: %w", err)
	}
	return def, st, nil
}

// buildMountRunOpts converts mount and cache slices into LLB run options.
func buildMountRunOpts(mounts []pipeline.Mount, caches []pipeline.Cache, localOpts []llb.LocalOption) []llb.RunOption {
	if len(mounts) == 0 && len(caches) == 0 {
		return nil
	}
	opts := make([]llb.RunOption, 0, len(mounts)+len(caches))
	for _, m := range mounts {
		mountOpts := []llb.MountOption{llb.SourcePath(m.Source)}
		if m.ReadOnly {
			mountOpts = append(mountOpts, llb.Readonly)
		}
		opts = append(opts, llb.AddMount(
			m.Target,
			llb.Local("context", localOpts...),
			mountOpts...,
		))
	}
	for _, c := range caches {
		opts = append(opts, llb.AddMount(
			c.Target,
			llb.Scratch(),
			llb.AsPersistentCacheDir(c.ID, llb.CacheMountShared),
		))
	}
	return opts
}

// depMountOpts builds run options for dependency mounts: /cicada/deps/{name}
// for output sourcing (always), and /deps/{name} for legacy full-FS access
// (only when exposeDeps is true).
//
//revive:disable-next-line:flag-parameter exposeDeps controls a clear behavioral branch.
func depMountOpts(job *pipeline.Job, depStates map[string]llb.State, exposeDeps bool) ([]llb.RunOption, error) {
	var opts []llb.RunOption
	for _, dep := range job.DependsOn {
		depSt, ok := depStates[dep]
		if !ok {
			return nil, fmt.Errorf(
				"dep %q: %w", dep, errMissingDepState,
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
// The job image must provide cp (coreutils); scratch or distroless images
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

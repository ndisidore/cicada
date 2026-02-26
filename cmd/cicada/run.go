package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"

	bkclient "github.com/moby/buildkit/client"
	"github.com/moby/buildkit/client/llb/imagemetaresolver"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/session/secrets/secretsprovider"
	"github.com/tonistiigi/fsutil"
	"github.com/urfave/cli/v3"

	"github.com/ndisidore/cicada/internal/builder"
	"github.com/ndisidore/cicada/internal/cache"
	"github.com/ndisidore/cicada/internal/filesync"
	"github.com/ndisidore/cicada/internal/imagestore"
	"github.com/ndisidore/cicada/internal/progress/progressmodel"
	"github.com/ndisidore/cicada/internal/runner"
	"github.com/ndisidore/cicada/internal/runner/runnermodel"
	"github.com/ndisidore/cicada/internal/secret"
	"github.com/ndisidore/cicada/internal/synccontext"
	"github.com/ndisidore/cicada/pkg/conditional"
	"github.com/ndisidore/cicada/pkg/gitinfo"
	"github.com/ndisidore/cicada/pkg/pipeline"
	"github.com/ndisidore/cicada/pkg/pipeline/pipelinemodel"
	"github.com/ndisidore/cicada/pkg/slogctx"
)

func (a *app) validateAction(_ context.Context, cmd *cli.Command) error {
	path := cmd.Args().First()
	if path == "" {
		return errors.New("usage: cicada validate <file | ->")
	}

	p, err := a.parse(path)
	if err != nil {
		return fmt.Errorf("parsing %s: %w", path, err)
	}

	if _, err := pipeline.Validate(&p); err != nil {
		return fmt.Errorf("validating %s: %w", path, err)
	}

	a.printPipelineSummary(p.Name, p.Jobs)
	return nil
}

//revive:disable-next-line:function-length runAction is a linear pipeline of parse-filter-build-convert-execute; splitting it would obscure the sequential flow.
func (a *app) runAction(ctx context.Context, cmd *cli.Command) error {
	path := cmd.Args().First()
	if path == "" {
		return errors.New("usage: cicada run <file | ->")
	}

	parallelism := cmd.Int("parallelism")
	if parallelism < 0 {
		return fmt.Errorf("invalid value %d for flag --parallelism: must be >= 0", parallelism)
	}

	p, err := a.parse(path)
	if err != nil {
		return fmt.Errorf("parsing %s: %w", path, err)
	}

	filterOpts := pipelinemodel.FilterOpts{
		StartAt:   cmd.String("start-at"),
		StopAfter: cmd.String("stop-after"),
	}
	p.Jobs, err = pipeline.FilterJobs(p.Jobs, filterOpts)
	if err != nil {
		return fmt.Errorf("filtering jobs: %w", err)
	}
	p.TopoOrder = nil

	wctx := conditional.Context{
		Getenv: os.Getenv,
		Branch: gitinfo.DetectBranch(ctx, os.Getenv),
		Tag:    gitinfo.DetectTag(ctx, os.Getenv),
	}
	condResult, err := pipeline.EvaluateConditions(ctx, p, wctx)
	if err != nil {
		return fmt.Errorf("evaluating when conditions: %w", err)
	}
	p = condResult.Pipeline

	cwd, err := a.getwd()
	if err != nil {
		return fmt.Errorf("getting working directory: %w", err)
	}

	excludes, err := synccontext.LoadIgnorePatterns(cwd)
	if err != nil && !errors.Is(err, synccontext.ErrNoIgnoreFile) {
		return fmt.Errorf("loading ignore patterns: %w", err)
	}

	noCacheFilter := buildNoCacheFilter(ctx, cmd.StringSlice("no-cache-filter"), p)

	result, err := builder.Build(ctx, p, builder.BuildOpts{
		NoCache:         cmd.Bool("no-cache"),
		NoCacheFilter:   noCacheFilter,
		ExcludePatterns: excludes,
		MetaResolver:    imagemetaresolver.Default(),
		ExposeDeps:      cmd.Bool("expose-deps"),
	})
	if err != nil {
		return fmt.Errorf("building %s: %w", path, err)
	}

	jobs, err := buildRunnerJobs(result, p, condResult.SkippedSteps)
	if err != nil {
		return fmt.Errorf("converting build result: %w", err)
	}

	if cmd.Bool("dry-run") {
		a.printDryRun(p.Name, jobs)
		return nil
	}

	sessionAttachables, secretValues, err := resolveSecrets(ctx, result.SecretDecls)
	if err != nil {
		return fmt.Errorf("resolving secrets: %w", err)
	}

	cacheExports, err := cache.ParseSpecs(cmd.StringSlice("cache-to"))
	if err != nil {
		return fmt.Errorf("parsing --cache-to: %w", err)
	}
	cacheImports, err := cache.ParseSpecs(cmd.StringSlice("cache-from"))
	if err != nil {
		return fmt.Errorf("parsing --cache-from: %w", err)
	}
	cacheExports = cache.DetectGHA(cacheExports)
	cacheImports = cache.DetectGHA(cacheImports)

	var collector *cache.Collector
	if cmd.Bool("cache-stats") {
		collector = cache.NewCollector()
	}

	exports := make([]runnermodel.Export, len(result.Exports))
	for i, exp := range result.Exports {
		exports[i] = runnermodel.Export{
			Definition: exp.Definition,
			JobName:    exp.JobName,
			Local:      exp.Local,
			Dir:        exp.Dir,
		}
	}

	imagePublishes := buildImagePublishes(ctx, result.ImageExports, cmd.StringSlice("with-docker-export"))

	return a.executePipeline(ctx, cmd, pipelineRunParams{
		path:           path,
		pipe:           p,
		jobs:           jobs,
		exports:        exports,
		imagePublishes: imagePublishes,
		cwd:            cwd,
		cacheExports:   cacheExports,
		cacheImports:   cacheImports,
		cacheCollector: collector,
		whenCtx:        &wctx,
		skippedJobs:    condResult.Skipped,
		failFast:       cmd.Bool("fail-fast"),
		session:        sessionAttachables,
		secretValues:   secretValues,
	})
}

// pipelineRunParams groups parameters for executePipeline.
type pipelineRunParams struct {
	path           string
	pipe           pipelinemodel.Pipeline
	jobs           []runnermodel.Job
	exports        []runnermodel.Export
	imagePublishes []runnermodel.ImagePublish
	cwd            string
	cacheExports   []bkclient.CacheOptionsEntry
	cacheImports   []bkclient.CacheOptionsEntry
	cacheCollector *cache.Collector
	whenCtx        *conditional.Context
	skippedJobs    []string
	failFast       bool
	session        []session.Attachable
	secretValues   map[string]string
}

// resolveSecrets resolves pipeline secret declarations to host values and
// returns the BuildKit session attachable and plaintext map for redaction.
func resolveSecrets(ctx context.Context, decls []pipelinemodel.SecretDecl) ([]session.Attachable, map[string]string, error) {
	if len(decls) == 0 {
		return nil, nil, nil
	}
	resolved, err := secret.Resolve(ctx, decls)
	if err != nil {
		return nil, nil, err
	}
	attachables := []session.Attachable{secretsprovider.FromMap(secret.ToMap(resolved))}
	return attachables, secret.PlaintextValues(resolved), nil
}

// executePipeline connects to BuildKit and runs the pipeline jobs.
//
//revive:disable-next-line:function-length executePipeline is a linear connect-setup-run sequence; splitting it would scatter resource lifecycle (defer closer, defer shutdown).
func (a *app) executePipeline(ctx context.Context, cmd *cli.Command, params pipelineRunParams) (shutdownErr error) {
	addr, err := a.resolveAddr(ctx, cmd)
	if err != nil {
		return err
	}

	solver, closer, err := a.connect(ctx, addr)
	if err != nil {
		return err
	}
	defer func() {
		if err := closer(); err != nil {
			slogctx.FromContext(ctx).LogAttrs(ctx, slog.LevelDebug, "close connection", slog.String("error", err.Error()))
		}
	}()

	if cmd.Bool("offline") {
		if err := checkOffline(ctx, solver, params.pipe, params.path); err != nil {
			return err
		}
	}

	baseFS, err := fsutil.NewFS(params.cwd)
	if err != nil {
		return fmt.Errorf("opening context directory %s: %w", params.cwd, err)
	}

	parallelism := int(cmd.Int("parallelism"))

	display, err := a.selectDisplay(cmd.String("progress"), cmd.Bool("boring"))
	if err != nil {
		return err
	}

	if err := display.Start(ctx); err != nil {
		return fmt.Errorf("starting display: %w", err)
	}
	defer func() { shutdownErr = errors.Join(shutdownErr, display.Shutdown()) }()

	contextFS := baseFS
	if !cmd.Bool("no-sync-cache") {
		cachePath, cacheErr := filesync.CachePath(params.cwd)
		if cacheErr != nil {
			slogctx.FromContext(ctx).LogAttrs(ctx, slog.LevelDebug,
				"resolving sync cache dir", slog.String("error", cacheErr.Error()))
		} else {
			contextFS = filesync.New(baseFS, filesync.Options{
				Cache:  filesync.NewHashCache(cachePath),
				Sender: display,
			})
		}
	}

	for _, name := range params.skippedJobs {
		display.Send(progressmodel.JobSkippedMsg{Job: name})
	}

	runErr := runner.Run(ctx, runnermodel.RunInput{
		Solver:  solver,
		Runtime: a.rt,
		Jobs:    params.jobs,
		LocalMounts: map[string]fsutil.FS{
			"context": contextFS,
		},
		Sender:         display,
		Parallelism:    parallelism,
		FailFast:       params.failFast,
		Exports:        params.exports,
		ImagePublishes: params.imagePublishes,
		CacheExports:   params.cacheExports,
		CacheImports:   params.cacheImports,
		CacheCollector: params.cacheCollector,
		WhenContext:    params.whenCtx,
		Session:        params.session,
		SecretValues:   params.secretValues,
	})

	if params.cacheCollector != nil {
		cache.PrintReport(a.stdout, params.cacheCollector.Report())
	}

	return runErr
}

// resolveAddr returns the BuildKit daemon address, starting the daemon if needed.
func (a *app) resolveAddr(ctx context.Context, cmd *cli.Command) (string, error) {
	addr := cmd.String("addr")
	if cmd.Bool("no-daemon") {
		return addr, nil
	}
	var err error
	addr, err = a.engine.EnsureRunning(ctx, addr)
	if err != nil {
		return "", fmt.Errorf("ensuring buildkitd: %w", err)
	}
	return addr, nil
}

func (a *app) pullAction(ctx context.Context, cmd *cli.Command) (shutdownErr error) {
	path := cmd.Args().First()
	if path == "" {
		return errors.New("usage: cicada pull <file | ->")
	}

	p, err := a.parse(path)
	if err != nil {
		return fmt.Errorf("parsing %s: %w", path, err)
	}

	images := pipeline.CollectImages(p)
	if len(images) == 0 {
		_, _ = fmt.Fprintln(a.stdout, "No images to pull")
		return nil
	}

	addr, err := a.resolveAddr(ctx, cmd)
	if err != nil {
		return err
	}

	solver, closer, err := a.connect(ctx, addr)
	if err != nil {
		return err
	}
	defer func() {
		if err := closer(); err != nil {
			slogctx.FromContext(ctx).LogAttrs(ctx, slog.LevelDebug, "close connection", slog.String("error", err.Error()))
		}
	}()

	display, err := a.selectDisplay(cmd.String("progress"), cmd.Bool("boring"))
	if err != nil {
		return err
	}

	if err := display.Start(ctx); err != nil {
		return fmt.Errorf("starting display: %w", err)
	}
	defer func() { shutdownErr = errors.Join(shutdownErr, display.Shutdown()) }()

	if err := imagestore.PullImages(ctx, solver, images, display); err != nil {
		return fmt.Errorf("pulling images: %w", err)
	}

	_, _ = fmt.Fprintf(a.stdout, "Pulled %d image(s)\n", len(images))
	return nil
}

func checkOffline(ctx context.Context, solver runnermodel.Solver, p pipelinemodel.Pipeline, path string) error {
	images := pipeline.CollectImages(p)
	missing, err := imagestore.CheckCached(ctx, solver, images)
	if err != nil {
		return fmt.Errorf("checking image cache: %w", err)
	}
	if len(missing) > 0 {
		msg := fmt.Sprintf(
			"%d image(s) not cached: %s\nrun 'cicada pull %s' first",
			len(missing), strings.Join(missing, ", "), path,
		)
		return fmt.Errorf("%w: %s", errOfflineMissingImages, msg)
	}
	return nil
}

func (a *app) engineStartAction(ctx context.Context, _ *cli.Command) error {
	addr, err := a.engine.Start(ctx)
	if err != nil {
		return fmt.Errorf("starting engine: %w", err)
	}
	_, _ = fmt.Fprintf(a.stdout, "BuildKit engine started at %s\n", addr)
	return nil
}

func (a *app) engineStopAction(ctx context.Context, _ *cli.Command) error {
	stopErr := a.engine.Stop(ctx)
	removeErr := a.engine.Remove(ctx)
	if err := errors.Join(stopErr, removeErr); err != nil {
		return fmt.Errorf("stopping engine: %w", err)
	}
	_, _ = fmt.Fprintln(a.stdout, "BuildKit engine stopped")
	return nil
}

func (a *app) engineStatusAction(ctx context.Context, _ *cli.Command) error {
	state, err := a.engine.Status(ctx)
	if err != nil {
		return fmt.Errorf("checking engine status: %w", err)
	}
	if state == "" {
		_, _ = fmt.Fprintln(a.stdout, "BuildKit engine: not running")
	} else {
		_, _ = fmt.Fprintf(a.stdout, "BuildKit engine: %s\n", state)
	}
	return nil
}

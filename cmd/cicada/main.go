// Package main provides the CLI entry point for cicada.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	bkclient "github.com/moby/buildkit/client"
	"github.com/moby/buildkit/client/llb/imagemetaresolver"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/session/secrets/secretsprovider"
	"github.com/tonistiigi/fsutil"
	"github.com/urfave/cli/v3"
	"golang.org/x/term"

	"github.com/ndisidore/cicada/internal/builder"
	"github.com/ndisidore/cicada/internal/cache"
	"github.com/ndisidore/cicada/internal/daemon"
	"github.com/ndisidore/cicada/internal/imagestore"
	"github.com/ndisidore/cicada/internal/progress"
	"github.com/ndisidore/cicada/internal/progress/plain"
	"github.com/ndisidore/cicada/internal/progress/quiet"
	"github.com/ndisidore/cicada/internal/progress/tui"
	"github.com/ndisidore/cicada/internal/runner"
	"github.com/ndisidore/cicada/internal/runtime"
	"github.com/ndisidore/cicada/internal/runtime/docker"
	"github.com/ndisidore/cicada/internal/runtime/podman"
	"github.com/ndisidore/cicada/internal/secret"
	"github.com/ndisidore/cicada/internal/synccontext"
	"github.com/ndisidore/cicada/pkg/conditional"
	"github.com/ndisidore/cicada/pkg/gitinfo"
	"github.com/ndisidore/cicada/pkg/parser"
	"github.com/ndisidore/cicada/pkg/pipeline"
	"github.com/ndisidore/cicada/pkg/slogctx"
)

// errResultMismatch indicates builder.Result has mismatched Definitions and JobNames lengths.
var errResultMismatch = errors.New("builder.Result: definitions/job-names length mismatch")

// errUnknownBuilderJob indicates a builder job name not found in the pipeline.
var errUnknownBuilderJob = errors.New("builder job not found in pipeline")

// errOfflineMissingImages indicates that offline mode was requested but some
// pipeline images are not present in the BuildKit cache.
var errOfflineMissingImages = errors.New("offline mode: images not cached")

// Engine abstracts daemon lifecycle operations for testability.
type Engine interface {
	// EnsureRunning ensures the daemon is running at the given address, starting it if needed.
	EnsureRunning(ctx context.Context, addr string) (string, error)
	// Start starts the BuildKit daemon and returns its address.
	Start(ctx context.Context) (string, error)
	// Stop stops the BuildKit daemon.
	Stop(ctx context.Context) error
	// Remove removes the BuildKit daemon container.
	Remove(ctx context.Context) error
	// Status returns the current daemon state, or "" if not running.
	Status(ctx context.Context) (string, error)
}

// app bundles dependencies so CLI action handlers become testable methods.
type app struct {
	engine  Engine
	rt      runtime.Runtime
	connect func(ctx context.Context, addr string) (runner.Solver, func() error, error)
	parse   func(path string) (pipeline.Pipeline, error)
	getwd   func() (string, error)
	stdout  io.Writer
	isTTY   bool
	format  string // resolved output format (pretty, json, text)
}

func main() {
	p := &parser.Parser{Resolver: &parser.FileResolver{}}
	a := &app{
		connect: defaultConnect,
		parse:   p.ParseFile,
		getwd:   os.Getwd,
		stdout:  os.Stdout,
		isTTY:   term.IsTerminal(int(os.Stdout.Fd())) && os.Getenv("CI") == "",
	}

	named := map[runtime.Type]func() runtime.Runtime{
		runtime.Docker: func() runtime.Runtime { return docker.New() },
		runtime.Podman: func() runtime.Runtime { return podman.New() },
	}
	resolver := &runtime.Resolver{
		Factories: []func() runtime.Runtime{named[runtime.Docker], named[runtime.Podman]},
		Named:     named,
	}
	initEngine := func(ctx context.Context, cmd *cli.Command) (context.Context, error) {
		rtName := cmd.String("runtime")
		rt, err := resolver.Get(ctx, rtName)
		if err != nil {
			return ctx, fmt.Errorf("resolve runtime %s: %w", rtName, err)
		}
		mgr, err := daemon.NewManager(rt)
		if err != nil {
			return ctx, fmt.Errorf("create daemon manager for runtime %s: %w", rtName, err)
		}
		a.engine = mgr
		a.rt = rt
		return ctx, nil
	}

	cmd := &cli.Command{
		Name:  "cicada",
		Usage: "a container-native CI/CD pipeline runner",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    "runtime",
				Usage:   "container runtime (auto, docker, podman)",
				Value:   "auto",
				Sources: cli.EnvVars("CICADA_RUNTIME"),
			},
			&cli.StringFlag{
				Name:    "format",
				Usage:   "output format (auto, pretty, json, text)",
				Value:   "auto",
				Sources: cli.EnvVars("CICADA_FORMAT"),
			},
			&cli.StringFlag{
				Name:    "log-level",
				Usage:   "log level (debug, info, warn, error)",
				Value:   "info",
				Sources: cli.EnvVars("CICADA_LOG_LEVEL"),
			},
		},
		Before: func(ctx context.Context, cmd *cli.Command) (context.Context, error) {
			a.format = cmd.String("format")
			if a.format == "auto" {
				if a.isTTY {
					a.format = "pretty"
				} else {
					a.format = "text"
				}
			}
			var level slog.Level
			if err := level.UnmarshalText([]byte(cmd.String("log-level"))); err != nil {
				return ctx, fmt.Errorf("invalid log level %q: %w", cmd.String("log-level"), err)
			}
			logger, err := progress.NewLogger(a.stdout, a.format, level)
			if err != nil {
				return ctx, fmt.Errorf("initializing logger: %w", err)
			}
			slog.SetDefault(logger)
			return slogctx.ContextWithLogger(ctx, logger), nil
		},
		Commands: []*cli.Command{
			{
				Name:      "validate",
				Usage:     "validate a KDL pipeline file",
				ArgsUsage: "<file>",
				Action:    a.validateAction,
			},
			{
				Name:               "run",
				Usage:              "run a KDL pipeline against BuildKit",
				ArgsUsage:          "<file>",
				Before:             initEngine,
				SliceFlagSeparator: ";",
				Flags: append(buildkitFlags(),
					&cli.BoolFlag{
						Name:  "dry-run",
						Usage: "generate LLB without executing",
					},
					&cli.BoolFlag{
						Name:  "no-cache",
						Usage: "disable BuildKit cache for all jobs",
					},
					&cli.StringSliceFlag{
						Name:  "no-cache-filter",
						Usage: "disable cache for specific jobs by name",
					},
					&cli.BoolFlag{
						Name:  "offline",
						Usage: "fail if images are not cached (use 'cicada pull' first)",
					},
					&cli.BoolFlag{
						Name:  "boring",
						Usage: "use ASCII instead of emoji in TUI output",
					},
					&cli.IntFlag{
						Name:    "parallelism",
						Aliases: []string{"j"},
						Usage:   "max concurrent jobs (0 = unlimited)",
						Value:   0,
					},
					&cli.BoolFlag{
						Name:  "fail-fast",
						Usage: "cancel all jobs on first failure (disable with --fail-fast=false to let independent jobs finish)",
						Value: true,
					},
					&cli.BoolFlag{
						Name:  "expose-deps",
						Usage: "mount full dependency root filesystems at /deps/{name}",
					},
					&cli.StringFlag{
						Name:  "start-at",
						Usage: "run from this job (or job:step) forward",
					},
					&cli.StringFlag{
						Name:  "stop-after",
						Usage: "run up to and including this job (or job:step)",
					},
					&cli.StringSliceFlag{
						Name:  "cache-to",
						Usage: "cache export destination (e.g. type=registry,ref=ghcr.io/user/cache)",
					},
					&cli.StringSliceFlag{
						Name:  "cache-from",
						Usage: "cache import source (e.g. type=registry,ref=ghcr.io/user/cache)",
					},
					&cli.BoolFlag{
						Name:  "cache-stats",
						Usage: "print cache hit/miss statistics after run",
					},
					&cli.StringSliceFlag{
						Name:  "with-docker-export",
						Usage: "load published images into local Docker daemon (job names, or '*' for all)",
					},
				),
				Action: a.runAction,
			},
			{
				Name:      "pull",
				Usage:     "pre-pull pipeline images into the BuildKit cache",
				ArgsUsage: "<file>",
				Before:    initEngine,
				Flags: append(buildkitFlags(),
					&cli.BoolFlag{
						Name:  "boring",
						Usage: "use ASCII instead of emoji in TUI output",
					},
				),
				Action: a.pullAction,
			},
			{
				Name:   "engine",
				Usage:  "manage the local BuildKit engine",
				Before: initEngine,
				Commands: []*cli.Command{
					{
						Name:   "start",
						Usage:  "start the BuildKit engine",
						Action: a.engineStartAction,
					},
					{
						Name:   "stop",
						Usage:  "stop the BuildKit engine",
						Action: a.engineStopAction,
					},
					{
						Name:   "status",
						Usage:  "show engine status",
						Action: a.engineStatusAction,
					},
				},
			},
		},
		ExitErrHandler: func(_ context.Context, _ *cli.Command, err error) {
			if err != nil {
				var de *progress.DisplayError
				if errors.As(err, &de) {
					_, _ = fmt.Fprintf(os.Stderr, "error: %s\n", de.Human)
				} else {
					_, _ = fmt.Fprintf(os.Stderr, "error: %v\n", err)
				}
			}
		},
	}

	if err := cmd.Run(context.Background(), os.Args); err != nil {
		os.Exit(1)
	}
}

// defaultConnect creates a BuildKit client and returns it as a runner.Solver.
// bkclient.New is lazy (no network I/O), so we eagerly verify reachability
// via ListWorkers. This also warms the gRPC/HTTP2 connection so that
// subsequent Solve calls can establish session streams immediately,
// preventing BuildKit's 5-second session-lookup timeout from firing
// when secrets require the session during early cache-key computation.
func defaultConnect(ctx context.Context, addr string) (runner.Solver, func() error, error) {
	c, err := bkclient.New(ctx, addr)
	if err != nil {
		return nil, func() error { return nil }, fmt.Errorf("connecting to buildkitd at %s: %w", addr, err)
	}
	if _, err := c.ListWorkers(ctx); err != nil {
		_ = c.Close()
		return nil, func() error { return nil }, fmt.Errorf("reaching buildkitd at %s: %w", addr, err)
	}
	return c, c.Close, nil
}

// buildkitFlags returns the shared flag set for commands that connect to BuildKit.
func buildkitFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{
			Name:  "addr",
			Usage: "BuildKit daemon address (unix socket or tcp://host:port)",
			Value: daemon.DefaultAddr(),
		},
		&cli.BoolFlag{
			Name:  "no-daemon",
			Usage: "disable automatic buildkitd management",
		},
		&cli.StringFlag{
			Name:  "progress",
			Usage: "progress output mode (auto, tui, plain, quiet)",
			Value: "auto",
		},
	}
}

func (a *app) validateAction(_ context.Context, cmd *cli.Command) error {
	path := cmd.Args().First()
	if path == "" {
		return errors.New("usage: cicada validate <file>")
	}

	p, err := a.parse(path)
	if err != nil {
		return fmt.Errorf("parsing %s: %w", path, err)
	}

	if _, err := p.Validate(); err != nil {
		return fmt.Errorf("validating %s: %w", path, err)
	}

	a.printPipelineSummary(p.Name, p.Jobs)
	return nil
}

// buildNoCacheFilter converts CLI filter names into a set and warns about
// names that don't match any pipeline job.
func buildNoCacheFilter(ctx context.Context, filters []string, p pipeline.Pipeline) map[string]struct{} {
	if len(filters) == 0 {
		return nil
	}
	set := make(map[string]struct{}, len(filters))
	for _, name := range filters {
		set[name] = struct{}{}
	}
	jobNames := make(map[string]struct{}, len(p.Jobs))
	for i := range p.Jobs {
		jobNames[p.Jobs[i].Name] = struct{}{}
	}
	for name := range set {
		if _, ok := jobNames[name]; !ok {
			slogctx.FromContext(ctx).LogAttrs(ctx, slog.LevelWarn, "no-cache-filter: no job matches", slog.String("name", name))
		}
	}
	return set
}

// buildDockerExportFilter converts CLI flag values into a set of job names
// whose published images should be loaded into the local Docker daemon.
// A value of "*" selects all jobs with a publish node.
func buildDockerExportFilter(ctx context.Context, flagValues []string, pubs []runner.ImagePublish) map[string]struct{} {
	if len(flagValues) == 0 {
		return nil
	}
	set := make(map[string]struct{}, len(flagValues))
	for _, v := range flagValues {
		if v == "*" {
			all := make(map[string]struct{}, len(pubs))
			for i := range pubs {
				all[pubs[i].JobName] = struct{}{}
			}
			return all
		}
		set[v] = struct{}{}
	}
	pubJobs := make(map[string]struct{}, len(pubs))
	for i := range pubs {
		pubJobs[pubs[i].JobName] = struct{}{}
	}
	for name := range set {
		if _, ok := pubJobs[name]; !ok {
			slogctx.FromContext(ctx).LogAttrs(ctx, slog.LevelWarn,
				"with-docker-export: no publish job matches", slog.String("name", name))
		}
	}
	return set
}

// buildImagePublishes converts builder image exports into runner ImagePublish
// values, applying the --with-docker-export filter to set ExportDocker.
func buildImagePublishes(ctx context.Context, exports []builder.ImageExport, dockerExportFlags []string) []runner.ImagePublish {
	pubs := make([]runner.ImagePublish, len(exports))
	for i, ie := range exports {
		pubs[i] = runner.ImagePublish{
			Definition: ie.Definition,
			JobName:    ie.JobName,
			Image:      ie.Publish.Image,
			Push:       ie.Publish.Push,
			Insecure:   ie.Publish.Insecure,
			Platform:   ie.Platform,
		}
	}
	filter := buildDockerExportFilter(ctx, dockerExportFlags, pubs)
	for i := range pubs {
		if _, ok := filter[pubs[i].JobName]; ok {
			pubs[i].ExportDocker = true
		}
	}
	return pubs
}

func (a *app) runAction(ctx context.Context, cmd *cli.Command) error {
	path := cmd.Args().First()
	if path == "" {
		return errors.New("usage: cicada run <file>")
	}

	if cmd.Int("parallelism") < 0 {
		return fmt.Errorf("invalid value %d for flag --parallelism: must be >= 0", cmd.Int("parallelism"))
	}

	p, err := a.parse(path)
	if err != nil {
		return fmt.Errorf("parsing %s: %w", path, err)
	}

	filterOpts := pipeline.FilterOpts{
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
		return err
	}

	// Parse cache export/import specs.
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

	exports := make([]runner.Export, len(result.Exports))
	for i, exp := range result.Exports {
		exports[i] = runner.Export{
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
	pipe           pipeline.Pipeline
	jobs           []runner.Job
	exports        []runner.Export
	imagePublishes []runner.ImagePublish
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
func resolveSecrets(ctx context.Context, decls []pipeline.SecretDecl) ([]session.Attachable, map[string]string, error) {
	if len(decls) == 0 {
		return nil, nil, nil
	}
	resolved, err := secret.Resolve(ctx, decls)
	if err != nil {
		return nil, nil, fmt.Errorf("resolving secrets: %w", err)
	}
	attachables := []session.Attachable{secretsprovider.FromMap(secret.ToMap(resolved))}
	return attachables, secret.PlaintextValues(resolved), nil
}

// executePipeline connects to BuildKit and runs the pipeline jobs.
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

	contextFS, err := fsutil.NewFS(params.cwd)
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

	for _, name := range params.skippedJobs {
		display.Send(progress.JobSkippedMsg{Job: name})
	}

	runErr := runner.Run(ctx, runner.RunInput{
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

func (a *app) printDryRun(name string, jobs []runner.Job) {
	_, _ = fmt.Fprintf(a.stdout, "Pipeline '%s' is valid\n", name)
	_, _ = fmt.Fprintf(a.stdout, "  Jobs: %d\n", len(jobs))
	for _, j := range jobs {
		ops := 0
		if j.Definition != nil {
			ops = len(j.Definition.Def)
		}
		if len(j.DependsOn) > 0 {
			_, _ = fmt.Fprintf(a.stdout, "    - %s (%d LLB ops, depends: %s)\n",
				j.Name, ops, strings.Join(j.DependsOn, ", "))
		} else {
			_, _ = fmt.Fprintf(a.stdout, "    - %s (%d LLB ops)\n", j.Name, ops)
		}
	}
}

func (a *app) printPipelineSummary(name string, jobs []pipeline.Job) {
	_, _ = fmt.Fprintf(a.stdout, "Pipeline '%s' is valid\n", name)
	_, _ = fmt.Fprintf(a.stdout, "  Jobs: %d\n", len(jobs))
	for _, j := range jobs {
		if len(j.DependsOn) > 0 {
			_, _ = fmt.Fprintf(a.stdout, "    - %s (image: %s, depends: %s)\n",
				j.Name, j.Image, strings.Join(j.DependsOn, ", "))
		} else {
			_, _ = fmt.Fprintf(a.stdout, "    - %s (image: %s)\n", j.Name, j.Image)
		}
		for _, s := range j.Steps {
			_, _ = fmt.Fprintf(a.stdout, "      - %s\n", s.Name)
		}
	}
}

// pipelineJobInfo holds pre-indexed per-job metadata from the pipeline.
type pipelineJobInfo struct {
	DependsOn []string
	When      runner.DeferredEvaluator // deferred condition; nil = no deferred condition
	Env       map[string]string        // pipeline-scoped env for deferred condition evaluation
	Matrix    map[string]string        // matrix dimension values for deferred condition evaluation
	Timeout   time.Duration            // job-level timeout
	Retry     *pipeline.Retry          // job-level retry config
}

// buildRunnerJobs converts a builder.Result into a slice of runner.Job,
// carrying dependency information from the pipeline. Dependencies are resolved
// by name rather than positional index, so builder output ordering need not
// match pipeline declaration order.
func buildRunnerJobs(r builder.Result, p pipeline.Pipeline, skippedSteps map[string][]string) ([]runner.Job, error) {
	if len(r.Definitions) != len(r.JobNames) {
		return nil, fmt.Errorf("%w: %d definitions, %d job names",
			errResultMismatch, len(r.Definitions), len(r.JobNames))
	}

	// Index pipeline jobs by name for dependency + deferred when lookup.
	pipelineIdx := make(map[string]pipelineJobInfo, len(p.Jobs))
	for i := range p.Jobs {
		info := pipelineJobInfo{
			DependsOn: p.Jobs[i].DependsOn,
			Timeout:   p.Jobs[i].Timeout,
			Retry:     p.Jobs[i].Retry,
		}
		if p.Jobs[i].When != nil && p.Jobs[i].When.Deferred {
			info.When = p.Jobs[i].When
			info.Env = pipeline.BuildEnvScope(p.Env, p.Jobs[i].Env)
			info.Matrix = p.Jobs[i].MatrixValues
		}
		pipelineIdx[p.Jobs[i].Name] = info
	}

	jobs := make([]runner.Job, len(r.Definitions))
	for i := range r.Definitions {
		info, ok := pipelineIdx[r.JobNames[i]]
		if !ok {
			return nil, fmt.Errorf("%w: %q", errUnknownBuilderJob, r.JobNames[i])
		}
		jobs[i] = runner.Job{
			Name:         r.JobNames[i],
			Definition:   r.Definitions[i],
			DependsOn:    info.DependsOn,
			When:         info.When,
			Env:          info.Env,
			Matrix:       info.Matrix,
			OutputDef:    r.OutputDefs[r.JobNames[i]],
			SkippedSteps: skippedSteps[r.JobNames[i]],
			Timeout:      info.Timeout,
			Retry:        info.Retry,
			StepTimeouts: r.StepTimeouts[r.JobNames[i]],
		}
	}
	return jobs, nil
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
		return errors.New("usage: cicada pull <file>")
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

func checkOffline(ctx context.Context, solver runner.Solver, p pipeline.Pipeline, path string) error {
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

func (a *app) selectDisplay(mode string, boring bool) (progress.Display, error) {
	switch mode {
	case "auto":
		if a.isTTY && a.format == "pretty" {
			return tui.New(boring), nil
		}
		return plain.New(), nil
	case "tui":
		return tui.New(boring), nil
	case "plain":
		return plain.New(), nil
	case "quiet":
		return quiet.New(), nil
	default:
		return nil, fmt.Errorf("unknown progress mode %q (valid: auto, tui, plain, quiet)", mode)
	}
}

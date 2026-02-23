package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"sync"

	"github.com/containerd/platforms"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/exporter/containerimage/exptypes"
	gateway "github.com/moby/buildkit/frontend/gateway/client"
	"golang.org/x/sync/errgroup"

	"github.com/ndisidore/cicada/internal/progress/progressmodel"
	rm "github.com/ndisidore/cicada/internal/runner/runnermodel"
	"github.com/ndisidore/cicada/pkg/pipeline/pipelinemodel"
)

// solveExport solves an export LLB definition using the local exporter to
// write files to the host filesystem.
func solveExport(ctx context.Context, exp rm.Export, cfg runConfig) error {
	if exp.Definition == nil {
		return fmt.Errorf("export %q from job %q: %w", exp.Local, exp.JobName, rm.ErrNilDefinition)
	}
	if exp.Local == "" {
		return fmt.Errorf("export from job %q: %w", exp.JobName, pipelinemodel.ErrEmptyExportLocal)
	}
	outputDir := filepath.Dir(exp.Local)
	if exp.Dir {
		outputDir = exp.Local
	}

	ch := make(chan *client.SolveStatus)
	displayName := "export:" + exp.JobName
	displayCh := teeStatus(ctx, ch, cfg.collector, cfg.secretValues, displayName)

	cfg.sender.Send(progressmodel.JobAddedMsg{Job: displayName})
	var wg sync.WaitGroup
	wg.Go(bridgeStatus(ctx, cfg.sender, displayName, displayCh))

	_, err := cfg.solver.Solve(ctx, exp.Definition, client.SolveOpt{
		Exports: []client.ExportEntry{{
			Type:      client.ExporterLocal,
			OutputDir: outputDir,
		}},
		CacheExports: cfg.cacheExports,
		CacheImports: cfg.cacheImports,
		Session:      cfg.session,
	}, ch)

	wg.Wait()

	if err != nil {
		return fmt.Errorf("solving export: %w", err)
	}
	return nil
}

// runPublishes groups image publishes and solves them concurrently. Single-variant
// groups use the simple image exporter; multi-variant groups use the gateway Build API.
//
//revive:disable-next-line:cognitive-complexity runPublishes is a flat dispatch over group size and export flags; splitting it hurts readability.
func runPublishes(ctx context.Context, in rm.RunInput, cfg runConfig) error {
	if len(in.ImagePublishes) == 0 {
		return nil
	}

	groups, err := groupPublishes(in.ImagePublishes)
	if err != nil {
		return fmt.Errorf("grouping image publishes: %w", err)
	}
	for _, grp := range groups {
		if len(grp.Variants) > 1 && grp.ExportDocker {
			return fmt.Errorf("image %q: %w", grp.Image, rm.ErrExportDockerMultiPlatform)
		}
	}

	pg, pctx := errgroup.WithContext(ctx)
	for _, grp := range groups {
		if len(grp.Variants) > 1 {
			pg.Go(func() error {
				if err := solveMultiPlatformPublish(pctx, grp, cfg); err != nil {
					return fmt.Errorf("publishing multi-platform %q: %w", grp.Image, err)
				}
				return nil
			})
			continue
		}
		pub := grp.Variants[0]
		switch {
		case pub.Push && pub.ExportDocker:
			pg.Go(func() error {
				eg, ectx := errgroup.WithContext(pctx)
				eg.Go(func() error {
					if err := solveImagePublish(ectx, pub, cfg); err != nil {
						return fmt.Errorf("publishing %q from job %q: %w", grp.Image, pub.JobName, err)
					}
					return nil
				})
				eg.Go(func() error {
					if err := solveImageExportDocker(ectx, pub, cfg); err != nil {
						return fmt.Errorf("export-docker %q from job %q: %w", grp.Image, pub.JobName, err)
					}
					return nil
				})
				return eg.Wait()
			})
		case pub.ExportDocker:
			pg.Go(func() error {
				if err := solveImageExportDocker(pctx, pub, cfg); err != nil {
					return fmt.Errorf("export-docker %q from job %q: %w", grp.Image, pub.JobName, err)
				}
				return nil
			})
		default:
			pg.Go(func() error {
				if err := solveImagePublish(pctx, pub, cfg); err != nil {
					return fmt.Errorf("publishing %q from job %q: %w", grp.Image, pub.JobName, err)
				}
				return nil
			})
		}
	}
	return pg.Wait()
}

// publishGroup groups image publish variants targeting the same image reference.
type publishGroup struct {
	Image        string
	Push         bool
	Insecure     bool
	ExportDocker bool
	Variants     []rm.ImagePublish
}

// groupPublishes groups image publishes by image reference. All variants
// targeting the same image must agree on Push, Insecure, and ExportDocker;
// a mismatch returns ErrPublishSettingConflict. Single-variant groups use
// the simple image exporter; multi-variant groups use the gateway Build API.
func groupPublishes(pubs []rm.ImagePublish) ([]publishGroup, error) {
	idx := make(map[string]int, len(pubs))
	var groups []publishGroup
	for _, pub := range pubs {
		if i, ok := idx[pub.Image]; ok {
			grp := &groups[i]
			if pub.Push != grp.Push || pub.Insecure != grp.Insecure || pub.ExportDocker != grp.ExportDocker {
				return nil, fmt.Errorf("image %q: %w", pub.Image, rm.ErrPublishSettingConflict)
			}
			grp.Variants = append(grp.Variants, pub)
			continue
		}
		idx[pub.Image] = len(groups)
		groups = append(groups, publishGroup{
			Image:        pub.Image,
			Push:         pub.Push,
			Insecure:     pub.Insecure,
			ExportDocker: pub.ExportDocker,
			Variants:     []rm.ImagePublish{pub},
		})
	}
	return groups, nil
}

// validateVariants checks that all variants have non-nil definitions and unique
// platforms. Returns ErrNilDefinition or ErrDuplicatePlatform on failure.
func validateVariants(variants []rm.ImagePublish) error {
	seen := make(map[string]string, len(variants))
	for _, v := range variants {
		if v.Definition == nil {
			return fmt.Errorf("variant %q (%s): %w", v.JobName, v.Platform, rm.ErrNilDefinition)
		}
		plat, err := platforms.Parse(v.Platform)
		if err != nil {
			return fmt.Errorf("parsing platform %q for job %q: %w", v.Platform, v.JobName, err)
		}
		pid := platforms.Format(plat)
		if prev, ok := seen[pid]; ok {
			return fmt.Errorf("platform %q: jobs %q and %q: %w", pid, prev, v.JobName, rm.ErrDuplicatePlatform)
		}
		seen[pid] = v.JobName
	}
	return nil
}

// multiPlatformBuildFunc returns a gateway.BuildFunc that solves each variant's
// definition and assembles them into a multi-platform result with platform metadata.
func multiPlatformBuildFunc(variants []rm.ImagePublish) gateway.BuildFunc {
	return func(ctx context.Context, c gateway.Client) (*gateway.Result, error) {
		if err := validateVariants(variants); err != nil {
			return nil, err
		}

		res := gateway.NewResult()
		platList := make([]exptypes.Platform, 0, len(variants))
		for _, v := range variants {
			plat, err := platforms.Parse(v.Platform)
			if err != nil {
				return nil, fmt.Errorf("parsing platform %q for job %q: %w", v.Platform, v.JobName, err)
			}

			solveRes, err := c.Solve(ctx, gateway.SolveRequest{
				Definition: v.Definition.ToPB(),
			})
			if err != nil {
				return nil, fmt.Errorf("solving variant %q (%s): %w", v.JobName, v.Platform, err)
			}

			ref, err := solveRes.SingleRef()
			if err != nil {
				return nil, fmt.Errorf("getting ref for %q (%s): %w", v.JobName, v.Platform, err)
			}

			platformID := platforms.Format(plat)
			res.AddRef(platformID, ref)
			platList = append(platList, exptypes.Platform{
				ID:       platformID,
				Platform: plat,
			})
		}

		dt, err := json.Marshal(exptypes.Platforms{Platforms: platList})
		if err != nil {
			return nil, fmt.Errorf("marshaling platform metadata: %w", err)
		}
		res.AddMeta(exptypes.ExporterPlatformsKey, dt)

		return res, nil
	}
}

// solveMultiPlatformPublish assembles multiple platform variants into a manifest
// list using the gateway Build API.
func solveMultiPlatformPublish(ctx context.Context, grp publishGroup, cfg runConfig) error {
	attrs := map[string]string{"name": grp.Image}
	if grp.Push {
		attrs["push"] = "true"
	}
	if grp.Insecure {
		attrs["registry.insecure"] = "true"
	}

	ch := make(chan *client.SolveStatus)
	displayName := "publish:" + grp.Image
	displayCh := teeStatus(ctx, ch, cfg.collector, cfg.secretValues, displayName)

	cfg.sender.Send(progressmodel.JobAddedMsg{Job: displayName})
	var wg sync.WaitGroup
	wg.Go(bridgeStatus(ctx, cfg.sender, displayName, displayCh))

	_, err := cfg.solver.Build(ctx, client.SolveOpt{
		Exports: []client.ExportEntry{{
			Type:  client.ExporterImage,
			Attrs: attrs,
		}},
		CacheExports: cfg.cacheExports,
		CacheImports: cfg.cacheImports,
		Session:      cfg.session,
	}, "", multiPlatformBuildFunc(grp.Variants), ch)

	wg.Wait()

	if err != nil {
		return fmt.Errorf("solving multi-platform publish: %w", err)
	}
	return nil
}

// solveImagePublish solves a single-platform image publish using the image exporter.
func solveImagePublish(ctx context.Context, pub rm.ImagePublish, cfg runConfig) error {
	if pub.Definition == nil {
		return fmt.Errorf("publish %q from job %q: %w", pub.Image, pub.JobName, rm.ErrNilDefinition)
	}

	attrs := map[string]string{"name": pub.Image}
	if pub.Push {
		attrs["push"] = "true"
	}
	if pub.Insecure {
		attrs["registry.insecure"] = "true"
	}

	ch := make(chan *client.SolveStatus)
	displayName := "publish:" + pub.JobName
	displayCh := teeStatus(ctx, ch, cfg.collector, cfg.secretValues, displayName)

	cfg.sender.Send(progressmodel.JobAddedMsg{Job: displayName})
	var wg sync.WaitGroup
	wg.Go(bridgeStatus(ctx, cfg.sender, displayName, displayCh))

	_, err := cfg.solver.Solve(ctx, pub.Definition, client.SolveOpt{
		Exports: []client.ExportEntry{{
			Type:  client.ExporterImage,
			Attrs: attrs,
		}},
		CacheExports: cfg.cacheExports,
		CacheImports: cfg.cacheImports,
		Session:      cfg.session,
	}, ch)

	wg.Wait()

	if err != nil {
		return fmt.Errorf("solving publish: %w", err)
	}
	return nil
}

// solveImageExportDocker solves an image definition using the Docker exporter,
// piping the tarball directly into `docker load` via an io.Pipe.
func solveImageExportDocker(ctx context.Context, pub rm.ImagePublish, cfg runConfig) error {
	if pub.Definition == nil {
		return fmt.Errorf("export-docker %q from job %q: %w", pub.Image, pub.JobName, rm.ErrNilDefinition)
	}

	pr, pw := io.Pipe()

	ch := make(chan *client.SolveStatus)
	displayName := "export-docker:" + pub.JobName
	displayCh := teeStatus(ctx, ch, cfg.collector, cfg.secretValues, displayName)

	cfg.sender.Send(progressmodel.JobAddedMsg{Job: displayName})
	var bridgeWg sync.WaitGroup
	bridgeWg.Go(bridgeStatus(ctx, cfg.sender, displayName, displayCh))

	eg, ectx := errgroup.WithContext(ctx)

	eg.Go(func() error {
		defer func() { _ = pr.Close() }()
		if err := cfg.rt.LoadImage(ectx, pr); err != nil {
			return fmt.Errorf("loading image: %w", err)
		}
		return nil
	})

	eg.Go(func() error {
		defer func() { _ = pw.Close() }()
		_, err := cfg.solver.Solve(ectx, pub.Definition, client.SolveOpt{
			Exports: []client.ExportEntry{{
				Type:  client.ExporterDocker,
				Attrs: map[string]string{"name": pub.Image},
				Output: func(_ map[string]string) (io.WriteCloser, error) {
					return pw, nil
				},
			}},
			CacheExports: cfg.cacheExports,
			CacheImports: cfg.cacheImports,
			Session:      cfg.session,
		}, ch)
		if err != nil {
			return fmt.Errorf("solving export-docker: %w", err)
		}
		return nil
	})

	err := eg.Wait()
	bridgeWg.Wait()
	return err
}

// Package imagestore provides image pre-pull and cache verification for offline mode.
package imagestore

import (
	"context"
	"fmt"

	"github.com/containerd/errdefs"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/client/llb"
	"golang.org/x/sync/errgroup"

	"github.com/ndisidore/cicada/internal/progress"
)

// Solver abstracts the BuildKit Solve RPC for testability.
//
// Channel close contract: the status channel passed to Solve is owned by the caller
// of Solve (e.g. pullImage, isImageCached) until the implementer closes it. Implementations
// of Solver MUST close the provided status channel when Solve returns or completes, so that
// consumers such as display.Attach and collectVertexCachedStates do not hang.
type Solver interface {
	// Solve runs the LLB definition. The implementer must close statusChan when
	// Solve returns or completes; ownership of the channel remains with the
	// caller of Solve until it is closed.
	Solve(ctx context.Context, def *llb.Definition, opt client.SolveOpt, statusChan chan *client.SolveStatus) (*client.SolveResponse, error)
}

// PullImages pulls all images into the BuildKit cache by solving minimal LLB
// definitions. Each image is pulled sequentially to provide clear progress.
func PullImages(ctx context.Context, c Solver, images []string, display progress.Display) error {
	for _, ref := range images {
		if err := pullImage(ctx, c, ref, display); err != nil {
			return fmt.Errorf("pulling %q: %w", ref, err)
		}
	}
	return nil
}

func pullImage(ctx context.Context, c Solver, ref string, display progress.Display) error {
	st := llb.Image(ref)
	def, err := st.Marshal(ctx)
	if err != nil {
		return fmt.Errorf("marshaling image definition: %w", err)
	}

	ch := make(chan *client.SolveStatus)

	if err := display.Attach(ctx, "pull "+ref, ch, nil); err != nil {
		close(ch)
		return fmt.Errorf("attaching pull display: %w", err)
	}

	_, err = c.Solve(ctx, def, client.SolveOpt{}, ch)
	if err != nil {
		return fmt.Errorf("solving image pull: %w", err)
	}
	return nil
}

// CheckCached verifies all images exist in the BuildKit cache.
// Returns a list of image refs that are not cached.
func CheckCached(ctx context.Context, c Solver, images []string) ([]string, error) {
	missing := make([]string, 0)
	for _, ref := range images {
		cached, err := isImageCached(ctx, c, ref)
		if err != nil {
			return nil, fmt.Errorf("checking cache for %q: %w", ref, err)
		}
		if !cached {
			missing = append(missing, ref)
		}
	}
	return missing, nil
}

// isImageCached solves a minimal LLB for the image and checks whether
// all vertices were satisfied from cache. BuildKit may report vertices
// across multiple status messages, so we track the final state per digest.
func isImageCached(ctx context.Context, c Solver, ref string) (bool, error) {
	st := llb.Image(ref)
	def, err := st.Marshal(ctx)
	if err != nil {
		return false, fmt.Errorf("marshaling: %w", err)
	}

	ch := make(chan *client.SolveStatus)

	// Safe without a mutex: collectVertexCachedStates is the sole writer (via goroutine),
	// and reads happen only after the errgroup completes.
	vertices := make(map[string]bool)

	var g errgroup.Group
	g.Go(func() error {
		collectVertexCachedStates(ch, vertices)
		return nil
	})

	_, err = c.Solve(ctx, def, client.SolveOpt{}, ch)

	// Wait for the collector to finish draining.
	_ = g.Wait()

	if err != nil {
		if isImageNotFoundErr(err) {
			return false, nil
		}
		return false, fmt.Errorf("checking image cache: %w", err)
	}

	if len(vertices) == 0 {
		return false, nil
	}

	for _, cached := range vertices {
		if !cached {
			return false, nil
		}
	}
	return true, nil
}

// collectVertexCachedStates drains the status channel and records
// the final cached state for each vertex digest.
func collectVertexCachedStates(ch <-chan *client.SolveStatus, vertices map[string]bool) {
	for status := range ch {
		for _, v := range status.Vertexes {
			key := v.Digest.String()
			if v.Cached {
				vertices[key] = true
			} else if _, ok := vertices[key]; !ok {
				vertices[key] = false
			}
		}
	}
}

// isImageNotFoundErr checks whether an error indicates the image
// could not be found or accessed.
func isImageNotFoundErr(err error) bool {
	return errdefs.IsNotFound(err) ||
		errdefs.IsUnauthorized(err) ||
		errdefs.IsPermissionDenied(err)
}

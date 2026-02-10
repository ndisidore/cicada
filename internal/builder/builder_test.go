package builder

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/moby/buildkit/client/llb/sourceresolver"
	"github.com/moby/buildkit/solver/pb"
	digest "github.com/opencontainers/go-digest"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ndisidore/cicada/pkg/pipeline"
)

// mockMetaResolver returns a fixed OCI image config for any image reference.
type mockMetaResolver struct {
	config ocispecs.Image
}

//revive:disable-next-line:function-result-limit // signature dictated by sourceresolver.ImageMetaResolver interface
func (m *mockMetaResolver) ResolveImageConfig(_ context.Context, ref string, _ sourceresolver.Opt) (string, digest.Digest, []byte, error) {
	dt, err := json.Marshal(m.config)
	if err != nil {
		return "", "", nil, err
	}
	return ref, "", dt, nil
}

// execMounts returns the mounts from the first ExecOp found in the definition.
func execMounts(t *testing.T, defBytes [][]byte) []*pb.Mount {
	t.Helper()
	for _, raw := range defBytes {
		var op pb.Op
		require.NoError(t, op.UnmarshalVT(raw))
		if exec := op.GetExec(); exec != nil {
			return exec.GetMounts()
		}
	}
	t.Fatal("no ExecOp found in definition")
	return nil
}

// execMeta returns the Meta from the first ExecOp found in the definition.
func execMeta(t *testing.T, defBytes [][]byte) *pb.Meta {
	t.Helper()
	for _, raw := range defBytes {
		var op pb.Op
		require.NoError(t, op.UnmarshalVT(raw))
		if exec := op.GetExec(); exec != nil {
			return exec.GetMeta()
		}
	}
	t.Fatal("no ExecOp found in definition")
	return nil
}

func TestBuild(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		p         pipeline.Pipeline
		opts      BuildOpts
		wantSteps int
		wantErr   error
		verify    func(t *testing.T, result Result)
	}{
		{
			name: "single step",
			p: pipeline.Pipeline{
				Name: "hello",
				Steps: []pipeline.Step{
					{
						Name:  "greet",
						Image: "alpine:latest",
						Run:   []string{"echo hello"},
					},
				},
			},
			wantSteps: 1,
		},
		{
			name: "multi step with dependency",
			p: pipeline.Pipeline{
				Name: "build",
				Steps: []pipeline.Step{
					{
						Name:  "setup",
						Image: "alpine:latest",
						Run:   []string{"echo setup"},
					},
					{
						Name:      "test",
						Image:     "golang:1.23",
						DependsOn: []string{"setup"},
						Run:       []string{"go test ./..."},
					},
				},
			},
			wantSteps: 2,
		},
		{
			name: "step with cache mount",
			p: pipeline.Pipeline{
				Name: "cached",
				Steps: []pipeline.Step{
					{
						Name:  "build",
						Image: "golang:1.23",
						Run:   []string{"go build ./..."},
						Caches: []pipeline.Cache{
							{ID: "go-build", Target: "/root/.cache/go-build"},
						},
					},
				},
			},
			wantSteps: 1,
			verify: func(t *testing.T, result Result) {
				t.Helper()
				mounts := execMounts(t, result.Definitions[0].Def)
				var found bool
				for _, m := range mounts {
					if m.GetMountType() == pb.MountType_CACHE &&
						m.GetDest() == "/root/.cache/go-build" &&
						m.GetCacheOpt().GetID() == "go-build" {
						found = true
						break
					}
				}
				assert.True(t, found, "expected cache mount at /root/.cache/go-build with ID go-build")
			},
		},
		{
			name: "step with bind mount read-write",
			p: pipeline.Pipeline{
				Name: "mounted",
				Steps: []pipeline.Step{
					{
						Name:    "build",
						Image:   "rust:1.76",
						Run:     []string{"cargo build"},
						Workdir: "/src",
						Mounts: []pipeline.Mount{
							{Source: ".", Target: "/src"},
						},
					},
				},
			},
			wantSteps: 1,
			verify: func(t *testing.T, result Result) {
				t.Helper()
				mounts := execMounts(t, result.Definitions[0].Def)
				for _, m := range mounts {
					if m.GetDest() == "/src" && m.GetSelector() == "." {
						assert.False(t, m.GetReadonly(), "mount at /src should be read-write")
						return
					}
				}
				t.Fatal("expected bind mount at /src with selector '.'")
			},
		},
		{
			name: "step with bind mount readonly",
			p: pipeline.Pipeline{
				Name: "mounted-ro",
				Steps: []pipeline.Step{
					{
						Name:    "build",
						Image:   "rust:1.76",
						Run:     []string{"cargo build"},
						Workdir: "/src",
						Mounts: []pipeline.Mount{
							{Source: ".", Target: "/src", ReadOnly: true},
						},
					},
				},
			},
			wantSteps: 1,
			verify: func(t *testing.T, result Result) {
				t.Helper()
				mounts := execMounts(t, result.Definitions[0].Def)
				for _, m := range mounts {
					if m.GetDest() == "/src" && m.GetSelector() == "." {
						assert.True(t, m.GetReadonly(), "mount at /src should be readonly")
						return
					}
				}
				t.Fatal("expected bind mount at /src with selector '.'")
			},
		},
		{
			name: "multiple run commands joined",
			p: pipeline.Pipeline{
				Name: "multi-cmd",
				Steps: []pipeline.Step{
					{
						Name:  "info",
						Image: "alpine:latest",
						Run:   []string{"uname -a", "date"},
					},
				},
			},
			wantSteps: 1,
		},
		{
			name: "toposort ordering preserved",
			p: pipeline.Pipeline{
				Name: "ordered",
				Steps: []pipeline.Step{
					{
						Name:      "second",
						Image:     "alpine:latest",
						DependsOn: []string{"first"},
						Run:       []string{"echo second"},
					},
					{
						Name:  "first",
						Image: "alpine:latest",
						Run:   []string{"echo first"},
					},
				},
			},
			wantSteps: 2,
		},
		{
			name: "no-cache sets IgnoreCache on all ops",
			opts: BuildOpts{NoCache: true},
			p: pipeline.Pipeline{
				Name: "uncached",
				Steps: []pipeline.Step{
					{
						Name:  "greet",
						Image: "alpine:latest",
						Run:   []string{"echo hello"},
					},
				},
			},
			wantSteps: 1,
			verify: func(t *testing.T, result Result) {
				t.Helper()
				def := result.Definitions[0]
				var imgIgnored, execIgnored bool
				for _, md := range def.Metadata {
					if !md.IgnoreCache {
						continue
					}
					if md.Description["llb.customname"] != "" {
						execIgnored = true
					} else {
						imgIgnored = true
					}
				}
				assert.True(t, imgIgnored, "image op should have IgnoreCache=true")
				assert.True(t, execIgnored, "exec op should have IgnoreCache=true")
			},
		},
		{
			name: "empty run commands",
			p: pipeline.Pipeline{
				Name: "bad",
				Steps: []pipeline.Step{
					{
						Name:  "empty",
						Image: "alpine:latest",
						Run:   nil,
					},
				},
			},
			wantErr: pipeline.ErrMissingRun,
		},
		{
			name: "empty string run commands",
			p: pipeline.Pipeline{
				Name: "bad",
				Steps: []pipeline.Step{
					{
						Name:  "empty-str",
						Image: "alpine:latest",
						Run:   []string{""},
					},
				},
			},
			wantErr: pipeline.ErrMissingRun,
		},
		{
			name: "whitespace-only run commands",
			p: pipeline.Pipeline{
				Name: "bad",
				Steps: []pipeline.Step{
					{
						Name:  "whitespace",
						Image: "alpine:latest",
						Run:   []string{"  "},
					},
				},
			},
			wantErr: pipeline.ErrMissingRun,
		},
		{
			name: "invalid step name rejected",
			p: pipeline.Pipeline{
				Name: "bad",
				Steps: []pipeline.Step{
					{
						Name:  "../escape",
						Image: "alpine:latest",
						Run:   []string{"echo bad"},
					},
				},
			},
			wantErr: pipeline.ErrInvalidName,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result, err := Build(context.Background(), tt.p, tt.opts)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Len(t, result.Definitions, tt.wantSteps)
			assert.Len(t, result.StepNames, tt.wantSteps)
			for i, def := range result.Definitions {
				require.NotNilf(t, def, "definition[%d] is nil", i)
				assert.NotEmptyf(t, def.Def, "definition[%d] has no operations", i)
			}
			if tt.verify != nil {
				tt.verify(t, result)
			}
		})
	}
}

func TestBuildWithPresetTopoOrder(t *testing.T) {
	t.Parallel()

	p := pipeline.Pipeline{
		Name: "cached",
		Steps: []pipeline.Step{
			{
				Name:  "a",
				Image: "alpine:latest",
				Run:   []string{"echo a"},
			},
			{
				Name:  "b",
				Image: "alpine:latest",
				Run:   []string{"echo b"},
			},
			{
				Name:  "c",
				Image: "alpine:latest",
				Run:   []string{"echo c"},
			},
		},
		TopoOrder: []int{0, 1, 2},
	}

	result, err := Build(context.Background(), p, BuildOpts{})
	require.NoError(t, err)
	assert.Equal(t, []string{"a", "b", "c"}, result.StepNames)
	assert.Len(t, result.Definitions, 3)
}

func TestBuildWithInvalidTopoOrder(t *testing.T) {
	t.Parallel()

	steps := []pipeline.Step{
		{
			Name:  "a",
			Image: "alpine:latest",
			Run:   []string{"echo a"},
		},
		{
			Name:  "b",
			Image: "alpine:latest",
			Run:   []string{"echo b"},
		},
		{
			Name:  "c",
			Image: "alpine:latest",
			Run:   []string{"echo c"},
		},
	}

	tests := []struct {
		name  string
		order []int
	}{
		{name: "duplicate indices", order: []int{0, 0, 2}},
		{name: "too short", order: []int{0, 1}},
		{name: "too long", order: []int{0, 1, 2, 3}},
		{name: "out of range", order: []int{0, 1, 5}},
		{name: "negative index", order: []int{-1, 0, 1}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			p := pipeline.Pipeline{
				Name:      "fallback",
				Steps:     steps,
				TopoOrder: tt.order,
			}

			result, err := Build(context.Background(), p, BuildOpts{})
			require.NoError(t, err, "Build should fall back to Validate when TopoOrder is invalid")
			assert.Equal(t, []string{"a", "b", "c"}, result.StepNames)
			assert.Len(t, result.Definitions, 3)
		})
	}
}

func TestBuildTopoSortOrder(t *testing.T) {
	t.Parallel()

	p := pipeline.Pipeline{
		Name: "ordered",
		Steps: []pipeline.Step{
			{
				Name:      "c",
				Image:     "alpine:latest",
				DependsOn: []string{"b"},
				Run:       []string{"echo c"},
			},
			{
				Name:      "b",
				Image:     "alpine:latest",
				DependsOn: []string{"a"},
				Run:       []string{"echo b"},
			},
			{
				Name:  "a",
				Image: "alpine:latest",
				Run:   []string{"echo a"},
			},
		},
	}

	result, err := Build(context.Background(), p, BuildOpts{})
	require.NoError(t, err)
	assert.Equal(t, []string{"a", "b", "c"}, result.StepNames)
}

func TestBuildWithPlatform(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		platform string
		wantOS   string
		wantArch string
	}{
		{
			name:     "linux/arm64 platform constraint",
			platform: "linux/arm64",
			wantOS:   "linux",
			wantArch: "arm64",
		},
		{
			name:     "linux/amd64 platform constraint",
			platform: "linux/amd64",
			wantOS:   "linux",
			wantArch: "amd64",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			p := pipeline.Pipeline{
				Name: "plat",
				Steps: []pipeline.Step{
					{
						Name:     "build",
						Image:    "golang:1.23",
						Platform: tt.platform,
						Run:      []string{"go version"},
					},
				},
			}

			result, err := Build(context.Background(), p, BuildOpts{})
			require.NoError(t, err)
			require.Len(t, result.Definitions, 1)

			// Walk the marshaled ops and find one with a platform constraint.
			var found bool
			for _, raw := range result.Definitions[0].Def {
				var op pb.Op
				require.NoError(t, op.UnmarshalVT(raw))
				if plat := op.GetPlatform(); plat != nil {
					assert.Equal(t, tt.wantOS, plat.GetOS())
					assert.Equal(t, tt.wantArch, plat.GetArchitecture())
					found = true
				}
			}
			assert.True(t, found, "expected at least one op with platform constraint")
		})
	}
}

func TestBuildWithInvalidPlatform(t *testing.T) {
	t.Parallel()

	p := pipeline.Pipeline{
		Name: "bad",
		Steps: []pipeline.Step{
			{
				Name:     "build",
				Image:    "alpine:latest",
				Platform: "not/a/valid/platform/string",
				Run:      []string{"echo hi"},
			},
		},
	}

	_, err := Build(context.Background(), p, BuildOpts{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parsing platform")
}

func TestBuildWithMetaResolver(t *testing.T) {
	t.Parallel()

	resolver := &mockMetaResolver{
		config: ocispecs.Image{
			Config: ocispecs.ImageConfig{
				Env:        []string{"PATH=/usr/local/go/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "GOPATH=/go"},
				WorkingDir: "/go",
			},
		},
	}

	tests := []struct {
		name    string
		p       pipeline.Pipeline
		opts    BuildOpts
		wantEnv []string
		wantCwd string
	}{
		{
			name: "image env and workdir propagated",
			p: pipeline.Pipeline{
				Name: "go-build",
				Steps: []pipeline.Step{
					{
						Name:  "build",
						Image: "golang:1.23",
						Run:   []string{"go version"},
					},
				},
			},
			opts:    BuildOpts{MetaResolver: resolver},
			wantEnv: []string{"PATH=/usr/local/go/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "GOPATH=/go"},
			wantCwd: "/go",
		},
		{
			name: "step workdir overrides image workdir",
			p: pipeline.Pipeline{
				Name: "go-build",
				Steps: []pipeline.Step{
					{
						Name:    "build",
						Image:   "golang:1.23",
						Run:     []string{"go version"},
						Workdir: "/src",
					},
				},
			},
			opts:    BuildOpts{MetaResolver: resolver},
			wantEnv: []string{"PATH=/usr/local/go/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "GOPATH=/go"},
			wantCwd: "/src",
		},
		{
			name: "no resolver omits image config",
			p: pipeline.Pipeline{
				Name: "plain",
				Steps: []pipeline.Step{
					{
						Name:  "build",
						Image: "golang:1.23",
						Run:   []string{"echo hello"},
					},
				},
			},
			opts:    BuildOpts{},
			wantEnv: nil,
			wantCwd: "/",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result, err := Build(context.Background(), tt.p, tt.opts)
			require.NoError(t, err)
			require.Len(t, result.Definitions, 1)

			meta := execMeta(t, result.Definitions[0].Def)
			require.NotNil(t, meta)
			assert.Equal(t, tt.wantEnv, meta.GetEnv())
			assert.Equal(t, tt.wantCwd, meta.GetCwd())
		})
	}
}

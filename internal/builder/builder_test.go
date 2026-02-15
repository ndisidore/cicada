package builder

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/moby/buildkit/client/llb"
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

// lastExecMeta returns the Meta from the last ExecOp in the definition.
// When a job has dependencies, its definition includes dependency ExecOps;
// the job's own ExecOp is the last one in topological order.
func lastExecMeta(t *testing.T, defBytes [][]byte) *pb.Meta {
	t.Helper()
	var last *pb.Meta
	for _, raw := range defBytes {
		var op pb.Op
		require.NoError(t, op.UnmarshalVT(raw))
		if exec := op.GetExec(); exec != nil {
			last = exec.GetMeta()
		}
	}
	require.NotNil(t, last, "no ExecOp found in definition")
	return last
}

// allExecMetas returns the Meta from every ExecOp in the definition, in order.
func allExecMetas(t *testing.T, defBytes [][]byte) []*pb.Meta {
	t.Helper()
	var metas []*pb.Meta
	for _, raw := range defBytes {
		var op pb.Op
		require.NoError(t, op.UnmarshalVT(raw))
		if exec := op.GetExec(); exec != nil {
			metas = append(metas, exec.GetMeta())
		}
	}
	return metas
}

// lastExecMounts returns the mounts from the last ExecOp in the definition.
func lastExecMounts(t *testing.T, defBytes [][]byte) []*pb.Mount {
	t.Helper()
	var last []*pb.Mount
	for _, raw := range defBytes {
		var op pb.Op
		require.NoError(t, op.UnmarshalVT(raw))
		if exec := op.GetExec(); exec != nil {
			last = exec.GetMounts()
		}
	}
	require.NotNil(t, last, "no ExecOp found in definition")
	return last
}

// fileCopyActions returns all FileActionCopy ops found in the definition.
func fileCopyActions(t *testing.T, defBytes [][]byte) []*pb.FileActionCopy {
	t.Helper()
	var copies []*pb.FileActionCopy
	for _, raw := range defBytes {
		var op pb.Op
		require.NoError(t, op.UnmarshalVT(raw))
		if fileOp := op.GetFile(); fileOp != nil {
			for _, action := range fileOp.GetActions() {
				if cp := action.GetCopy(); cp != nil {
					copies = append(copies, cp)
				}
			}
		}
	}
	return copies
}

func TestBuild(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		p        pipeline.Pipeline
		opts     BuildOpts
		wantJobs int
		wantErr  error
		verify   func(t *testing.T, result Result)
	}{
		{
			name: "single job",
			p: pipeline.Pipeline{
				Name: "hello",
				Jobs: []pipeline.Job{
					{
						Name:  "greet",
						Image: "alpine:latest",
						Steps: []pipeline.Step{{Name: "greet", Run: []string{"echo hello"}}},
					},
				},
			},
			wantJobs: 1,
		},
		{
			name: "multi job with dependency",
			p: pipeline.Pipeline{
				Name: "build",
				Jobs: []pipeline.Job{
					{
						Name:  "setup",
						Image: "alpine:latest",
						Steps: []pipeline.Step{{Name: "setup", Run: []string{"echo setup"}}},
					},
					{
						Name:      "test",
						Image:     "golang:1.23",
						DependsOn: []string{"setup"},
						Steps:     []pipeline.Step{{Name: "test", Run: []string{"go test ./..."}}},
					},
				},
			},
			wantJobs: 2,
		},
		{
			name: "job with cache mount",
			p: pipeline.Pipeline{
				Name: "cached",
				Jobs: []pipeline.Job{
					{
						Name:  "build",
						Image: "golang:1.23",
						Caches: []pipeline.Cache{
							{ID: "go-build", Target: "/root/.cache/go-build"},
						},
						Steps: []pipeline.Step{{Name: "build", Run: []string{"go build ./..."}}},
					},
				},
			},
			wantJobs: 1,
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
			name: "job with bind mount read-write",
			p: pipeline.Pipeline{
				Name: "mounted",
				Jobs: []pipeline.Job{
					{
						Name:    "build",
						Image:   "rust:1.76",
						Workdir: "/src",
						Mounts: []pipeline.Mount{
							{Source: ".", Target: "/src"},
						},
						Steps: []pipeline.Step{{Name: "build", Run: []string{"cargo build"}}},
					},
				},
			},
			wantJobs: 1,
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
			name: "job with bind mount readonly",
			p: pipeline.Pipeline{
				Name: "mounted-ro",
				Jobs: []pipeline.Job{
					{
						Name:    "build",
						Image:   "rust:1.76",
						Workdir: "/src",
						Mounts: []pipeline.Mount{
							{Source: ".", Target: "/src", ReadOnly: true},
						},
						Steps: []pipeline.Step{{Name: "build", Run: []string{"cargo build"}}},
					},
				},
			},
			wantJobs: 1,
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
			name: "multiple run commands produce individual ops",
			p: pipeline.Pipeline{
				Name: "multi-cmd",
				Jobs: []pipeline.Job{
					{
						Name:  "info",
						Image: "alpine:latest",
						Steps: []pipeline.Step{{Name: "info", Run: []string{"uname -a", "date"}}},
					},
				},
			},
			wantJobs: 1,
			verify: func(t *testing.T, result Result) {
				t.Helper()
				metas := allExecMetas(t, result.Definitions[0].Def)
				require.Len(t, metas, 2, "expected 2 ExecOps for 2 run commands")
				assert.Equal(t, []string{"/bin/sh", "-c", "uname -a"}, metas[0].GetArgs())
				assert.Equal(t, []string{"/bin/sh", "-c", "date"}, metas[1].GetArgs())
			},
		},
		{
			name: "toposort ordering preserved",
			p: pipeline.Pipeline{
				Name: "ordered",
				Jobs: []pipeline.Job{
					{
						Name:      "second",
						Image:     "alpine:latest",
						DependsOn: []string{"first"},
						Steps:     []pipeline.Step{{Name: "second", Run: []string{"echo second"}}},
					},
					{
						Name:  "first",
						Image: "alpine:latest",
						Steps: []pipeline.Step{{Name: "first", Run: []string{"echo first"}}},
					},
				},
			},
			wantJobs: 2,
		},
		{
			name: "no-cache sets IgnoreCache on all ops",
			opts: BuildOpts{NoCache: true},
			p: pipeline.Pipeline{
				Name: "uncached",
				Jobs: []pipeline.Job{
					{
						Name:  "greet",
						Image: "alpine:latest",
						Steps: []pipeline.Step{{Name: "greet", Run: []string{"echo hello"}}},
					},
				},
			},
			wantJobs: 1,
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
			name: "no-cache-filter applies IgnoreCache to matching job only",
			opts: BuildOpts{NoCacheFilter: map[string]struct{}{"test": {}}},
			p: pipeline.Pipeline{
				Name: "selective",
				Jobs: []pipeline.Job{
					{
						Name:  "build",
						Image: "alpine:latest",
						Steps: []pipeline.Step{{Name: "build", Run: []string{"echo build"}}},
					},
					{
						Name:      "test",
						Image:     "alpine:latest",
						DependsOn: []string{"build"},
						Steps:     []pipeline.Step{{Name: "test", Run: []string{"echo test"}}},
					},
				},
			},
			wantJobs: 2,
			verify: func(t *testing.T, result Result) {
				t.Helper()
				// "build" should NOT have IgnoreCache.
				buildDef := result.Definitions[0]
				for _, md := range buildDef.Metadata {
					assert.False(t, md.IgnoreCache, "build job should not have IgnoreCache")
				}
				// "test" should have IgnoreCache on both image and exec.
				testDef := result.Definitions[1]
				var imgIgnored, execIgnored bool
				for _, md := range testDef.Metadata {
					if !md.IgnoreCache {
						continue
					}
					if md.Description["llb.customname"] != "" {
						execIgnored = true
					} else {
						imgIgnored = true
					}
				}
				assert.True(t, imgIgnored, "test image op should have IgnoreCache=true")
				assert.True(t, execIgnored, "test exec op should have IgnoreCache=true")
			},
		},
		{
			name: "job NoCache field applies IgnoreCache",
			p: pipeline.Pipeline{
				Name: "job-nocache",
				Jobs: []pipeline.Job{
					{
						Name:    "test",
						Image:   "alpine:latest",
						NoCache: true,
						Steps:   []pipeline.Step{{Name: "test", Run: []string{"echo test"}}},
					},
				},
			},
			wantJobs: 1,
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
				Jobs: []pipeline.Job{
					{
						Name:  "empty",
						Image: "alpine:latest",
						Steps: []pipeline.Step{{Name: "empty", Run: nil}},
					},
				},
			},
			wantErr: pipeline.ErrMissingRun,
		},
		{
			name: "empty string run commands",
			p: pipeline.Pipeline{
				Name: "bad",
				Jobs: []pipeline.Job{
					{
						Name:  "empty-str",
						Image: "alpine:latest",
						Steps: []pipeline.Step{{Name: "empty-str", Run: []string{""}}},
					},
				},
			},
			wantErr: pipeline.ErrEmptyRunCommand,
		},
		{
			name: "whitespace-only run commands",
			p: pipeline.Pipeline{
				Name: "bad",
				Jobs: []pipeline.Job{
					{
						Name:  "whitespace",
						Image: "alpine:latest",
						Steps: []pipeline.Step{{Name: "whitespace", Run: []string{"  "}}},
					},
				},
			},
			wantErr: pipeline.ErrEmptyRunCommand,
		},
		{
			name: "invalid job name rejected",
			p: pipeline.Pipeline{
				Name: "bad",
				Jobs: []pipeline.Job{
					{
						Name:  "../escape",
						Image: "alpine:latest",
						Steps: []pipeline.Step{{Name: "escape", Run: []string{"echo bad"}}},
					},
				},
			},
			wantErr: pipeline.ErrInvalidName,
		},
		{
			name: "pipeline-level env vars applied",
			p: pipeline.Pipeline{
				Name: "env-test",
				Env:  []pipeline.EnvVar{{Key: "CI", Value: "true"}},
				Jobs: []pipeline.Job{
					{
						Name:  "build",
						Image: "alpine:latest",
						Steps: []pipeline.Step{{Name: "build", Run: []string{"echo hello"}}},
					},
				},
			},
			wantJobs: 1,
			verify: func(t *testing.T, result Result) {
				t.Helper()
				meta := execMeta(t, result.Definitions[0].Def)
				assert.Contains(t, meta.GetEnv(), "CI=true")
			},
		},
		{
			name: "job-level env vars override pipeline-level",
			p: pipeline.Pipeline{
				Name: "env-override",
				Env:  []pipeline.EnvVar{{Key: "MODE", Value: "default"}},
				Jobs: []pipeline.Job{
					{
						Name:  "build",
						Image: "alpine:latest",
						Env:   []pipeline.EnvVar{{Key: "MODE", Value: "custom"}},
						Steps: []pipeline.Step{{Name: "build", Run: []string{"echo hello"}}},
					},
				},
			},
			wantJobs: 1,
			verify: func(t *testing.T, result Result) {
				t.Helper()
				meta := execMeta(t, result.Definitions[0].Def)
				assert.Contains(t, meta.GetEnv(), "MODE=custom")
				assert.NotContains(t, meta.GetEnv(), "MODE=default")
			},
		},
		{
			name: "CICADA_OUTPUT always set",
			p: pipeline.Pipeline{
				Name: "output-test",
				Jobs: []pipeline.Job{
					{
						Name:  "build",
						Image: "alpine:latest",
						Steps: []pipeline.Step{{Name: "build", Run: []string{"echo hello"}}},
					},
				},
			},
			wantJobs: 1,
			verify: func(t *testing.T, result Result) {
				t.Helper()
				meta := execMeta(t, result.Definitions[0].Def)
				assert.Contains(t, meta.GetEnv(), "CICADA_OUTPUT=/cicada/output")
			},
		},
		{
			name: "dependency output sourcing preamble added",
			p: pipeline.Pipeline{
				Name: "output-sourcing",
				Jobs: []pipeline.Job{
					{
						Name:  "version",
						Image: "alpine:latest",
						Steps: []pipeline.Step{{Name: "version", Run: []string{"echo VERSION=1.0 >> $CICADA_OUTPUT"}}},
					},
					{
						Name:      "build",
						Image:     "alpine:latest",
						DependsOn: []string{"version"},
						Steps:     []pipeline.Step{{Name: "build", Run: []string{"echo $VERSION"}}},
					},
				},
			},
			wantJobs: 2,
			verify: func(t *testing.T, result Result) {
				t.Helper()
				meta := lastExecMeta(t, result.Definitions[1].Def)
				args := meta.GetArgs()
				require.NotEmpty(t, args, "expected non-empty args")
				assert.Contains(t, args[len(args)-1], "for __f in /cicada/deps/*/output")
			},
		},
		{
			name: "preamble prepended to all commands in first step",
			p: pipeline.Pipeline{
				Name: "preamble-multi",
				Jobs: []pipeline.Job{
					{
						Name:  "setup",
						Image: "alpine:latest",
						Steps: []pipeline.Step{{Name: "setup", Run: []string{"echo setup"}}},
					},
					{
						Name:      "build",
						Image:     "alpine:latest",
						DependsOn: []string{"setup"},
						Steps: []pipeline.Step{
							{Name: "build", Run: []string{"echo first", "echo second"}},
							{Name: "verify", Run: []string{"echo third"}},
						},
					},
				},
			},
			wantJobs: 2,
			verify: func(t *testing.T, result Result) {
				t.Helper()
				metas := allExecMetas(t, result.Definitions[1].Def)
				// Dep job contributes 1 ExecOp; build step has 2, verify step has 1.
				require.GreaterOrEqual(t, len(metas), 3, "expected at least 3 ExecOps")
				buildMetas := metas[len(metas)-3:]

				// Both commands in the first step get the preamble.
				firstArgs := buildMetas[0].GetArgs()
				require.GreaterOrEqual(t, len(firstArgs), 3, "expected at least 3 args in first build ExecOp")
				assert.Contains(t, firstArgs[2], "for __f in /cicada/deps/*/output")
				assert.Contains(t, firstArgs[2], "echo first")

				secondArgs := buildMetas[1].GetArgs()
				require.GreaterOrEqual(t, len(secondArgs), 3, "expected at least 3 args in second build ExecOp")
				assert.Contains(t, secondArgs[2], "for __f in /cicada/deps/*/output")
				assert.Contains(t, secondArgs[2], "echo second")

				// Second step does NOT get the preamble.
				assert.Equal(t, []string{"/bin/sh", "-c", "echo third"}, buildMetas[2].GetArgs())
			},
		},
		{
			name: "dep mounts use /cicada/deps/ by default",
			p: pipeline.Pipeline{
				Name: "dep-mounts",
				Jobs: []pipeline.Job{
					{
						Name:  "setup",
						Image: "alpine:latest",
						Steps: []pipeline.Step{{Name: "setup", Run: []string{"echo setup"}}},
					},
					{
						Name:      "test",
						Image:     "alpine:latest",
						DependsOn: []string{"setup"},
						Steps:     []pipeline.Step{{Name: "test", Run: []string{"echo test"}}},
					},
				},
			},
			wantJobs: 2,
			verify: func(t *testing.T, result Result) {
				t.Helper()
				mounts := lastExecMounts(t, result.Definitions[1].Def)
				var cicadaMount, legacyMount bool
				for _, m := range mounts {
					if m.GetDest() == "/cicada/deps/setup" {
						cicadaMount = true
					}
					if m.GetDest() == "/deps/setup" {
						legacyMount = true
					}
				}
				assert.True(t, cicadaMount, "expected /cicada/deps/setup mount")
				assert.False(t, legacyMount, "should not have /deps/setup mount without expose-deps")
			},
		},
		{
			name: "expose-deps adds legacy /deps/ mounts",
			opts: BuildOpts{ExposeDeps: true},
			p: pipeline.Pipeline{
				Name: "legacy-deps",
				Jobs: []pipeline.Job{
					{
						Name:  "setup",
						Image: "alpine:latest",
						Steps: []pipeline.Step{{Name: "setup", Run: []string{"echo setup"}}},
					},
					{
						Name:      "test",
						Image:     "alpine:latest",
						DependsOn: []string{"setup"},
						Steps:     []pipeline.Step{{Name: "test", Run: []string{"echo test"}}},
					},
				},
			},
			wantJobs: 2,
			verify: func(t *testing.T, result Result) {
				t.Helper()
				mounts := lastExecMounts(t, result.Definitions[1].Def)
				var cicadaMount, legacyMount bool
				for _, m := range mounts {
					if m.GetDest() == "/cicada/deps/setup" {
						cicadaMount = true
					}
					if m.GetDest() == "/deps/setup" {
						legacyMount = true
					}
				}
				assert.True(t, cicadaMount, "expected /cicada/deps/setup mount")
				assert.True(t, legacyMount, "expected /deps/setup mount with expose-deps")
			},
		},
		{
			name: "artifact import copies from dependency",
			p: pipeline.Pipeline{
				Name: "artifact-test",
				Jobs: []pipeline.Job{
					{
						Name:  "build",
						Image: "alpine:latest",
						Steps: []pipeline.Step{{Name: "build", Run: []string{"echo build"}}},
					},
					{
						Name:      "test",
						Image:     "alpine:latest",
						DependsOn: []string{"build"},
						Artifacts: []pipeline.Artifact{
							{From: "build", Source: "/out/myapp", Target: "/usr/local/bin/myapp"},
						},
						Steps: []pipeline.Step{{Name: "test", Run: []string{"myapp --version"}}},
					},
				},
			},
			wantJobs: 2,
			verify: func(t *testing.T, result Result) {
				t.Helper()
				copies := fileCopyActions(t, result.Definitions[1].Def)
				require.NotEmpty(t, copies, "expected at least one FileActionCopy for artifact import")
				found := false
				for _, cp := range copies {
					if cp.GetSrc() == "/out/myapp" && cp.GetDest() == "/usr/local/bin/myapp" {
						found = true
						break
					}
				}
				assert.True(t, found, "expected Copy from /out/myapp to /usr/local/bin/myapp")
			},
		},
		{
			name: "job with export produces export definition",
			p: pipeline.Pipeline{
				Name: "export-test",
				Jobs: []pipeline.Job{
					{
						Name:  "build",
						Image: "alpine:latest",
						Steps: []pipeline.Step{{
							Name:    "build",
							Run:     []string{"echo build"},
							Exports: []pipeline.Export{{Path: "/out/myapp", Local: "./bin/myapp"}},
						}},
					},
				},
			},
			wantJobs: 1,
			verify: func(t *testing.T, result Result) {
				t.Helper()
				require.Len(t, result.Exports, 1)
				assert.Equal(t, "build", result.Exports[0].JobName)
				assert.Equal(t, "./bin/myapp", result.Exports[0].Local)
				assert.NotNil(t, result.Exports[0].Definition)
			},
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
			assert.Len(t, result.Definitions, tt.wantJobs)
			assert.Len(t, result.JobNames, tt.wantJobs)
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
		Jobs: []pipeline.Job{
			{
				Name:  "a",
				Image: "alpine:latest",
				Steps: []pipeline.Step{{Name: "a", Run: []string{"echo a"}}},
			},
			{
				Name:  "b",
				Image: "alpine:latest",
				Steps: []pipeline.Step{{Name: "b", Run: []string{"echo b"}}},
			},
			{
				Name:  "c",
				Image: "alpine:latest",
				Steps: []pipeline.Step{{Name: "c", Run: []string{"echo c"}}},
			},
		},
		TopoOrder: []int{0, 1, 2},
	}

	result, err := Build(context.Background(), p, BuildOpts{})
	require.NoError(t, err)
	assert.Equal(t, []string{"a", "b", "c"}, result.JobNames)
	assert.Len(t, result.Definitions, 3)
}

func TestBuildWithInvalidTopoOrder(t *testing.T) {
	t.Parallel()

	makeJobs := func() []pipeline.Job {
		return []pipeline.Job{
			{
				Name:  "a",
				Image: "alpine:latest",
				Steps: []pipeline.Step{{Name: "a", Run: []string{"echo a"}}},
			},
			{
				Name:  "b",
				Image: "alpine:latest",
				Steps: []pipeline.Step{{Name: "b", Run: []string{"echo b"}}},
			},
			{
				Name:  "c",
				Image: "alpine:latest",
				Steps: []pipeline.Step{{Name: "c", Run: []string{"echo c"}}},
			},
		}
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
				Jobs:      makeJobs(),
				TopoOrder: tt.order,
			}

			result, err := Build(context.Background(), p, BuildOpts{})
			require.NoError(t, err, "Build should fall back to Validate when TopoOrder is invalid")
			assert.Equal(t, []string{"a", "b", "c"}, result.JobNames)
			assert.Len(t, result.Definitions, 3)
		})
	}
}

func TestBuildTopoSortOrder(t *testing.T) {
	t.Parallel()

	p := pipeline.Pipeline{
		Name: "ordered",
		Jobs: []pipeline.Job{
			{
				Name:      "c",
				Image:     "alpine:latest",
				DependsOn: []string{"b"},
				Steps:     []pipeline.Step{{Name: "c", Run: []string{"echo c"}}},
			},
			{
				Name:      "b",
				Image:     "alpine:latest",
				DependsOn: []string{"a"},
				Steps:     []pipeline.Step{{Name: "b", Run: []string{"echo b"}}},
			},
			{
				Name:  "a",
				Image: "alpine:latest",
				Steps: []pipeline.Step{{Name: "a", Run: []string{"echo a"}}},
			},
		},
	}

	result, err := Build(context.Background(), p, BuildOpts{})
	require.NoError(t, err)
	assert.Equal(t, []string{"a", "b", "c"}, result.JobNames)
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
				Jobs: []pipeline.Job{
					{
						Name:     "build",
						Image:    "golang:1.23",
						Platform: tt.platform,
						Steps:    []pipeline.Step{{Name: "build", Run: []string{"go version"}}},
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
		Jobs: []pipeline.Job{
			{
				Name:     "build",
				Image:    "alpine:latest",
				Platform: "not/a/valid/platform/string",
				Steps:    []pipeline.Step{{Name: "build", Run: []string{"echo hi"}}},
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
				Jobs: []pipeline.Job{
					{
						Name:  "build",
						Image: "golang:1.23",
						Steps: []pipeline.Step{{Name: "build", Run: []string{"go version"}}},
					},
				},
			},
			opts:    BuildOpts{MetaResolver: resolver},
			wantEnv: []string{"PATH=/usr/local/go/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "GOPATH=/go", "CICADA_OUTPUT=/cicada/output"},
			wantCwd: "/go",
		},
		{
			name: "job workdir overrides image workdir",
			p: pipeline.Pipeline{
				Name: "go-build",
				Jobs: []pipeline.Job{
					{
						Name:    "build",
						Image:   "golang:1.23",
						Workdir: "/src",
						Steps:   []pipeline.Step{{Name: "build", Run: []string{"go version"}}},
					},
				},
			},
			opts:    BuildOpts{MetaResolver: resolver},
			wantEnv: []string{"PATH=/usr/local/go/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "GOPATH=/go", "CICADA_OUTPUT=/cicada/output"},
			wantCwd: "/src",
		},
		{
			name: "no resolver omits image config",
			p: pipeline.Pipeline{
				Name: "plain",
				Jobs: []pipeline.Job{
					{
						Name:  "build",
						Image: "golang:1.23",
						Steps: []pipeline.Step{{Name: "build", Run: []string{"echo hello"}}},
					},
				},
			},
			opts:    BuildOpts{},
			wantEnv: []string{"CICADA_OUTPUT=/cicada/output"},
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

func TestBuildExportDef_invalidPaths(t *testing.T) {
	t.Parallel()

	st := llb.Image("alpine:latest")

	tests := []struct {
		name    string
		path    string
		wantErr string
	}{
		{name: "empty path", path: "", wantErr: "invalid export path"},
		{name: "root path", path: "/", wantErr: "invalid export path"},
		{name: "root with trailing slash", path: "///", wantErr: "invalid export path"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := buildExportDef(context.Background(), st, tt.path)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestBuildExportDef_fileExport(t *testing.T) {
	t.Parallel()

	st := llb.Image("alpine:latest").Run(llb.Args([]string{"touch", "/out/myapp"})).Root()
	def, err := buildExportDef(context.Background(), st, "/out/myapp")
	require.NoError(t, err)
	require.NotNil(t, def)

	meta := lastExecMeta(t, def.Def)
	args := meta.GetArgs()
	assert.Equal(t, []string{"cp", "-a", "/out/myapp", "/cicada/export/myapp"}, args)
}

func TestBuildExportDef_directoryExport(t *testing.T) {
	t.Parallel()

	st := llb.Image("alpine:latest").Run(llb.Args([]string{"mkdir", "-p", "/out/dist"})).Root()
	def, err := buildExportDef(context.Background(), st, "/out/dist/")
	require.NoError(t, err)
	require.NotNil(t, def)

	meta := lastExecMeta(t, def.Def)
	args := meta.GetArgs()
	assert.Equal(t, []string{"cp", "-a", "/out/dist/.", "/cicada/export/"}, args)
}

func TestBuild_directoryExportSetsDir(t *testing.T) {
	t.Parallel()

	p := pipeline.Pipeline{
		Name: "dir-export-test",
		Jobs: []pipeline.Job{
			{
				Name:  "build",
				Image: "alpine:latest",
				Steps: []pipeline.Step{{
					Name:    "build",
					Run:     []string{"mkdir -p /out/dist"},
					Exports: []pipeline.Export{{Path: "/out/dist/", Local: "./output/dist"}},
				}},
			},
		},
	}

	result, err := Build(context.Background(), p, BuildOpts{})
	require.NoError(t, err)
	require.Len(t, result.Exports, 1)
	assert.True(t, result.Exports[0].Dir, "directory export should have Dir=true")
	assert.Equal(t, "./output/dist", result.Exports[0].Local)
}

func TestBuild_imageExportCollected(t *testing.T) {
	t.Parallel()

	p := pipeline.Pipeline{
		Name: "publish-test",
		Jobs: []pipeline.Job{
			{
				Name:    "build",
				Image:   "alpine:latest",
				Publish: &pipeline.Publish{Image: "ghcr.io/user/app:latest", Push: true, Insecure: false},
				Steps:   []pipeline.Step{{Name: "build", Run: []string{"echo build"}}},
			},
		},
	}

	result, err := Build(context.Background(), p, BuildOpts{})
	require.NoError(t, err)
	require.Len(t, result.ImageExports, 1)
	assert.Equal(t, "build", result.ImageExports[0].JobName)
	assert.Equal(t, "ghcr.io/user/app:latest", result.ImageExports[0].Publish.Image)
	assert.True(t, result.ImageExports[0].Publish.Push)
	assert.False(t, result.ImageExports[0].Publish.Insecure)
	assert.NotNil(t, result.ImageExports[0].Definition)
}

func TestBuild_imageExportWithPlatform(t *testing.T) {
	t.Parallel()

	p := pipeline.Pipeline{
		Name: "platform-publish",
		Jobs: []pipeline.Job{
			{
				Name:     "build",
				Image:    "alpine:latest",
				Platform: "linux/arm64",
				Publish:  &pipeline.Publish{Image: "ghcr.io/user/app:latest", Push: true},
				Steps:    []pipeline.Step{{Name: "build", Run: []string{"echo build"}}},
			},
		},
	}

	result, err := Build(context.Background(), p, BuildOpts{})
	require.NoError(t, err)
	require.Len(t, result.ImageExports, 1)
	assert.Equal(t, "linux/arm64", result.ImageExports[0].Platform)
}

func TestBuild_noImageExportWithoutPublish(t *testing.T) {
	t.Parallel()

	p := pipeline.Pipeline{
		Name: "no-publish",
		Jobs: []pipeline.Job{
			{
				Name:  "build",
				Image: "alpine:latest",
				Steps: []pipeline.Step{{Name: "build", Run: []string{"echo build"}}},
			},
		},
	}

	result, err := Build(context.Background(), p, BuildOpts{})
	require.NoError(t, err)
	assert.Empty(t, result.ImageExports)
}

func TestBuild_fileExportClearsDirFlag(t *testing.T) {
	t.Parallel()

	p := pipeline.Pipeline{
		Name: "file-export-test",
		Jobs: []pipeline.Job{
			{
				Name:  "build",
				Image: "alpine:latest",
				Steps: []pipeline.Step{{
					Name:    "build",
					Run:     []string{"echo build"},
					Exports: []pipeline.Export{{Path: "/out/myapp", Local: "./bin/myapp"}},
				}},
			},
		},
	}

	result, err := Build(context.Background(), p, BuildOpts{})
	require.NoError(t, err)
	require.Len(t, result.Exports, 1)
	assert.False(t, result.Exports[0].Dir, "file export should have Dir=false")
}

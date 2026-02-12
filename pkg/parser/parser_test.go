package parser

import (
	"io"
	"path"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ndisidore/cicada/pkg/pipeline"
)

// memResolver is a test Resolver that serves content from an in-memory map.
type memResolver struct {
	files map[string]string // abs path -> KDL content
}

func (m *memResolver) Resolve(source string, basePath string) (io.ReadCloser, string, error) {
	abs := source
	if !path.IsAbs(source) {
		abs = path.Join(basePath, source)
	}
	abs = path.Clean(abs)
	content, ok := m.files[abs]
	if !ok {
		return nil, "", &testNotFoundError{path: abs}
	}
	return io.NopCloser(strings.NewReader(content)), abs, nil
}

type testNotFoundError struct{ path string }

func (e *testNotFoundError) Error() string { return "file not found: " + e.path }

// newTestParser creates a Parser backed by the memResolver.
func newTestParser(files map[string]string) *Parser {
	return &Parser{Resolver: &memResolver{files: files}}
}

// stringParser creates a Parser that only supports ParseString (no file resolution).
func stringParser() *Parser {
	return &Parser{Resolver: &memResolver{files: map[string]string{}}}
}

func TestParse(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   string
		want    pipeline.Pipeline
		wantErr error
	}{
		{
			name: "single step",
			input: `pipeline "hello" {
				step "greet" {
					image "alpine:latest"
					run "echo hello"
				}
			}`,
			want: pipeline.Pipeline{
				Name: "hello",
				Jobs: []pipeline.Job{
					{
						Name:  "greet",
						Image: "alpine:latest",
						Steps: []pipeline.Step{{Name: "greet", Run: []string{"echo hello"}}},
					},
				},
				TopoOrder: []int{0},
			},
		},
		{
			name: "multi step with dependencies",
			input: `pipeline "build" {
				step "setup" {
					image "alpine:latest"
					run "echo setup"
				}
				step "test" {
					image "golang:1.23"
					depends-on "setup"
					run "go test ./..."
				}
			}`,
			want: pipeline.Pipeline{
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
				TopoOrder: []int{0, 1},
			},
		},
		{
			name: "step with mount and cache",
			input: `pipeline "full" {
				step "build" {
					image "rust:1.76"
					mount "." "/src"
					cache "cargo" "/root/.cargo"
					workdir "/src"
					run "cargo build"
				}
			}`,
			want: pipeline.Pipeline{
				Name: "full",
				Jobs: []pipeline.Job{
					{
						Name:    "build",
						Image:   "rust:1.76",
						Workdir: "/src",
						Mounts:  []pipeline.Mount{{Source: ".", Target: "/src"}},
						Caches:  []pipeline.Cache{{ID: "cargo", Target: "/root/.cargo"}},
						Steps:   []pipeline.Step{{Name: "build", Run: []string{"cargo build"}}},
					},
				},
				TopoOrder: []int{0},
			},
		},
		{
			name: "mount with readonly property",
			input: `pipeline "ro" {
				step "build" {
					image "rust:1.76"
					mount "." "/src" readonly=true
					run "cargo build"
				}
			}`,
			want: pipeline.Pipeline{
				Name: "ro",
				Jobs: []pipeline.Job{
					{
						Name:  "build",
						Image: "rust:1.76",
						Mounts: []pipeline.Mount{
							{Source: ".", Target: "/src", ReadOnly: true},
						},
						Steps: []pipeline.Step{{Name: "build", Run: []string{"cargo build"}}},
					},
				},
				TopoOrder: []int{0},
			},
		},
		{
			name: "mount readonly non-boolean",
			input: `pipeline "bad" {
				step "a" {
					image "alpine:latest"
					mount "." "/src" readonly="yes"
					run "echo hi"
				}
			}`,
			wantErr: ErrTypeMismatch,
		},
		{
			name: "multiple run commands",
			input: `pipeline "multi" {
				step "info" {
					image "alpine:latest"
					run "uname -a"
					run "date"
				}
			}`,
			want: pipeline.Pipeline{
				Name: "multi",
				Jobs: []pipeline.Job{
					{
						Name:  "info",
						Image: "alpine:latest",
						Steps: []pipeline.Step{{Name: "info", Run: []string{"uname -a", "date"}}},
					},
				},
				TopoOrder: []int{0},
			},
		},
		{
			name: "unknown top-level node",
			input: `something "foo" {
				image "alpine:latest"
			}`,
			wantErr: ErrUnknownNode,
		},
		{
			name:    "empty file",
			input:   ``,
			wantErr: ErrNoPipeline,
		},
		{
			name: "missing image",
			input: `pipeline "bad" {
				step "noimg" {
					run "echo oops"
				}
			}`,
			wantErr: pipeline.ErrMissingImage,
		},
		{
			name: "duplicate step names",
			input: `pipeline "dup" {
				step "a" {
					image "alpine:latest"
					run "echo 1"
				}
				step "a" {
					image "alpine:latest"
					run "echo 2"
				}
			}`,
			wantErr: pipeline.ErrDuplicateJob,
		},
		{
			name: "unknown dependency",
			input: `pipeline "bad" {
				step "a" {
					image "alpine:latest"
					depends-on "nonexistent"
					run "echo 1"
				}
			}`,
			wantErr: pipeline.ErrUnknownDep,
		},
		{
			name: "pipeline without name",
			input: `pipeline {
				step "a" {
					image "alpine:latest"
					run "echo hi"
				}
			}`,
			wantErr: ErrMissingName,
		},
		{
			name: "empty pipeline",
			input: `pipeline "empty" {
			}`,
			wantErr: pipeline.ErrEmptyPipeline,
		},
		{
			name: "unknown step child node",
			input: `pipeline "bad" {
				step "a" {
					image "alpine:latest"
					run "echo hi"
					foobar "wat"
				}
			}`,
			wantErr: ErrUnknownNode,
		},
		{
			name: "nested step in bare step",
			input: `pipeline "bad" {
				step "outer" {
					image "alpine:latest"
					run "echo outer"
					step "inner" {
						run "echo inner"
					}
				}
			}`,
			wantErr: ErrUnknownNode,
		},
		{
			name: "duplicate image field",
			input: `pipeline "bad" {
				step "a" {
					image "alpine:latest"
					image "ubuntu:latest"
					run "echo hi"
				}
			}`,
			wantErr: ErrDuplicateField,
		},
		{
			name: "duplicate workdir field",
			input: `pipeline "bad" {
				step "a" {
					image "alpine:latest"
					workdir "/a"
					workdir "/b"
					run "echo hi"
				}
			}`,
			wantErr: ErrDuplicateField,
		},
		{
			name: "mount with extra arguments",
			input: `pipeline "bad" {
				step "a" {
					image "alpine:latest"
					mount "a" "b" "c"
					run "echo hi"
				}
			}`,
			wantErr: ErrExtraArgs,
		},
		{
			name: "self dependency",
			input: `pipeline "bad" {
				step "a" {
					image "alpine:latest"
					depends-on "a"
					run "echo hi"
				}
			}`,
			wantErr: pipeline.ErrSelfDependency,
		},
		{
			name: "multiple pipeline nodes",
			input: `pipeline "first" {
				step "a" {
					image "alpine:latest"
					run "echo 1"
				}
			}
			pipeline "second" {
				step "b" {
					image "alpine:latest"
					run "echo 2"
				}
			}`,
			wantErr: ErrMultiplePipelines,
		},
		{
			name: "dependency cycle",
			input: `pipeline "bad" {
				step "a" {
					image "alpine:latest"
					depends-on "b"
					run "echo a"
				}
				step "b" {
					image "alpine:latest"
					depends-on "a"
					run "echo b"
				}
			}`,
			wantErr: pipeline.ErrCycleDetected,
		},
		{
			name: "non-string step field",
			input: `pipeline "bad" {
				step "a" {
					image 42
					run "echo hi"
				}
			}`,
			wantErr: ErrTypeMismatch,
		},
		{
			name: "pipeline-level matrix",
			input: `pipeline "ci" {
				matrix {
					os "linux" "darwin"
				}
				step "build" {
					image "golang:1.23"
					run "GOOS=${matrix.os} go build ./..."
				}
			}`,
			want: pipeline.Pipeline{
				Name: "ci",
				Jobs: []pipeline.Job{
					{
						Name:  "build[os=linux]",
						Image: "golang:1.23",
						Steps: []pipeline.Step{{Name: "build", Run: []string{"GOOS=linux go build ./..."}}},
					},
					{
						Name:  "build[os=darwin]",
						Image: "golang:1.23",
						Steps: []pipeline.Step{{Name: "build", Run: []string{"GOOS=darwin go build ./..."}}},
					},
				},
				TopoOrder: []int{0, 1},
			},
		},
		{
			name: "step-level matrix",
			input: `pipeline "test" {
				step "test" {
					matrix {
						go-version "1.21" "1.22"
					}
					image "golang:${matrix.go-version}"
					run "go test ./..."
				}
			}`,
			want: pipeline.Pipeline{
				Name: "test",
				Jobs: []pipeline.Job{
					{
						Name:  "test[go-version=1.21]",
						Image: "golang:1.21",
						Steps: []pipeline.Step{{Name: "test", Run: []string{"go test ./..."}}},
					},
					{
						Name:  "test[go-version=1.22]",
						Image: "golang:1.22",
						Steps: []pipeline.Step{{Name: "test", Run: []string{"go test ./..."}}},
					},
				},
				TopoOrder: []int{0, 1},
			},
		},
		{
			name: "combined pipeline and step matrix",
			input: `pipeline "ci" {
				matrix {
					os "linux" "darwin"
				}
				step "build" {
					image "golang:1.23"
					run "GOOS=${matrix.os} go build ./..."
				}
				step "test" {
					matrix {
						go-version "1.21" "1.22"
					}
					depends-on "build"
					image "golang:${matrix.go-version}"
					run "GOOS=${matrix.os} go test ./..."
				}
			}`,
			want: pipeline.Pipeline{
				Name: "ci",
				Jobs: []pipeline.Job{
					{
						Name:  "build[os=linux]",
						Image: "golang:1.23",
						Steps: []pipeline.Step{{Name: "build", Run: []string{"GOOS=linux go build ./..."}}},
					},
					{
						Name:      "test[go-version=1.21,os=linux]",
						Image:     "golang:1.21",
						DependsOn: []string{"build[os=linux]"},
						Steps:     []pipeline.Step{{Name: "test", Run: []string{"GOOS=linux go test ./..."}}},
					},
					{
						Name:      "test[go-version=1.22,os=linux]",
						Image:     "golang:1.22",
						DependsOn: []string{"build[os=linux]"},
						Steps:     []pipeline.Step{{Name: "test", Run: []string{"GOOS=linux go test ./..."}}},
					},
					{
						Name:  "build[os=darwin]",
						Image: "golang:1.23",
						Steps: []pipeline.Step{{Name: "build", Run: []string{"GOOS=darwin go build ./..."}}},
					},
					{
						Name:      "test[go-version=1.21,os=darwin]",
						Image:     "golang:1.21",
						DependsOn: []string{"build[os=darwin]"},
						Steps:     []pipeline.Step{{Name: "test", Run: []string{"GOOS=darwin go test ./..."}}},
					},
					{
						Name:      "test[go-version=1.22,os=darwin]",
						Image:     "golang:1.22",
						DependsOn: []string{"build[os=darwin]"},
						Steps:     []pipeline.Step{{Name: "test", Run: []string{"GOOS=darwin go test ./..."}}},
					},
				},
				TopoOrder: []int{0, 1, 2, 3, 4, 5},
			},
		},
		{
			name: "empty matrix block",
			input: `pipeline "bad" {
				matrix {
				}
				step "a" {
					image "alpine:latest"
					run "echo hi"
				}
			}`,
			wantErr: pipeline.ErrEmptyMatrix,
		},
		{
			name: "empty dimension values",
			input: `pipeline "bad" {
				step "a" {
					matrix {
						os
					}
					image "alpine:latest"
					run "echo hi"
				}
			}`,
			wantErr: pipeline.ErrEmptyDimension,
		},
		{
			name: "duplicate pipeline matrix",
			input: `pipeline "bad" {
				matrix {
					os "linux"
				}
				matrix {
					arch "amd64"
				}
				step "a" {
					image "alpine:latest"
					run "echo hi"
				}
			}`,
			wantErr: ErrDuplicateField,
		},
		{
			name: "duplicate step matrix",
			input: `pipeline "bad" {
				step "a" {
					matrix {
						os "linux"
					}
					matrix {
						arch "amd64"
					}
					image "alpine:latest"
					run "echo hi"
				}
			}`,
			wantErr: ErrDuplicateField,
		},
		{
			name: "non-string matrix dimension value",
			input: `pipeline "bad" {
				step "a" {
					matrix {
						count 1 2 3
					}
					image "alpine:latest"
					run "echo hi"
				}
			}`,
			wantErr: ErrTypeMismatch,
		},
		{
			name: "invalid dimension name",
			input: `pipeline "bad" {
				step "a" {
					matrix {
						"os.name" "linux"
					}
					image "alpine:latest"
					run "echo hi"
				}
			}`,
			wantErr: pipeline.ErrInvalidDimName,
		},
		{
			name: "platform field",
			input: `pipeline "plat" {
				step "build" {
					image "golang:1.23"
					platform "linux/arm64"
					run "go version"
				}
			}`,
			want: pipeline.Pipeline{
				Name: "plat",
				Jobs: []pipeline.Job{
					{
						Name:     "build",
						Image:    "golang:1.23",
						Platform: "linux/arm64",
						Steps:    []pipeline.Step{{Name: "build", Run: []string{"go version"}}},
					},
				},
				TopoOrder: []int{0},
			},
		},
		{
			name: "duplicate platform field",
			input: `pipeline "bad" {
				step "a" {
					image "alpine:latest"
					platform "linux/amd64"
					platform "linux/arm64"
					run "echo hi"
				}
			}`,
			wantErr: ErrDuplicateField,
		},
		{
			name: "duplicate dimension name across levels",
			input: `pipeline "bad" {
				matrix {
					os "linux"
				}
				step "a" {
					matrix {
						os "darwin"
					}
					image "alpine:latest"
					run "echo hi"
				}
			}`,
			wantErr: pipeline.ErrDuplicateDim,
		},
		{
			name: "pipeline-level env vars",
			input: `pipeline "ci" {
				env "CI" "true"
				env "GOFLAGS" "-mod=vendor"
				step "build" {
					image "golang:1.23"
					run "go build ./..."
				}
			}`,
			want: pipeline.Pipeline{
				Name: "ci",
				Env:  []pipeline.EnvVar{{Key: "CI", Value: "true"}, {Key: "GOFLAGS", Value: "-mod=vendor"}},
				Jobs: []pipeline.Job{
					{
						Name:  "build",
						Image: "golang:1.23",
						Steps: []pipeline.Step{{Name: "build", Run: []string{"go build ./..."}}},
					},
				},
				TopoOrder: []int{0},
			},
		},
		{
			name: "step-level env vars",
			input: `pipeline "ci" {
				step "build" {
					image "golang:1.23"
					env "CGO_ENABLED" "0"
					run "go build ./..."
				}
			}`,
			want: pipeline.Pipeline{
				Name: "ci",
				Jobs: []pipeline.Job{
					{
						Name:  "build",
						Image: "golang:1.23",
						Env:   []pipeline.EnvVar{{Key: "CGO_ENABLED", Value: "0"}},
						Steps: []pipeline.Step{{Name: "build", Run: []string{"go build ./..."}}},
					},
				},
				TopoOrder: []int{0},
			},
		},
		{
			name: "step export without local",
			input: `pipeline "ci" {
				step "build" {
					image "golang:1.23"
					run "go build -o /out/myapp ./..."
					export "/out/myapp"
				}
			}`,
			wantErr: ErrMissingField,
		},
		{
			name: "step export with empty local",
			input: `pipeline "ci" {
				step "build" {
					image "golang:1.23"
					run "go build -o /out/myapp ./..."
					export "/out/myapp" local=""
				}
			}`,
			wantErr: ErrMissingField,
		},
		{
			name: "step export with local",
			input: `pipeline "ci" {
				step "build" {
					image "golang:1.23"
					run "go build -o /out/myapp ./..."
					export "/out/myapp" local="./bin/myapp"
				}
			}`,
			want: pipeline.Pipeline{
				Name: "ci",
				Jobs: []pipeline.Job{
					{
						Name:    "build",
						Image:   "golang:1.23",
						Exports: []pipeline.Export{{Path: "/out/myapp", Local: "./bin/myapp"}},
						Steps:   []pipeline.Step{{Name: "build", Run: []string{"go build -o /out/myapp ./..."}}},
					},
				},
				TopoOrder: []int{0},
			},
		},
		{
			name: "step artifact",
			input: `pipeline "ci" {
				step "build" {
					image "golang:1.23"
					run "go build -o /out/myapp ./..."
				}
				step "deploy" {
					image "alpine:latest"
					depends-on "build"
					artifact "build" "/out/myapp" "/usr/local/bin/myapp"
					run "echo deploying"
				}
			}`,
			want: pipeline.Pipeline{
				Name: "ci",
				Jobs: []pipeline.Job{
					{
						Name:  "build",
						Image: "golang:1.23",
						Steps: []pipeline.Step{{Name: "build", Run: []string{"go build -o /out/myapp ./..."}}},
					},
					{
						Name:      "deploy",
						Image:     "alpine:latest",
						DependsOn: []string{"build"},
						Artifacts: []pipeline.Artifact{{From: "build", Source: "/out/myapp", Target: "/usr/local/bin/myapp"}},
						Steps:     []pipeline.Step{{Name: "deploy", Run: []string{"echo deploying"}}},
					},
				},
				TopoOrder: []int{0, 1},
			},
		},
		{
			name: "artifact without dependency errors",
			input: `pipeline "bad" {
				step "build" {
					image "golang:1.23"
					run "go build -o /out/myapp ./..."
				}
				step "deploy" {
					image "alpine:latest"
					artifact "build" "/out/myapp" "/usr/local/bin/myapp"
					run "echo deploying"
				}
			}`,
			wantErr: pipeline.ErrArtifactNoDep,
		},
		{
			name: "env with wrong args",
			input: `pipeline "bad" {
				step "a" {
					image "alpine:latest"
					env "ONLY_KEY"
					run "echo hi"
				}
			}`,
			wantErr: ErrMissingField,
		},
		{
			name: "export with extra args",
			input: `pipeline "bad" {
				step "a" {
					image "alpine:latest"
					export "/out/myapp" "/extra"
					run "echo hi"
				}
			}`,
			wantErr: ErrExtraArgs,
		},
		{
			name: "artifact with wrong args",
			input: `pipeline "bad" {
				step "other" {
					image "alpine:latest"
					run "echo other"
				}
				step "a" {
					image "alpine:latest"
					depends-on "other"
					artifact "other" "/src"
					run "echo hi"
				}
			}`,
			wantErr: ErrMissingField,
		},
		{
			name: "step with no-cache flag",
			input: `pipeline "ci" {
				step "test" {
					image "golang:1.23"
					run "go test ./..."
					no-cache
				}
			}`,
			want: pipeline.Pipeline{
				Name: "ci",
				Jobs: []pipeline.Job{
					{
						Name:    "test",
						Image:   "golang:1.23",
						NoCache: true,
						Steps:   []pipeline.Step{{Name: "test", Run: []string{"go test ./..."}}},
					},
				},
				TopoOrder: []int{0},
			},
		},
		{
			name: "duplicate no-cache rejected",
			input: `pipeline "bad" {
				step "test" {
					image "alpine:latest"
					run "echo hi"
					no-cache
					no-cache
				}
			}`,
			wantErr: ErrDuplicateField,
		},
		{
			name: "no-cache with arguments rejected",
			input: `pipeline "bad" {
				step "test" {
					image "alpine:latest"
					run "echo hi"
					no-cache "arg"
				}
			}`,
			wantErr: ErrExtraArgs,
		},
		{
			name: "defaults with arguments rejected",
			input: `pipeline "bad" {
				defaults "extra" {
					image "alpine:latest"
				}
				step "a" {
					image "alpine:latest"
					run "echo hi"
				}
			}`,
			wantErr: ErrExtraArgs,
		},
		{
			name: "defaults duplicate image rejected",
			input: `pipeline "bad" {
				defaults {
					image "alpine:latest"
					image "golang:1.23"
				}
				step "a" {
					image "alpine:latest"
					run "echo hi"
				}
			}`,
			wantErr: ErrDuplicateField,
		},
		{
			name: "defaults unknown child rejected",
			input: `pipeline "bad" {
				defaults {
					image "alpine:latest"
					bogus "value"
				}
				step "a" {
					image "alpine:latest"
					run "echo hi"
				}
			}`,
			wantErr: ErrUnknownNode,
		},
		{
			name: "multi-step job",
			input: `pipeline "ci" {
				job "quality" {
					image "golang:1.23"
					step "fmt" {
						run "gofmt -l ."
					}
					step "vet" {
						run "go vet ./..."
					}
					step "lint" {
						run "golangci-lint run"
					}
				}
			}`,
			want: pipeline.Pipeline{
				Name: "ci",
				Jobs: []pipeline.Job{
					{
						Name:  "quality",
						Image: "golang:1.23",
						Steps: []pipeline.Step{
							{Name: "fmt", Run: []string{"gofmt -l ."}},
							{Name: "vet", Run: []string{"go vet ./..."}},
							{Name: "lint", Run: []string{"golangci-lint run"}},
						},
					},
				},
				TopoOrder: []int{0},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			p := stringParser()
			got, err := p.ParseString(tt.input)

			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestParseInclude(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		files   map[string]string
		entry   string // abs path of the entry pipeline
		want    pipeline.Pipeline
		wantErr error
	}{
		{
			name: "single fragment include",
			files: map[string]string{
				"/project/ci.kdl": `pipeline "ci" {
					include "./fragments/lint.kdl" as="lint"
					step "build" {
						image "golang:1.23"
						depends-on "lint"
						run "go build ./..."
					}
				}`,
				"/project/fragments/lint.kdl": `fragment "lint" {
					step "lint" {
						image "golangci/golangci-lint:latest"
						run "golangci-lint run"
					}
				}`,
			},
			entry: "/project/ci.kdl",
			want: pipeline.Pipeline{
				Name: "ci",
				Jobs: []pipeline.Job{
					{
						Name:  "lint",
						Image: "golangci/golangci-lint:latest",
						Steps: []pipeline.Step{{Name: "lint", Run: []string{"golangci-lint run"}}},
					},
					{
						Name:      "build",
						Image:     "golang:1.23",
						DependsOn: []string{"lint"},
						Steps:     []pipeline.Step{{Name: "build", Run: []string{"go build ./..."}}},
					},
				},
				TopoOrder: []int{0, 1},
			},
		},
		{
			name: "parameterized fragment",
			files: map[string]string{
				"/project/ci.kdl": `pipeline "ci" {
					include "./fragments/test.kdl" as="tests" {
						go-version "1.23"
						threshold "80"
					}
				}`,
				"/project/fragments/test.kdl": `fragment "go-test" {
					param "go-version" default="1.22"
					param "threshold"
					step "unit-test" {
						image "golang:${param.go-version}"
						run "go test -coverprofile=cover.out ./..."
					}
					step "check-coverage" {
						depends-on "unit-test"
						image "golang:${param.go-version}"
						run "check-coverage ${param.threshold}"
					}
				}`,
			},
			entry: "/project/ci.kdl",
			want: pipeline.Pipeline{
				Name: "ci",
				Jobs: []pipeline.Job{
					{
						Name:  "unit-test",
						Image: "golang:1.23",
						Steps: []pipeline.Step{{Name: "unit-test", Run: []string{"go test -coverprofile=cover.out ./..."}}},
					},
					{
						Name:      "check-coverage",
						Image:     "golang:1.23",
						DependsOn: []string{"unit-test"},
						Steps:     []pipeline.Step{{Name: "check-coverage", Run: []string{"check-coverage 80"}}},
					},
				},
				TopoOrder: []int{0, 1},
			},
		},
		{
			name: "alias resolves to terminal steps",
			files: map[string]string{
				"/project/ci.kdl": `pipeline "ci" {
					include "./fragments/test.kdl" as="tests" {
						go-version "1.23"
					}
					step "deploy" {
						image "alpine:latest"
						depends-on "tests"
						run "echo deploy"
					}
				}`,
				"/project/fragments/test.kdl": `fragment "go-test" {
					param "go-version"
					step "unit-test" {
						image "golang:${param.go-version}"
						run "go test -short ./..."
					}
					step "integration-test" {
						depends-on "unit-test"
						image "golang:${param.go-version}"
						run "go test -tags=integration ./..."
					}
				}`,
			},
			entry: "/project/ci.kdl",
			want: pipeline.Pipeline{
				Name: "ci",
				Jobs: []pipeline.Job{
					{
						Name:  "unit-test",
						Image: "golang:1.23",
						Steps: []pipeline.Step{{Name: "unit-test", Run: []string{"go test -short ./..."}}},
					},
					{
						Name:      "integration-test",
						Image:     "golang:1.23",
						DependsOn: []string{"unit-test"},
						Steps:     []pipeline.Step{{Name: "integration-test", Run: []string{"go test -tags=integration ./..."}}},
					},
					{
						Name:      "deploy",
						Image:     "alpine:latest",
						DependsOn: []string{"integration-test"},
						Steps:     []pipeline.Step{{Name: "deploy", Run: []string{"echo deploy"}}},
					},
				},
				TopoOrder: []int{0, 1, 2},
			},
		},
		{
			name: "multiple includes",
			files: map[string]string{
				"/project/ci.kdl": `pipeline "ci" {
					include "./fragments/lint.kdl" as="lint"
					include "./fragments/test.kdl" as="tests"
					step "deploy" {
						image "alpine:latest"
						depends-on "lint"
						depends-on "tests"
						run "echo deploy"
					}
				}`,
				"/project/fragments/lint.kdl": `fragment "lint" {
					step "lint" {
						image "golangci/golangci-lint:latest"
						run "golangci-lint run"
					}
				}`,
				"/project/fragments/test.kdl": `fragment "test" {
					step "unit-test" {
						image "golang:1.23"
						run "go test ./..."
					}
				}`,
			},
			entry: "/project/ci.kdl",
			want: pipeline.Pipeline{
				Name: "ci",
				Jobs: []pipeline.Job{
					{
						Name:  "lint",
						Image: "golangci/golangci-lint:latest",
						Steps: []pipeline.Step{{Name: "lint", Run: []string{"golangci-lint run"}}},
					},
					{
						Name:  "unit-test",
						Image: "golang:1.23",
						Steps: []pipeline.Step{{Name: "unit-test", Run: []string{"go test ./..."}}},
					},
					{
						Name:      "deploy",
						Image:     "alpine:latest",
						DependsOn: []string{"lint", "unit-test"},
						Steps:     []pipeline.Step{{Name: "deploy", Run: []string{"echo deploy"}}},
					},
				},
				TopoOrder: []int{0, 1, 2},
			},
		},
		{
			name: "circular include detection",
			files: map[string]string{
				"/project/a.kdl": `pipeline "ci" {
					include "./b.kdl" as="b"
				}`,
				"/project/b.kdl": `fragment "b" {
					step "b" {
						image "alpine:latest"
						run "echo b"
					}
					include "../project/a.kdl" as="a"
				}`,
			},
			entry:   "/project/a.kdl",
			wantErr: pipeline.ErrCircularInclude,
		},
		{
			name: "missing required param",
			files: map[string]string{
				"/project/ci.kdl": `pipeline "ci" {
					include "./frag.kdl" as="f"
				}`,
				"/project/frag.kdl": `fragment "f" {
					param "required-param"
					step "s" {
						image "alpine:latest"
						run "echo ${param.required-param}"
					}
				}`,
			},
			entry:   "/project/ci.kdl",
			wantErr: pipeline.ErrMissingParam,
		},
		{
			name: "unknown param provided",
			files: map[string]string{
				"/project/ci.kdl": `pipeline "ci" {
					include "./frag.kdl" as="f" {
						unknown-param "value"
					}
				}`,
				"/project/frag.kdl": `fragment "f" {
					step "s" {
						image "alpine:latest"
						run "echo hello"
					}
				}`,
			},
			entry:   "/project/ci.kdl",
			wantErr: pipeline.ErrUnknownParam,
		},
		{
			name: "alias falls back to fragment name",
			files: map[string]string{
				"/project/ci.kdl": `pipeline "ci" {
					include "./frag.kdl"
					step "deploy" {
						image "alpine:latest"
						depends-on "f"
						run "echo deploy"
					}
				}`,
				"/project/frag.kdl": `fragment "f" {
					step "s" {
						image "alpine:latest"
						run "echo hello"
					}
				}`,
			},
			entry: "/project/ci.kdl",
			want: pipeline.Pipeline{
				Name: "ci",
				Jobs: []pipeline.Job{
					{
						Name:  "s",
						Image: "alpine:latest",
						Steps: []pipeline.Step{{Name: "s", Run: []string{"echo hello"}}},
					},
					{
						Name:      "deploy",
						Image:     "alpine:latest",
						DependsOn: []string{"s"},
						Steps:     []pipeline.Step{{Name: "deploy", Run: []string{"echo deploy"}}},
					},
				},
				TopoOrder: []int{0, 1},
			},
		},
		{
			name: "duplicate alias names",
			files: map[string]string{
				"/project/ci.kdl": `pipeline "ci" {
					include "./a.kdl" as="x"
					include "./b.kdl" as="x"
				}`,
				"/project/a.kdl": `fragment "a" {
					step "a" {
						image "alpine:latest"
						run "echo a"
					}
				}`,
				"/project/b.kdl": `fragment "b" {
					step "b" {
						image "alpine:latest"
						run "echo b"
					}
				}`,
			},
			entry:   "/project/ci.kdl",
			wantErr: pipeline.ErrDuplicateAlias,
		},
		{
			name: "on-conflict skip first wins",
			files: map[string]string{
				"/project/ci.kdl": `pipeline "ci" {
					step "scan" {
						image "custom-scanner:latest"
						run "custom-scan ."
					}
					include "./security.kdl" as="security" on-conflict="skip"
				}`,
				"/project/security.kdl": `fragment "security" {
					step "scan" {
						image "trivy:latest"
						run "trivy scan"
					}
					step "audit" {
						image "alpine:latest"
						run "audit check"
					}
				}`,
			},
			entry: "/project/ci.kdl",
			want: pipeline.Pipeline{
				Name: "ci",
				Jobs: []pipeline.Job{
					{
						Name:  "scan",
						Image: "custom-scanner:latest",
						Steps: []pipeline.Step{{Name: "scan", Run: []string{"custom-scan ."}}},
					},
					{
						Name:  "audit",
						Image: "alpine:latest",
						Steps: []pipeline.Step{{Name: "audit", Run: []string{"audit check"}}},
					},
				},
				TopoOrder: []int{0, 1},
			},
		},
		{
			name: "on-conflict error duplicate step",
			files: map[string]string{
				"/project/ci.kdl": `pipeline "ci" {
					step "scan" {
						image "custom-scanner:latest"
						run "custom-scan ."
					}
					include "./security.kdl" as="security"
				}`,
				"/project/security.kdl": `fragment "security" {
					step "scan" {
						image "trivy:latest"
						run "trivy scan"
					}
				}`,
			},
			entry:   "/project/ci.kdl",
			wantErr: pipeline.ErrDuplicateJob,
		},
		{
			name: "param default used when not provided",
			files: map[string]string{
				"/project/ci.kdl": `pipeline "ci" {
					include "./frag.kdl" as="f"
				}`,
				"/project/frag.kdl": `fragment "f" {
					param "version" default="1.22"
					step "s" {
						image "golang:${param.version}"
						run "go version"
					}
				}`,
			},
			entry: "/project/ci.kdl",
			want: pipeline.Pipeline{
				Name: "ci",
				Jobs: []pipeline.Job{
					{
						Name:  "s",
						Image: "golang:1.22",
						Steps: []pipeline.Step{{Name: "s", Run: []string{"go version"}}},
					},
				},
				TopoOrder: []int{0},
			},
		},
		{
			name: "param plus matrix interaction",
			files: map[string]string{
				"/project/ci.kdl": `pipeline "ci" {
					matrix {
						os "linux" "darwin"
					}
					include "./frag.kdl" as="tests" {
						version "1.23"
					}
				}`,
				"/project/frag.kdl": `fragment "tests" {
					param "version"
					step "test" {
						image "golang:${param.version}"
						run "GOOS=${matrix.os} go test ./..."
					}
				}`,
			},
			entry: "/project/ci.kdl",
			want: pipeline.Pipeline{
				Name: "ci",
				Jobs: []pipeline.Job{
					{
						Name:  "test[os=linux]",
						Image: "golang:1.23",
						Steps: []pipeline.Step{{Name: "test", Run: []string{"GOOS=linux go test ./..."}}},
					},
					{
						Name:  "test[os=darwin]",
						Image: "golang:1.23",
						Steps: []pipeline.Step{{Name: "test", Run: []string{"GOOS=darwin go test ./..."}}},
					},
				},
				TopoOrder: []int{0, 1},
			},
		},
		{
			name: "including a pipeline file extracts steps",
			files: map[string]string{
				"/project/ci.kdl": `pipeline "ci" {
					include "./other.kdl" as="other"
					step "deploy" {
						image "alpine:latest"
						depends-on "other"
						run "echo deploy"
					}
				}`,
				"/project/other.kdl": `pipeline "security" {
					matrix {
						os "linux"
					}
					step "scan" {
						image "trivy:latest"
						run "trivy scan"
					}
				}`,
			},
			entry: "/project/ci.kdl",
			want: pipeline.Pipeline{
				Name: "ci",
				Jobs: []pipeline.Job{
					{
						Name:  "scan",
						Image: "trivy:latest",
						Steps: []pipeline.Step{{Name: "scan", Run: []string{"trivy scan"}}},
					},
					{
						Name:      "deploy",
						Image:     "alpine:latest",
						DependsOn: []string{"scan"},
						Steps:     []pipeline.Step{{Name: "deploy", Run: []string{"echo deploy"}}},
					},
				},
				TopoOrder: []int{0, 1},
			},
		},
		{
			name: "transitive includes",
			files: map[string]string{
				"/project/ci.kdl": `pipeline "ci" {
					include "./a.kdl" as="a"
				}`,
				"/project/a.kdl": `fragment "a" {
					include "./sub/b.kdl" as="b"
					step "a-step" {
						image "alpine:latest"
						depends-on "b"
						run "echo a"
					}
				}`,
				"/project/sub/b.kdl": `fragment "b" {
					step "b-step" {
						image "alpine:latest"
						run "echo b"
					}
				}`,
			},
			entry: "/project/ci.kdl",
			want: pipeline.Pipeline{
				Name: "ci",
				Jobs: []pipeline.Job{
					{
						Name:  "b-step",
						Image: "alpine:latest",
						Steps: []pipeline.Step{{Name: "b-step", Run: []string{"echo b"}}},
					},
					{
						Name:      "a-step",
						Image:     "alpine:latest",
						DependsOn: []string{"b-step"},
						Steps:     []pipeline.Step{{Name: "a-step", Run: []string{"echo a"}}},
					},
				},
				TopoOrder: []int{0, 1},
			},
		},
		{
			name: "duplicate include param",
			files: map[string]string{
				"/project/ci.kdl": `pipeline "ci" {
					include "./frag.kdl" as="f" {
						version "1.23"
						version "1.24"
					}
				}`,
				"/project/frag.kdl": `fragment "f" {
					param "version"
					step "s" {
						image "golang:${param.version}"
						run "go version"
					}
				}`,
			},
			entry:   "/project/ci.kdl",
			wantErr: ErrDuplicateField,
		},
		{
			name: "included file with both pipeline and fragment",
			files: map[string]string{
				"/project/ci.kdl": `pipeline "ci" {
					include "./both.kdl" as="both"
				}`,
				"/project/both.kdl": `pipeline "p" {
					step "a" {
						image "alpine:latest"
						run "echo a"
					}
				}
				fragment "f" {
					step "b" {
						image "alpine:latest"
						run "echo b"
					}
				}`,
			},
			entry:   "/project/ci.kdl",
			wantErr: ErrAmbiguousFile,
		},
		{
			name: "unknown top-level node in included file",
			files: map[string]string{
				"/project/ci.kdl": `pipeline "ci" {
					include "./bad.kdl" as="bad"
				}`,
				"/project/bad.kdl": `frgament "oops" {
					step "s" {
						image "alpine:latest"
						run "echo hi"
					}
				}`,
			},
			entry:   "/project/ci.kdl",
			wantErr: ErrUnknownNode,
		},
		{
			name: "alias falls back to pipeline name",
			files: map[string]string{
				"/project/ci.kdl": `pipeline "ci" {
					include "./other.kdl"
					step "deploy" {
						image "alpine:latest"
						depends-on "security"
						run "echo deploy"
					}
				}`,
				"/project/other.kdl": `pipeline "security" {
					step "scan" {
						image "trivy:latest"
						run "trivy scan"
					}
				}`,
			},
			entry: "/project/ci.kdl",
			want: pipeline.Pipeline{
				Name: "ci",
				Jobs: []pipeline.Job{
					{
						Name:  "scan",
						Image: "trivy:latest",
						Steps: []pipeline.Step{{Name: "scan", Run: []string{"trivy scan"}}},
					},
					{
						Name:      "deploy",
						Image:     "alpine:latest",
						DependsOn: []string{"scan"},
						Steps:     []pipeline.Step{{Name: "deploy", Run: []string{"echo deploy"}}},
					},
				},
				TopoOrder: []int{0, 1},
			},
		},
		{
			name: "explicit as overrides fragment name",
			files: map[string]string{
				"/project/ci.kdl": `pipeline "ci" {
					include "./frag.kdl" as="custom"
					step "deploy" {
						image "alpine:latest"
						depends-on "custom"
						run "echo deploy"
					}
				}`,
				"/project/frag.kdl": `fragment "go-test" {
					step "unit-test" {
						image "golang:1.23"
						run "go test ./..."
					}
				}`,
			},
			entry: "/project/ci.kdl",
			want: pipeline.Pipeline{
				Name: "ci",
				Jobs: []pipeline.Job{
					{
						Name:  "unit-test",
						Image: "golang:1.23",
						Steps: []pipeline.Step{{Name: "unit-test", Run: []string{"go test ./..."}}},
					},
					{
						Name:      "deploy",
						Image:     "alpine:latest",
						DependsOn: []string{"unit-test"},
						Steps:     []pipeline.Step{{Name: "deploy", Run: []string{"echo deploy"}}},
					},
				},
				TopoOrder: []int{0, 1},
			},
		},
		{
			name: "duplicate auto-alias",
			files: map[string]string{
				"/project/ci.kdl": `pipeline "ci" {
					include "./a.kdl"
					include "./b.kdl"
				}`,
				"/project/a.kdl": `fragment "shared" {
					step "a-step" {
						image "alpine:latest"
						run "echo a"
					}
				}`,
				"/project/b.kdl": `fragment "shared" {
					step "b-step" {
						image "alpine:latest"
						run "echo b"
					}
				}`,
			},
			entry:   "/project/ci.kdl",
			wantErr: pipeline.ErrDuplicateAlias,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p := newTestParser(tt.files)
			got, err := p.ParseFile(tt.entry)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

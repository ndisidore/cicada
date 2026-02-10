package parser

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ndisidore/cicada/pkg/pipeline"
)

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
				Steps: []pipeline.Step{
					{
						Name:  "greet",
						Image: "alpine:latest",
						Run:   []string{"echo hello"},
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
				Steps: []pipeline.Step{
					{
						Name:    "build",
						Image:   "rust:1.76",
						Workdir: "/src",
						Run:     []string{"cargo build"},
						Mounts:  []pipeline.Mount{{Source: ".", Target: "/src"}},
						Caches:  []pipeline.Cache{{ID: "cargo", Target: "/root/.cargo"}},
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
				Steps: []pipeline.Step{
					{
						Name:  "build",
						Image: "rust:1.76",
						Run:   []string{"cargo build"},
						Mounts: []pipeline.Mount{
							{Source: ".", Target: "/src", ReadOnly: true},
						},
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
				Steps: []pipeline.Step{
					{
						Name:  "info",
						Image: "alpine:latest",
						Run:   []string{"uname -a", "date"},
					},
				},
				TopoOrder: []int{0},
			},
		},
		{
			name: "missing pipeline node",
			input: `something "foo" {
				image "alpine:latest"
			}`,
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
			wantErr: pipeline.ErrDuplicateStep,
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
				Steps: []pipeline.Step{
					{
						Name:  "build[os=linux]",
						Image: "golang:1.23",
						Run:   []string{"GOOS=linux go build ./..."},
					},
					{
						Name:  "build[os=darwin]",
						Image: "golang:1.23",
						Run:   []string{"GOOS=darwin go build ./..."},
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
				Steps: []pipeline.Step{
					{
						Name:  "test[go-version=1.21]",
						Image: "golang:1.21",
						Run:   []string{"go test ./..."},
					},
					{
						Name:  "test[go-version=1.22]",
						Image: "golang:1.22",
						Run:   []string{"go test ./..."},
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
				Steps: []pipeline.Step{
					{
						Name:  "build[os=linux]",
						Image: "golang:1.23",
						Run:   []string{"GOOS=linux go build ./..."},
					},
					{
						Name:      "test[go-version=1.21,os=linux]",
						Image:     "golang:1.21",
						Run:       []string{"GOOS=linux go test ./..."},
						DependsOn: []string{"build[os=linux]"},
					},
					{
						Name:      "test[go-version=1.22,os=linux]",
						Image:     "golang:1.22",
						Run:       []string{"GOOS=linux go test ./..."},
						DependsOn: []string{"build[os=linux]"},
					},
					{
						Name:  "build[os=darwin]",
						Image: "golang:1.23",
						Run:   []string{"GOOS=darwin go build ./..."},
					},
					{
						Name:      "test[go-version=1.21,os=darwin]",
						Image:     "golang:1.21",
						Run:       []string{"GOOS=darwin go test ./..."},
						DependsOn: []string{"build[os=darwin]"},
					},
					{
						Name:      "test[go-version=1.22,os=darwin]",
						Image:     "golang:1.22",
						Run:       []string{"GOOS=darwin go test ./..."},
						DependsOn: []string{"build[os=darwin]"},
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
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := ParseString(tt.input)

			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

package cache

import (
	"testing"

	"github.com/moby/buildkit/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseSpec(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		raw          string
		want         client.CacheOptionsEntry
		wantSentinel error
		wantErr      string
	}{
		{
			name: "registry shorthand",
			raw:  "ghcr.io/myorg/cache:main",
			want: client.CacheOptionsEntry{
				Type:  "registry",
				Attrs: map[string]string{"ref": "ghcr.io/myorg/cache:main"},
			},
		},
		{
			name: "explicit registry type",
			raw:  "type=registry,ref=ghcr.io/myorg/cache:main,mode=max",
			want: client.CacheOptionsEntry{
				Type:  "registry",
				Attrs: map[string]string{"ref": "ghcr.io/myorg/cache:main", "mode": "max"},
			},
		},
		{
			name: "gha type with scope",
			raw:  "type=gha,scope=main",
			want: client.CacheOptionsEntry{
				Type:  "gha",
				Attrs: map[string]string{"scope": "main"},
			},
		},
		{
			name: "local type",
			raw:  "type=local,dest=/tmp/cache",
			want: client.CacheOptionsEntry{
				Type:  "local",
				Attrs: map[string]string{"dest": "/tmp/cache"},
			},
		},
		{
			name: "inline type",
			raw:  "type=inline",
			want: client.CacheOptionsEntry{
				Type:  "inline",
				Attrs: map[string]string{},
			},
		},
		{
			name: "s3 type",
			raw:  "type=s3,bucket=my-bucket,region=us-east-1",
			want: client.CacheOptionsEntry{
				Type:  "s3",
				Attrs: map[string]string{"bucket": "my-bucket", "region": "us-east-1"},
			},
		},
		{
			name:         "empty spec",
			raw:          "",
			wantSentinel: ErrInvalidCacheSpec,
		},
		{
			name:         "whitespace-only spec",
			raw:          "   ",
			wantSentinel: ErrInvalidCacheSpec,
		},
		{
			name:         "missing type attribute",
			raw:          "ref=ghcr.io/myorg/cache",
			wantSentinel: ErrInvalidCacheSpec,
			wantErr:      "missing type",
		},
		{
			name:         "unknown cache type",
			raw:          "type=memcached,host=localhost",
			wantSentinel: ErrUnknownCacheType,
		},
		{
			name:         "malformed key-value",
			raw:          "type=registry,,ref=foo",
			wantSentinel: ErrInvalidCacheSpec,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParseSpec(tt.raw)
			if tt.wantSentinel != nil {
				require.ErrorIs(t, err, tt.wantSentinel)
				if tt.wantErr != "" {
					assert.Contains(t, err.Error(), tt.wantErr)
				}
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestParseSpecs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		raw     []string
		want    int
		wantErr bool
	}{
		{
			name: "multiple specs",
			raw:  []string{"type=registry,ref=ghcr.io/org/cache", "type=gha,scope=main"},
			want: 2,
		},
		{
			name: "empty slice",
			raw:  nil,
			want: 0,
		},
		{
			name:    "one invalid",
			raw:     []string{"type=registry,ref=foo", "type=bogus"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParseSpecs(tt.raw)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Len(t, got, tt.want)
		})
	}
}

// TestDetectGHA cannot use t.Parallel because subtests mutate process
// environment via t.Setenv.
func TestDetectGHA(t *testing.T) {
	tests := []struct {
		name      string
		entries   []client.CacheOptionsEntry
		envURLV1  string
		envURLV2  string
		envToken  string
		wantURLV1 string
		wantURLV2 string
		wantTok   string
	}{
		{
			name: "populates v1 url and token from env",
			entries: []client.CacheOptionsEntry{
				{Type: "gha", Attrs: map[string]string{"scope": "main"}},
			},
			envURLV1:  "https://actions.example.com/cache",
			envToken:  "tok-123",
			wantURLV1: "https://actions.example.com/cache",
			wantTok:   "tok-123",
		},
		{
			name: "populates v2 url and token from env",
			entries: []client.CacheOptionsEntry{
				{Type: "gha", Attrs: map[string]string{"scope": "main"}},
			},
			envURLV2:  "https://results.actions.githubusercontent.com/",
			envToken:  "tok-456",
			wantURLV2: "https://results.actions.githubusercontent.com/",
			wantTok:   "tok-456",
		},
		{
			name: "populates both url and url_v2 when both env vars set",
			entries: []client.CacheOptionsEntry{
				{Type: "gha", Attrs: map[string]string{"scope": "main"}},
			},
			envURLV1:  "https://actions.example.com/cache",
			envURLV2:  "https://results.actions.githubusercontent.com/",
			envToken:  "tok-789",
			wantURLV1: "https://actions.example.com/cache",
			wantURLV2: "https://results.actions.githubusercontent.com/",
			wantTok:   "tok-789",
		},
		{
			name: "preserves explicit attrs",
			entries: []client.CacheOptionsEntry{
				{Type: "gha", Attrs: map[string]string{
					"url":    "https://custom.example.com",
					"url_v2": "https://custom-v2.example.com",
					"token":  "explicit-tok",
				}},
			},
			envURLV1:  "https://actions.example.com/cache",
			envURLV2:  "https://results.actions.githubusercontent.com/",
			envToken:  "tok-123",
			wantURLV1: "https://custom.example.com",
			wantURLV2: "https://custom-v2.example.com",
			wantTok:   "explicit-tok",
		},
		{
			name: "skips non-gha entries",
			entries: []client.CacheOptionsEntry{
				{Type: "registry", Attrs: map[string]string{"ref": "foo"}},
			},
			envURLV1: "https://actions.example.com/cache",
			envToken: "tok-123",
		},
		{
			name: "no env vars is a no-op",
			entries: []client.CacheOptionsEntry{
				{Type: "gha", Attrs: map[string]string{"scope": "main"}},
			},
		},
		{
			name: "nil attrs map gets created",
			entries: []client.CacheOptionsEntry{
				{Type: "gha"},
			},
			envURLV1:  "https://actions.example.com/cache",
			envToken:  "tok-123",
			wantURLV1: "https://actions.example.com/cache",
			wantTok:   "tok-123",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("ACTIONS_CACHE_URL", tt.envURLV1)
			t.Setenv("ACTIONS_RESULTS_URL", tt.envURLV2)
			t.Setenv("ACTIONS_RUNTIME_TOKEN", tt.envToken)

			result := DetectGHA(tt.entries)
			require.Len(t, result, len(tt.entries))
			for _, e := range result {
				if e.Type != "gha" {
					assert.NotContains(t, e.Attrs, "url")
					assert.NotContains(t, e.Attrs, "url_v2")
					assert.NotContains(t, e.Attrs, "token")
					continue
				}
				if tt.wantURLV1 != "" {
					assert.Equal(t, tt.wantURLV1, e.Attrs["url"])
				} else if tt.envURLV1 == "" {
					assert.NotContains(t, e.Attrs, "url")
				}
				if tt.wantURLV2 != "" {
					assert.Equal(t, tt.wantURLV2, e.Attrs["url_v2"])
				} else if tt.envURLV2 == "" {
					assert.NotContains(t, e.Attrs, "url_v2")
				}
				if tt.wantTok != "" {
					assert.Equal(t, tt.wantTok, e.Attrs["token"])
				} else if tt.envToken == "" {
					assert.NotContains(t, e.Attrs, "token")
				}
			}
		})
	}
}

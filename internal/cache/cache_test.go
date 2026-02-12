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
		name     string
		entries  []client.CacheOptionsEntry
		envURL   string
		envToken string
		wantURL  string
		wantTok  string
	}{
		{
			name: "populates from env",
			entries: []client.CacheOptionsEntry{
				{Type: "gha", Attrs: map[string]string{"scope": "main"}},
			},
			envURL:   "https://actions.example.com/cache",
			envToken: "tok-123",
			wantURL:  "https://actions.example.com/cache",
			wantTok:  "tok-123",
		},
		{
			name: "preserves explicit attrs",
			entries: []client.CacheOptionsEntry{
				{Type: "gha", Attrs: map[string]string{
					"url":   "https://custom.example.com",
					"token": "explicit-tok",
				}},
			},
			envURL:   "https://actions.example.com/cache",
			envToken: "tok-123",
			wantURL:  "https://custom.example.com",
			wantTok:  "explicit-tok",
		},
		{
			name: "skips non-gha entries",
			entries: []client.CacheOptionsEntry{
				{Type: "registry", Attrs: map[string]string{"ref": "foo"}},
			},
			envURL:   "https://actions.example.com/cache",
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
			envURL:   "https://actions.example.com/cache",
			envToken: "tok-123",
			wantURL:  "https://actions.example.com/cache",
			wantTok:  "tok-123",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("ACTIONS_CACHE_URL", tt.envURL)
			t.Setenv("ACTIONS_RUNTIME_TOKEN", tt.envToken)

			result := DetectGHA(tt.entries)
			require.Len(t, result, len(tt.entries))
			for _, e := range result {
				if e.Type != "gha" {
					// Non-gha entries should be untouched.
					assert.NotContains(t, e.Attrs, "url")
					assert.NotContains(t, e.Attrs, "token")
					continue
				}
				if tt.wantURL != "" {
					assert.Equal(t, tt.wantURL, e.Attrs["url"])
				} else if tt.envURL == "" {
					assert.NotContains(t, e.Attrs, "url")
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

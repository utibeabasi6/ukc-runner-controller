package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func load(t *testing.T, yaml string) (*Config, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	return Load(path)
}

func TestLoadAppliesDefaults(t *testing.T) {
	cfg, err := load(t, `
github:
  url: https://github.com/acme
runners:
  - name: small
    labels: [linux]
    image: acme/actions-runner:2.337.0
    memory_mb: 4096
    max_runners: 5
`)
	if err != nil {
		t.Fatal(err)
	}

	r := cfg.Runners[0]
	if cfg.GitHub.RunnerGroup != DefaultRunnerGroup || cfg.Unikraft.Metro != DefaultMetro {
		t.Errorf("defaults not applied: %+v", cfg)
	}
	if r.VCPUs != 1 || r.DiskMB != DefaultDiskMB {
		t.Errorf("runner defaults not applied: %+v", r)
	}
	if !slices.Equal(r.Labels, []string{"small", "linux"}) {
		t.Errorf("labels = %v, want the name first", r.Labels)
	}
}

func TestLoadRejectsInvalidConfig(t *testing.T) {
	tests := map[string]struct {
		yaml string
		want string
	}{
		"unknown field": {
			yaml: "github:\n  url: https://github.com/acme\n  tokn: x\n",
			want: "field tokn not found",
		},
		"missing url": {
			yaml: "runners:\n  - {name: a, image: i, memory_mb: 1, max_runners: 1}\n",
			want: "github.url",
		},
		"no runners": {
			yaml: "github:\n  url: https://github.com/acme\n",
			want: "at least one runner",
		},
		"bad name": {
			yaml: "github:\n  url: https://github.com/acme\nrunners:\n  - {name: Big_Runner, image: i, memory_mb: 1, max_runners: 1}\n",
			want: "lowercase",
		},
		"duplicate name": {
			yaml: "github:\n  url: https://github.com/acme\nrunners:\n  - {name: a, image: i, memory_mb: 1, max_runners: 1}\n  - {name: a, image: i, memory_mb: 1, max_runners: 1}\n",
			want: "duplicate",
		},
		"incomplete app": {
			yaml: "github:\n  url: https://github.com/acme\n  app: {client_id: x}\nrunners:\n  - {name: a, image: i, memory_mb: 1, max_runners: 1}\n",
			want: "private_key_file",
		},
		"missing shape": {
			yaml: "github:\n  url: https://github.com/acme\nrunners:\n  - {name: a}\n",
			want: "image is required",
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := load(t, tt.yaml)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %v, want it to mention %q", err, tt.want)
			}
		})
	}
}

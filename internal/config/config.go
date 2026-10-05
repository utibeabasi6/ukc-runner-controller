package config

import (
	"bytes"
	"errors"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"slices"

	"go.yaml.in/yaml/v3"
)

const (
	DefaultRunnerGroup = "default"
	DefaultMetro       = "fra"
	DefaultDiskMB      = 10240
)

type Config struct {
	GitHub   GitHub         `yaml:"github"`
	Unikraft Unikraft       `yaml:"unikraft"`
	Runners  []RunnerConfig `yaml:"runners"`
}

type GitHub struct {
	URL         string     `yaml:"url"`
	RunnerGroup string     `yaml:"runner_group"`
	App         *GitHubApp `yaml:"app"`
}

type GitHubApp struct {
	ClientID       string `yaml:"client_id"`
	InstallationID int64  `yaml:"installation_id"`
	PrivateKeyFile string `yaml:"private_key_file"`
}

type Unikraft struct {
	Metro string `yaml:"metro"`
}

// RunnerConfig describes one scale set and the instance shape of its runners.
// Name is both the scale set name in GitHub and the prefix of the instance
// names in Unikraft Cloud, so it must be a valid DNS label.
type RunnerConfig struct {
	Name       string   `yaml:"name"`
	Labels     []string `yaml:"labels"`
	Image      string   `yaml:"image"`
	VCPUs      int      `yaml:"vcpus"`
	MemoryMB   int      `yaml:"memory_mb"`
	DiskMB     int      `yaml:"disk_mb"`
	MaxRunners int      `yaml:"max_runners"`
}

var nameRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,38}[a-z0-9])?$`)

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)

	var cfg Config
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}

	cfg.setDefaults()
	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("invalid config %s: %w", path, err)
	}
	return &cfg, nil
}

func (c *Config) setDefaults() {
	if c.GitHub.RunnerGroup == "" {
		c.GitHub.RunnerGroup = DefaultRunnerGroup
	}
	if c.Unikraft.Metro == "" {
		c.Unikraft.Metro = DefaultMetro
	}
	for i := range c.Runners {
		r := &c.Runners[i]
		if r.VCPUs == 0 {
			r.VCPUs = 1
		}
		if r.DiskMB == 0 {
			r.DiskMB = DefaultDiskMB
		}
		if !slices.Contains(r.Labels, r.Name) {
			r.Labels = append([]string{r.Name}, r.Labels...)
		}
	}
}

func (c *Config) validate() error {
	var errs []error

	u, err := url.Parse(c.GitHub.URL)
	if c.GitHub.URL == "" || err != nil || u.Scheme == "" || u.Host == "" {
		errs = append(errs, errors.New("github.url must be an organization, repository or enterprise URL"))
	}
	if app := c.GitHub.App; app != nil {
		if app.ClientID == "" || app.InstallationID == 0 || app.PrivateKeyFile == "" {
			errs = append(errs, errors.New("github.app needs client_id, installation_id and private_key_file"))
		}
	}

	if len(c.Runners) == 0 {
		errs = append(errs, errors.New("at least one runner must be configured"))
	}

	seen := make(map[string]bool)
	for i, r := range c.Runners {
		prefix := fmt.Sprintf("runners[%d]", i)
		if !nameRE.MatchString(r.Name) {
			errs = append(errs, fmt.Errorf("%s: name %q must be 1-40 lowercase letters, digits or dashes", prefix, r.Name))
		}
		if seen[r.Name] {
			errs = append(errs, fmt.Errorf("%s: duplicate name %q", prefix, r.Name))
		}
		seen[r.Name] = true

		if r.Image == "" {
			errs = append(errs, fmt.Errorf("%s: image is required", prefix))
		}
		if r.VCPUs < 1 {
			errs = append(errs, fmt.Errorf("%s: vcpus must be at least 1", prefix))
		}
		if r.MemoryMB < 1 {
			errs = append(errs, fmt.Errorf("%s: memory_mb is required", prefix))
		}
		if r.DiskMB < 1 {
			errs = append(errs, fmt.Errorf("%s: disk_mb must be positive", prefix))
		}
		if r.MaxRunners < 1 {
			errs = append(errs, fmt.Errorf("%s: max_runners must be at least 1", prefix))
		}
	}

	return errors.Join(errs...)
}

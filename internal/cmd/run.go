package cmd

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/actions/scaleset"
	"golang.org/x/sync/errgroup"
	"unikraft.com/cloud/sdk/platform"

	"github.com/utibeabasi6/ukc-runner-controller/internal/config"
	"github.com/utibeabasi6/ukc-runner-controller/internal/controller"
	"github.com/utibeabasi6/ukc-runner-controller/internal/store"
	"github.com/utibeabasi6/ukc-runner-controller/internal/web"
)

type RunCmd struct {
	Config   string `help:"Path to the YAML configuration file." default:"config.yaml" env:"UKC_RUNNER_CONTROLLER_CONFIG"`
	Database string `help:"Path to the SQLite database file." default:"ukc-runner-controller.db" env:"UKC_RUNNER_CONTROLLER_DATABASE"`
	Listen   string `help:"Address for the dashboard and the health check." default:"127.0.0.1:8080" env:"UKC_RUNNER_CONTROLLER_LISTEN"`

	GitHubToken   string `name:"github-token" help:"GitHub personal access token. Not needed when the config has a GitHub App." env:"GITHUB_TOKEN"`
	UnikraftToken string `help:"Unikraft Cloud API token." required:"" env:"UKC_TOKEN"`

	DashboardUsername string `help:"Username for basic auth on the dashboard." env:"UKC_RUNNER_CONTROLLER_DASHBOARD_USERNAME"`
	DashboardPassword string `help:"Password for basic auth on the dashboard." env:"UKC_RUNNER_CONTROLLER_DASHBOARD_PASSWORD"`
	InsecureDashboard bool   `help:"Serve the dashboard without auth on any address, for use behind an authenticating proxy." hidden:"" env:"UKC_RUNNER_CONTROLLER_INSECURE_DASHBOARD"`

	LogLevel  string `help:"Log level." default:"info" enum:"debug,info,warn,error" env:"UKC_RUNNER_CONTROLLER_LOG_LEVEL"`
	LogFormat string `help:"Log format." default:"text" enum:"text,json" env:"UKC_RUNNER_CONTROLLER_LOG_FORMAT"`
}

func (c *RunCmd) Run() error {
	var level slog.Level
	if err := level.UnmarshalText([]byte(c.LogLevel)); err != nil {
		return err
	}
	opts := &slog.HandlerOptions{Level: level}
	logger := slog.New(slog.NewTextHandler(os.Stderr, opts))
	if c.LogFormat == "json" {
		logger = slog.New(slog.NewJSONHandler(os.Stderr, opts))
	}

	if (c.DashboardUsername == "") != (c.DashboardPassword == "") {
		return errors.New("set both the dashboard username and password, or neither")
	}
	if c.DashboardUsername == "" && !c.InsecureDashboard && !isLoopback(c.Listen) {
		return fmt.Errorf("the dashboard on %s would be open to the network: set the dashboard username and password, "+
			"or pass --insecure-dashboard if a proxy authenticates requests", c.Listen)
	}

	cfg, err := config.Load(c.Config)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	v, commit := buildVersion()
	info := scaleset.SystemInfo{System: "ukc-runner-controller", Version: v, CommitSHA: commit}
	ghLogger := scaleset.WithLogger(logger.With("component", "scaleset"))

	var gh *scaleset.Client
	if app := cfg.GitHub.App; app != nil {
		key, err := os.ReadFile(app.PrivateKeyFile)
		if err != nil {
			return fmt.Errorf("reading GitHub App private key: %w", err)
		}
		gh, err = scaleset.NewClientWithGitHubApp(scaleset.ClientWithGitHubAppConfig{
			GitHubConfigURL: cfg.GitHub.URL,
			GitHubAppAuth: scaleset.GitHubAppAuth{
				ClientID:       app.ClientID,
				InstallationID: app.InstallationID,
				PrivateKey:     string(key),
			},
			SystemInfo: info,
		}, ghLogger)
		if err != nil {
			return fmt.Errorf("creating GitHub client: %w", err)
		}
	} else {
		if c.GitHubToken == "" {
			return errors.New("configure github.app or set GITHUB_TOKEN")
		}
		gh, err = scaleset.NewClientWithPersonalAccessToken(scaleset.NewClientWithPersonalAccessTokenConfig{
			GitHubConfigURL:     cfg.GitHub.URL,
			PersonalAccessToken: c.GitHubToken,
			SystemInfo:          info,
		}, ghLogger)
		if err != nil {
			return fmt.Errorf("creating GitHub client: %w", err)
		}
	}

	st, err := store.Open(ctx, c.Database)
	if err != nil {
		return fmt.Errorf("opening database: %w", err)
	}
	defer st.Close()

	owner, err := os.Hostname()
	if err != nil {
		owner = "ukc-runner-controller"
	}

	ctrl := controller.New(controller.Options{
		Config: cfg,
		GitHub: gh,
		UKC: platform.NewClient(
			platform.WithToken(c.UnikraftToken),
			platform.WithHTTPClient(controller.NewUKCHTTPClient()),
			platform.WithDefaultMetro(cfg.Unikraft.Metro),
			platform.WithUserAgent("ukc-runner-controller/"+v),
		),
		Store:  st,
		Logger: logger,
		Owner:  owner,
	})

	srv := &http.Server{
		Addr: c.Listen,
		Handler: web.New(web.Options{
			Store:      st,
			Controller: ctrl,
			Logger:     logger,
			Version:    v,
			Username:   c.DashboardUsername,
			Password:   c.DashboardPassword,
		}).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	logger.Info("starting", "version", v, "listen", c.Listen, "github", cfg.GitHub.URL, "metro", cfg.Unikraft.Metro)

	g, ctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		return ctrl.Run(ctx)
	})
	g.Go(func() error {
		if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	})
	g.Go(func() error {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(shutdown)
	})
	return g.Wait()
}

func isLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

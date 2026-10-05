package web

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/utibeabasi6/ukc-runner-controller/internal/config"
	"github.com/utibeabasi6/ukc-runner-controller/internal/controller"
	"github.com/utibeabasi6/ukc-runner-controller/internal/store"
)

func newTestServer(t *testing.T, username, password string) http.Handler {
	t.Helper()
	ctx := t.Context()

	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	now := time.Now()
	started, completed := now.Add(-time.Minute), now
	exit := 1
	seed := []error{
		st.CreateRunner(ctx, store.Runner{Name: "small-abc", ScaleSet: "small", Image: "acme/runner:1", VCPUs: 1, MemoryMB: 2048, DiskMB: 10240, State: store.RunnerIdle, CreatedAt: now}),
		st.CreateRunner(ctx, store.Runner{Name: "small-def", ScaleSet: "small", Image: "acme/runner:1", VCPUs: 1, MemoryMB: 2048, DiskMB: 10240, State: store.RunnerIdle, CreatedAt: now}),
		st.FinishRunner(ctx, "small-def", store.RunnerFailed, "instance stopped before the runner took a job", now),
		st.RecordInstanceExit(ctx, "small-def", store.InstanceExit{ExitCode: &exit, LogTail: "Listening for Jobs\n<script>alert(1)</script>", DeletedAt: now}),
		st.RecordJob(ctx, store.Job{RequestID: 7, ScaleSet: "small", Owner: "acme", Repository: "app", WorkflowRef: "acme/app/.github/workflows/ci.yml@refs/heads/main", DisplayName: "build", WorkflowRunID: 99, QueuedAt: now.Add(-2 * time.Minute)}),
		st.RecordJob(ctx, store.Job{RequestID: 7, RunnerName: "small-abc", Result: "failed", StartedAt: &started, CompletedAt: &completed}),
		st.AddEvent(ctx, store.Event{ScaleSet: "small", Level: store.LevelError, Message: "listener stopped"}),
	}
	for _, err := range seed {
		if err != nil {
			t.Fatal(err)
		}
	}

	cfg := &config.Config{
		GitHub:   config.GitHub{URL: "https://github.com/acme"},
		Unikraft: config.Unikraft{Metro: "fra"},
		Runners:  []config.RunnerConfig{{Name: "small", Labels: []string{"small"}, Image: "acme/runner:1", VCPUs: 1, MemoryMB: 2048, DiskMB: 10240, MaxRunners: 4}},
	}
	ctrl := controller.New(controller.Options{Config: cfg, Store: st, Logger: slog.New(slog.DiscardHandler)})

	return New(Options{
		Store:      st,
		Controller: ctrl,
		Logger:     slog.New(slog.DiscardHandler),
		Version:    "test",
		Username:   username,
		Password:   password,
	}).Handler()
}

func TestPagesRender(t *testing.T) {
	h := newTestServer(t, "", "")

	tests := []struct {
		path   string
		status int
		want   string
	}{
		{"/", http.StatusOK, "Active runners"},
		{"/jobs", http.StatusOK, "build"},
		{"/jobs?status=failed&scale_set=small", http.StatusOK, "acme/app"},
		{"/jobs?status=running", http.StatusOK, "No jobs match"},
		{"/jobs/7", http.StatusOK, "https://github.com/acme/app/actions/runs/99"},
		{"/jobs/8", http.StatusNotFound, "does not exist"},
		{"/jobs/abc", http.StatusNotFound, "does not exist"},
		{"/runners", http.StatusOK, "small-abc"},
		{"/runners/small-def", http.StatusOK, "&lt;script&gt;"},
		{"/runners/missing", http.StatusNotFound, "does not exist"},
		{"/scale-sets", http.StatusOK, "Quota not loaded yet"},
		{"/nope", http.StatusNotFound, "does not exist"},
		{"/static/css/app.css", http.StatusOK, "--ok-fg"},
		{"/healthz", http.StatusOK, "ok"},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, tt.path, nil))
			body, _ := io.ReadAll(rec.Body)
			if rec.Code != tt.status {
				t.Errorf("status = %d, want %d", rec.Code, tt.status)
			}
			if !strings.Contains(string(body), tt.want) {
				t.Errorf("body does not contain %q:\n%s", tt.want, body)
			}
		})
	}
}

func TestPartialRenderSkipsLayout(t *testing.T) {
	h := newTestServer(t, "", "")
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
	req.Header.Set("X-Partial", "1")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if strings.Contains(rec.Body.String(), "<html") {
		t.Error("partial response contains the page layout")
	}
	if !strings.Contains(rec.Body.String(), "Active runners") {
		t.Error("partial response is missing the page content")
	}
}

func TestBasicAuth(t *testing.T) {
	h := newTestServer(t, "admin", "secret")

	tests := []struct {
		name       string
		path       string
		user, pass string
		status     int
	}{
		{"no credentials", "/", "", "", http.StatusUnauthorized},
		{"wrong password", "/", "admin", "nope", http.StatusUnauthorized},
		{"valid", "/", "admin", "secret", http.StatusOK},
		{"health check stays open", "/healthz", "", "", http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, tt.path, nil)
			if tt.user != "" {
				req.SetBasicAuth(tt.user, tt.pass)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tt.status {
				t.Errorf("status = %d, want %d", rec.Code, tt.status)
			}
		})
	}
}

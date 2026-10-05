package web

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"unikraft.com/cloud/sdk/platform"

	"github.com/utibeabasi6/ukc-runner-controller/internal/config"
	"github.com/utibeabasi6/ukc-runner-controller/internal/controller"
	"github.com/utibeabasi6/ukc-runner-controller/internal/store"
)

const (
	navOverview  = "overview"
	navJobs      = "jobs"
	navRunners   = "runners"
	navScaleSets = "scale-sets"
)

var jobStatuses = []string{store.JobQueued, store.JobRunning, store.JobSucceeded, store.JobFailed, store.JobCanceled}

type scaleSetView struct {
	Config config.RunnerConfig
	Status controller.ScaleSetStatus
	Queued int
	Idle   int
	Busy   int
}

type overviewView struct {
	ScaleSets []scaleSetView
	Summary   store.Summary
	Assigned  int
	Queued    int
	Idle      int
	Busy      int
	Jobs      []store.Job
	Events    []store.Event
	Updated   time.Time
}

type jobsView struct {
	Jobs      []store.Job
	ScaleSets []string
	Filter    store.JobFilter
}

type jobView struct {
	Job    store.Job
	RunURL string
}

type runnersView struct {
	Active []store.Runner
	Recent []store.Runner
}

type runnerView struct {
	Runner store.Runner
	Jobs   []store.Job
	Logs   string
	Live   bool
}

type scaleSetsView struct {
	ScaleSets []scaleSetView
	Quota     *platform.Quotas
	Metro     string
	GitHubURL string
}

func (s *Server) scaleSetViews(active []store.Runner) []scaleSetView {
	queued := make(map[string]int)
	idle := make(map[string]int)
	busy := make(map[string]int)
	for _, r := range active {
		switch r.State {
		case store.RunnerQueued:
			queued[r.ScaleSet]++
		case store.RunnerIdle:
			idle[r.ScaleSet]++
		case store.RunnerBusy:
			busy[r.ScaleSet]++
		}
	}

	var out []scaleSetView
	for _, st := range s.ctrl.Statuses() {
		name := st.Config.Name
		out = append(out, scaleSetView{
			Config: st.Config,
			Status: st,
			Queued: queued[name],
			Idle:   idle[name],
			Busy:   busy[name],
		})
	}
	return out
}

func (s *Server) overview(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	summary, err := s.store.Summary(ctx, time.Now().Add(-24*time.Hour))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	active, err := s.store.Runners(ctx, store.RunnerFilter{Active: true})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	jobs, err := s.store.Jobs(ctx, store.JobFilter{Limit: 10})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	events, err := s.store.Events(ctx, 8)
	if err != nil {
		s.fail(w, r, err)
		return
	}

	v := overviewView{
		ScaleSets: s.scaleSetViews(active),
		Summary:   summary,
		Jobs:      jobs,
		Events:    events,
		Updated:   time.Now(),
	}
	for _, ss := range v.ScaleSets {
		v.Assigned += ss.Status.Statistics.TotalAssignedJobs
		v.Queued += ss.Queued
		v.Idle += ss.Idle
		v.Busy += ss.Busy
	}
	s.render(w, r, http.StatusOK, "Overview", navOverview, overviewPage(v))
}

func (s *Server) jobs(w http.ResponseWriter, r *http.Request) {
	f := store.JobFilter{
		ScaleSet: r.URL.Query().Get("scale_set"),
		Status:   r.URL.Query().Get("status"),
		Limit:    200,
	}
	jobs, err := s.store.Jobs(r.Context(), f)
	if err != nil {
		s.fail(w, r, err)
		return
	}

	v := jobsView{Jobs: jobs, Filter: f}
	for _, rc := range s.ctrl.Config().Runners {
		v.ScaleSets = append(v.ScaleSets, rc.Name)
	}
	s.render(w, r, http.StatusOK, "Jobs", navJobs, jobsPage(v))
}

func (s *Server) job(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.notFound(w, r)
		return
	}
	j, err := s.store.Job(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		s.notFound(w, r)
		return
	} else if err != nil {
		s.fail(w, r, err)
		return
	}

	v := jobView{Job: j}
	if u, err := url.Parse(s.ctrl.Config().GitHub.URL); err == nil && j.WorkflowRunID != 0 && j.Repository != "" {
		v.RunURL = fmt.Sprintf("%s://%s/%s/%s/actions/runs/%d", u.Scheme, u.Host, j.Owner, j.Repository, j.WorkflowRunID)
	}
	s.render(w, r, http.StatusOK, "Job "+strconv.FormatInt(id, 10), navJobs, jobPage(v))
}

func (s *Server) runners(w http.ResponseWriter, r *http.Request) {
	active, err := s.store.Runners(r.Context(), store.RunnerFilter{Active: true})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	recent, err := s.store.Runners(r.Context(), store.RunnerFilter{
		States: []store.RunnerState{store.RunnerFinished, store.RunnerFailed, store.RunnerRemoved},
		Limit:  100,
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "Runners", navRunners, runnersPage(runnersView{Active: active, Recent: recent}))
}

func (s *Server) runner(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rn, err := s.store.Runner(ctx, r.PathValue("name"))
	if errors.Is(err, store.ErrNotFound) {
		s.notFound(w, r)
		return
	} else if err != nil {
		s.fail(w, r, err)
		return
	}
	jobs, err := s.store.Jobs(ctx, store.JobFilter{RunnerName: rn.Name})
	if err != nil {
		s.fail(w, r, err)
		return
	}

	v := runnerView{Runner: rn, Jobs: jobs, Logs: rn.LogTail}
	if rn.InstanceDeletedAt == nil && rn.InstanceUUID != "" {
		s.logsMu.Lock()
		now := time.Now()
		for uuid, c := range s.logs {
			if now.Sub(c.at) > logCacheTTL {
				delete(s.logs, uuid)
			}
		}
		cached, ok := s.logs[rn.InstanceUUID]
		if !ok {
			text, err := s.ctrl.InstanceLogs(ctx, rn.InstanceUUID)
			if err != nil {
				s.log.Warn("reading instance logs", "runner", rn.Name, "error", err)
			} else {
				cached, ok = cachedLogs{text: text, at: now}, true
				s.logs[rn.InstanceUUID] = cached
			}
		}
		s.logsMu.Unlock()
		if ok {
			v.Logs, v.Live = cached.text, true
		}
	}
	s.render(w, r, http.StatusOK, rn.Name, navRunners, runnerPage(v))
}

func (s *Server) scaleSets(w http.ResponseWriter, r *http.Request) {
	active, err := s.store.Runners(r.Context(), store.RunnerFilter{Active: true})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	cfg := s.ctrl.Config()
	s.render(w, r, http.StatusOK, "Scale sets", navScaleSets, scaleSetsPage(scaleSetsView{
		ScaleSets: s.scaleSetViews(active),
		Quota:     s.ctrl.Quota(),
		Metro:     cfg.Unikraft.Metro,
		GitHubURL: cfg.GitHub.URL,
	}))
}

func (s *Server) notFound(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, http.StatusNotFound, "Not found", "", errorPage("404", "This page does not exist."))
}

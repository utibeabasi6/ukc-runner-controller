package controller

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/actions/scaleset"
	"unikraft.com/cloud/sdk/platform"

	"github.com/utibeabasi6/ukc-runner-controller/internal/config"
	"github.com/utibeabasi6/ukc-runner-controller/internal/store"
)

// fakeGitHub serves the subset of the GitHub and Actions service APIs that
// the scaleset client uses, for an organization named "acme".
type fakeGitHub struct {
	*httptest.Server
	queue chan string

	mu       sync.Mutex
	acquired []int64
	runners  map[int]string
	removed  []int
}

func newFakeGitHub(t *testing.T) *fakeGitHub {
	f := &fakeGitHub{queue: make(chan string, 16), runners: make(map[int]string)}
	tenant := "/tenant/123/_apis/runtime/runnerscalesets"
	stats := `{"totalAssignedJobs":0}`

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v3/orgs/acme/actions/runners/registration-token", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		io.WriteString(w, `{"token":"registration"}`)
	})
	mux.HandleFunc("POST /api/v3/actions/runner-registration", func(w http.ResponseWriter, r *http.Request) {
		enc := base64.RawURLEncoding
		claims := fmt.Sprintf(`{"exp":%d}`, time.Now().Add(time.Hour).Unix())
		jwt := enc.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`)) + "." + enc.EncodeToString([]byte(claims)) + ".sig"
		w.WriteHeader(http.StatusCreated)
		fmt.Fprintf(w, `{"url":%q,"token":%q}`, f.URL+"/tenant/123/", jwt)
	})
	mux.HandleFunc("GET "+tenant, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"count":0,"value":[]}`)
	})
	mux.HandleFunc("POST "+tenant, func(w http.ResponseWriter, r *http.Request) {
		var ss scaleset.RunnerScaleSet
		json.NewDecoder(r.Body).Decode(&ss)
		ss.ID = 1
		json.NewEncoder(w).Encode(ss)
	})
	mux.HandleFunc("POST "+tenant+"/1/sessions", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"sessionId":"6f1c2c1e-7d0a-4b8e-9a39-2f6f7a1e0c11","ownerName":"test",
			"messageQueueUrl":%q,"messageQueueAccessToken":"queue","statistics":%s}`, f.URL+"/queue", stats)
	})
	mux.HandleFunc("DELETE "+tenant+"/1/sessions/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /queue", func(w http.ResponseWriter, r *http.Request) {
		select {
		case msg := <-f.queue:
			io.WriteString(w, msg)
		case <-time.After(50 * time.Millisecond):
			w.WriteHeader(http.StatusAccepted)
		}
	})
	mux.HandleFunc("DELETE /queue/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST "+tenant+"/1/acquirejobs", func(w http.ResponseWriter, r *http.Request) {
		var ids []int64
		json.NewDecoder(r.Body).Decode(&ids)
		f.mu.Lock()
		f.acquired = append(f.acquired, ids...)
		f.mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{"count": len(ids), "value": ids})
	})
	mux.HandleFunc("POST "+tenant+"/1/generatejitconfig", func(w http.ResponseWriter, r *http.Request) {
		var setting scaleset.RunnerScaleSetJitRunnerSetting
		json.NewDecoder(r.Body).Decode(&setting)
		f.mu.Lock()
		id := len(f.runners) + 100
		f.runners[id] = setting.Name
		f.mu.Unlock()
		fmt.Fprintf(w, `{"runner":{"id":%d,"name":%q,"runnerScaleSetId":1},"encodedJITConfig":"jit-%s"}`, id, setting.Name, setting.Name)
	})
	mux.HandleFunc("DELETE /tenant/123/_apis/distributedtask/pools/0/agents/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.Atoi(r.PathValue("id"))
		f.mu.Lock()
		f.removed = append(f.removed, id)
		f.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})

	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Close)
	return f
}

func (f *fakeGitHub) send(id int, assigned int, jobs ...map[string]any) {
	body, _ := json.Marshal(jobs)
	msg, _ := json.Marshal(map[string]any{
		"messageId":   id,
		"messageType": "RunnerScaleSetJobMessages",
		"body":        string(body),
		"statistics":  map[string]int{"totalAssignedJobs": assigned},
	})
	f.queue <- string(msg)
}

// fakeUKC serves the instance and quota endpoints of the Unikraft Cloud API.
type fakeUKC struct {
	*httptest.Server

	mu        sync.Mutex
	instances map[string]map[string]any
	created   []platform.CreateInstanceRequest
	// rateLimited is the number of create requests to reject with a 429.
	rateLimited int
	// unlisted instances exist but are left out of the full list, as they
	// would be on a later page.
	unlisted map[string]bool
	// undeletable instances fail deletion with a per-item error.
	undeletable map[string]bool
}

func newFakeUKC(t *testing.T) *fakeUKC {
	f := &fakeUKC{instances: make(map[string]map[string]any), unlisted: make(map[string]bool), undeletable: make(map[string]bool)}
	reply := func(w http.ResponseWriter, data any) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"status": "success", "data": data})
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/users/quotas", func(w http.ResponseWriter, r *http.Request) {
		reply(w, map[string]any{"quotas": []any{map[string]any{
			"limits": map[string]int{"min_vcpus": 1, "max_vcpus": 4, "min_memory_mb": 16, "max_memory_mb": 8192, "min_volume_mb": 8, "max_volume_mb": 20480},
		}}})
	})
	mux.HandleFunc("POST /v1/instances", func(w http.ResponseWriter, r *http.Request) {
		var req platform.CreateInstanceRequest
		json.NewDecoder(r.Body).Decode(&req)
		for _, v := range req.Volumes {
			if (v.Name != nil || v.Uuid != nil) && v.SizeMb != nil {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				io.WriteString(w, `{"status":"error","message":"Either a reference to an existing volume or the description of a new volume can be provided"}`)
				return
			}
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.rateLimited > 0 {
			f.rateLimited--
			w.Header().Set("Retry-After", "120")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		f.created = append(f.created, req)
		inst := map[string]any{"uuid": "uuid-" + *req.Name, "name": *req.Name, "state": "running", "status": "success"}
		f.instances[*req.Name] = inst
		reply(w, map[string]any{"instances": []any{inst}})
	})
	mux.HandleFunc("GET /v1/instances", func(w http.ResponseWriter, r *http.Request) {
		var ids []platform.NameOrUUID
		json.NewDecoder(r.Body).Decode(&ids)
		f.mu.Lock()
		defer f.mu.Unlock()
		var list []any
		for _, id := range ids {
			if inst, ok := f.instances[*id.Name]; ok {
				list = append(list, inst)
			} else {
				list = append(list, map[string]any{"name": *id.Name, "status": "error", "error": platform.APIHTTPErrorNotFound, "message": "instance not found"})
			}
		}
		if len(ids) == 0 {
			for name, inst := range f.instances {
				if !f.unlisted[name] {
					list = append(list, inst)
				}
			}
		}
		reply(w, map[string]any{"instances": list})
	})
	mux.HandleFunc("GET /v1/instances/{uuid}/log", func(w http.ResponseWriter, r *http.Request) {
		reply(w, map[string]any{"instances": []any{map[string]any{
			"output": base64.StdEncoding.EncodeToString([]byte("Job completed with result: Succeeded")),
		}}})
	})
	mux.HandleFunc("DELETE /v1/instances/{uuid}", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		for name, inst := range f.instances {
			if inst["uuid"] != r.PathValue("uuid") {
				continue
			}
			if f.undeletable[name] {
				reply(w, map[string]any{"instances": []any{map[string]any{"status": "error", "error": platform.APIHTTPErrorFailedOperation, "message": "delete failed"}}})
				return
			}
			delete(f.instances, name)
		}
		reply(w, map[string]any{"instances": []any{map[string]any{"status": "success"}}})
	})

	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Close)
	return f
}

func (f *fakeUKC) stop(name string, exitCode int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.instances[name]["state"] = "stopped"
	f.instances[name]["exit_code"] = exitCode
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

var smallRunner = config.RunnerConfig{Name: "small", Labels: []string{"small"}, Image: "acme/runner:1", VCPUs: 1, MemoryMB: 2048, DiskMB: 4096, MaxRunners: 2}

func newTestController(t *testing.T, gh *fakeGitHub, ukc *fakeUKC) (*Controller, *store.Store) {
	t.Helper()
	st, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	cfg := &config.Config{
		GitHub:  config.GitHub{URL: gh.URL + "/acme", RunnerGroup: config.DefaultRunnerGroup},
		Runners: []config.RunnerConfig{smallRunner},
	}
	ghClient, err := scaleset.NewClientWithPersonalAccessToken(scaleset.NewClientWithPersonalAccessTokenConfig{
		GitHubConfigURL:     cfg.GitHub.URL,
		PersonalAccessToken: "pat",
	})
	if err != nil {
		t.Fatal(err)
	}
	c := New(Options{
		Config: cfg,
		GitHub: ghClient,
		UKC: platform.NewClient(
			platform.WithDefaultEndpoint(ukc.URL),
			platform.WithToken("token"),
			platform.WithHTTPClient(NewUKCHTTPClient()),
		),
		Store:  st,
		Logger: slog.New(slog.DiscardHandler),
		Owner:  "test",
	})
	return c, st
}

func TestControllerRunsOneInstancePerJob(t *testing.T) {
	gh := newFakeGitHub(t)
	ukc := newFakeUKC(t)
	c, st := newTestController(t, gh, ukc)

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error)
	go func() { done <- c.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	job := func(typ string, id int64, runner string) map[string]any {
		return map[string]any{"messageType": typ, "runnerRequestId": id, "repositoryName": "app", "ownerName": "acme", "runnerName": runner, "result": "succeeded"}
	}

	gh.send(1, 3, job("JobAvailable", 11, ""), job("JobAvailable", 12, ""), job("JobAvailable", 13, ""))
	eventually(t, "two instances", func() bool {
		ukc.mu.Lock()
		defer ukc.mu.Unlock()
		return len(ukc.created) == 2
	})

	gh.mu.Lock()
	if len(gh.acquired) != 3 {
		t.Errorf("acquired %v, want three jobs", gh.acquired)
	}
	gh.mu.Unlock()

	ukc.mu.Lock()
	req := ukc.created[0]
	ukc.mu.Unlock()
	if req.Env[jitConfigEnv] != "jit-"+*req.Name {
		t.Errorf("instance env = %v, want the runner's JIT config", req.Env)
	}
	if len(req.Volumes) != 1 || req.Volumes[0].At != runnerWorkDir || *req.Volumes[0].SizeMb != 4096 {
		t.Errorf("instance volumes = %+v, want one 4096 MiB volume at %s", req.Volumes, runnerWorkDir)
	}
	if *req.Vcpus != 1 || *req.MemoryMb != 2048 || *req.RestartPolicy != platform.CreateInstanceRequestRestartPolicyNever {
		t.Errorf("unexpected instance shape %+v", req)
	}

	first := *req.Name
	gh.send(2, 2, job("JobStarted", 11, first), job("JobCompleted", 11, first))
	eventually(t, "a replacement instance", func() bool {
		ukc.mu.Lock()
		defer ukc.mu.Unlock()
		return len(ukc.created) == 3
	})

	ukc.stop(first, 0)
	if err := c.reconcile(ctx); err != nil {
		t.Fatal(err)
	}

	r, err := st.Runner(ctx, first)
	if err != nil {
		t.Fatal(err)
	}
	if r.State != store.RunnerFinished || r.InstanceDeletedAt == nil || r.ExitCode == nil || *r.ExitCode != 0 {
		t.Errorf("runner after collection = %+v", r)
	}
	if r.LogTail == "" {
		t.Error("log tail was not recorded")
	}

	j, err := st.Job(ctx, 11)
	if err != nil {
		t.Fatal(err)
	}
	if j.Status() != "succeeded" || j.RunnerName != first {
		t.Errorf("job 11 = %+v", j)
	}

	ukc.mu.Lock()
	_, exists := ukc.instances[first]
	ukc.mu.Unlock()
	if exists {
		t.Error("stopped instance was not deleted")
	}
}

func TestReconcileFailsRunnerWhoseInstanceStopsWhileIdle(t *testing.T) {
	gh := newFakeGitHub(t)
	ukc := newFakeUKC(t)
	c, st := newTestController(t, gh, ukc)

	ctx := t.Context()
	err := st.CreateRunner(ctx, store.Runner{Name: "small-crash", ScaleSet: "small", GitHubRunnerID: 42, Image: "img", State: store.RunnerIdle, CreatedAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	ukc.instances["small-crash"] = map[string]any{"uuid": "uuid-crash", "name": "small-crash", "state": "stopped", "exit_code": 1}

	if err := c.reconcile(ctx); err != nil {
		t.Fatal(err)
	}

	r, err := st.Runner(ctx, "small-crash")
	if err != nil {
		t.Fatal(err)
	}
	if r.State != store.RunnerFailed || r.Error == "" {
		t.Errorf("runner = %+v, want failed with a reason", r)
	}
	gh.mu.Lock()
	defer gh.mu.Unlock()
	if len(gh.removed) != 1 || gh.removed[0] != 42 {
		t.Errorf("removed runners = %v, want [42]", gh.removed)
	}
}

func TestValidateLimits(t *testing.T) {
	limits := &platform.QuotasLimits{MinVcpus: new(int64(1)), MaxVcpus: new(int64(1)), MaxMemoryMb: new(int64(8192)), MaxVolumeMb: new(int64(20480))}

	ok := []config.RunnerConfig{{Name: "a", VCPUs: 1, MemoryMB: 4096, DiskMB: 10240}}
	if err := validateLimits(ok, limits); err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	tooBig := []config.RunnerConfig{{Name: "b", VCPUs: 2, MemoryMB: 16384, DiskMB: 10240}}
	if err := validateLimits(tooBig, limits); err == nil {
		t.Error("expected vcpus and memory to be rejected")
	}
}

func TestRateLimitedRunnerIsQueuedAndRetried(t *testing.T) {
	gh := newFakeGitHub(t)
	ukc := newFakeUKC(t)
	c, st := newTestController(t, gh, ukc)
	ctx := t.Context()
	s := &scaler{c: c, rc: smallRunner, id: 1, log: c.log}

	ukc.rateLimited = 2
	if err := s.startRunner(ctx); err != nil {
		t.Fatal(err)
	}

	queued, err := st.Runners(ctx, store.RunnerFilter{States: []store.RunnerState{store.RunnerQueued}})
	if err != nil {
		t.Fatal(err)
	}
	if len(queued) != 1 || !c.retries.has(queued[0].Name) {
		t.Fatalf("queued runners = %+v, want one runner in the retry queue", queued)
	}
	name := queued[0].Name

	if until := time.Until(c.retries.notBefore); until < 110*time.Second {
		t.Errorf("retry in %v, want the Retry-After of 120s", until)
	}
	c.retryQueued(ctx)
	if len(ukc.created) != 0 {
		t.Error("retried before the backoff expired")
	}

	c.retries.notBefore = time.Time{}
	c.retryQueued(ctx)
	if !c.retries.has(name) {
		t.Error("runner left the queue while still rate limited")
	}

	c.retries.notBefore = time.Time{}
	c.retryQueued(ctx)
	if c.retries.has(name) {
		t.Error("runner is still queued after a successful retry")
	}

	r, err := st.Runner(ctx, name)
	if err != nil {
		t.Fatal(err)
	}
	if r.State != store.RunnerIdle || r.InstanceUUID != "uuid-"+name {
		t.Errorf("runner after retry = %+v, want idle with an instance", r)
	}
	if len(ukc.created) != 1 || ukc.created[0].Env[jitConfigEnv] != "jit-"+name {
		t.Errorf("created instances = %+v, want one with the original JIT config", ukc.created)
	}
}

func TestScaleDownDropsQueuedRunnersFirst(t *testing.T) {
	gh := newFakeGitHub(t)
	ukc := newFakeUKC(t)
	c, st := newTestController(t, gh, ukc)
	ctx := t.Context()
	s := &scaler{c: c, rc: smallRunner, id: 1, log: c.log}

	ukc.rateLimited = 1
	if err := s.startRunner(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.converge(ctx, 0); err != nil {
		t.Fatal(err)
	}

	runners, err := st.Runners(ctx, store.RunnerFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(runners) != 1 || runners[0].State != store.RunnerRemoved || c.retries.has(runners[0].Name) {
		t.Errorf("runners = %+v, want the queued runner removed", runners)
	}
	if len(gh.removed) != 1 {
		t.Errorf("removed GitHub runners = %v, want one", gh.removed)
	}
}

func TestReconcileFailsQueuedRunnerLostOnRestart(t *testing.T) {
	gh := newFakeGitHub(t)
	ukc := newFakeUKC(t)
	c, st := newTestController(t, gh, ukc)
	ctx := t.Context()

	for _, name := range []string{"small-lost", "small-waiting"} {
		if err := st.CreateRunner(ctx, store.Runner{Name: name, ScaleSet: "small", GitHubRunnerID: 7, Image: "img", State: store.RunnerIdle, CreatedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
		if err := st.QueueRunner(ctx, name); err != nil {
			t.Fatal(err)
		}
	}
	c.retries.push(pendingInstance{scaleSet: "small", req: platform.CreateInstanceRequest{Name: new("small-waiting")}}, 0)

	if err := c.reconcile(ctx); err != nil {
		t.Fatal(err)
	}

	lost, err := st.Runner(ctx, "small-lost")
	if err != nil {
		t.Fatal(err)
	}
	if lost.State != store.RunnerFailed {
		t.Errorf("lost runner state = %s, want failed", lost.State)
	}
	waiting, err := st.Runner(ctx, "small-waiting")
	if err != nil {
		t.Fatal(err)
	}
	if waiting.State != store.RunnerQueued {
		t.Errorf("waiting runner state = %s, want queued", waiting.State)
	}
}

func TestReconcileConfirmsInstancesMissingFromList(t *testing.T) {
	gh := newFakeGitHub(t)
	ukc := newFakeUKC(t)
	c, st := newTestController(t, gh, ukc)
	ctx := t.Context()

	created := time.Now().Add(-2 * createGracePeriod)
	for _, name := range []string{"small-paged", "small-gone"} {
		if err := st.CreateRunner(ctx, store.Runner{Name: name, ScaleSet: "small", GitHubRunnerID: 9, Image: "img", State: store.RunnerIdle, CreatedAt: created}); err != nil {
			t.Fatal(err)
		}
	}
	ukc.instances["small-paged"] = map[string]any{"uuid": "uuid-paged", "name": "small-paged", "state": "running"}
	ukc.unlisted["small-paged"] = true

	if err := c.reconcile(ctx); err != nil {
		t.Fatal(err)
	}

	for name, want := range map[string]store.RunnerState{"small-paged": store.RunnerIdle, "small-gone": store.RunnerFailed} {
		r, err := st.Runner(ctx, name)
		if err != nil {
			t.Fatal(err)
		}
		if r.State != want {
			t.Errorf("%s state = %s, want %s", name, r.State, want)
		}
	}
}

func TestReconcileRetriesFailedDelete(t *testing.T) {
	gh := newFakeGitHub(t)
	ukc := newFakeUKC(t)
	c, st := newTestController(t, gh, ukc)
	ctx := t.Context()

	if err := st.CreateRunner(ctx, store.Runner{Name: "small-stuck", ScaleSet: "small", Image: "img", State: store.RunnerIdle, CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := st.FinishRunner(ctx, "small-stuck", store.RunnerRemoved, "scaled down", time.Now()); err != nil {
		t.Fatal(err)
	}
	ukc.instances["small-stuck"] = map[string]any{"uuid": "uuid-stuck", "name": "small-stuck", "state": "running"}
	ukc.undeletable["small-stuck"] = true

	if err := c.reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if r, _ := st.Runner(ctx, "small-stuck"); r.InstanceDeletedAt != nil {
		t.Fatal("instance recorded as deleted although the delete failed")
	}

	ukc.undeletable["small-stuck"] = false
	if err := c.reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if r, _ := st.Runner(ctx, "small-stuck"); r.InstanceDeletedAt == nil {
		t.Error("instance not recorded as deleted after a successful retry")
	}
}

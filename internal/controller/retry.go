package controller

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"sync"
	"time"

	"unikraft.com/cloud/sdk/platform"

	"github.com/utibeabasi6/ukc-runner-controller/internal/store"
)

const (
	retryInterval = 5 * time.Second
	minRetryDelay = 10 * time.Second
	maxRetryDelay = 5 * time.Minute
)

type rateLimitError struct {
	retryAfter time.Duration
}

func (e *rateLimitError) Error() string {
	return "rate limited by the Unikraft Cloud API"
}

type rateLimitTransport struct {
	base http.RoundTripper
}

func (t rateLimitTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	if err != nil || resp.StatusCode != http.StatusTooManyRequests {
		return resp, err
	}
	resp.Body.Close()
	secs, _ := strconv.Atoi(resp.Header.Get("Retry-After"))
	return nil, &rateLimitError{retryAfter: time.Duration(secs) * time.Second}
}

// NewUKCHTTPClient returns the HTTP client to pass to the Unikraft Cloud SDK.
// The SDK does not expose response status codes, so this client turns a 429
// response into an error that the controller can detect and retry.
func NewUKCHTTPClient() *http.Client {
	t := http.DefaultTransport.(*http.Transport).Clone()
	// The default SDK client disables keep-alives because of the proxy in
	// front of the API.
	t.DisableKeepAlives = true
	return &http.Client{Transport: rateLimitTransport{base: t}}
}

type pendingInstance struct {
	scaleSet string
	runnerID int
	req      platform.CreateInstanceRequest
}

// retryQueue holds instance creations that the API rejected with a 429. The
// rate limit applies to the whole account, so the queue backs off as a whole
// and retries the oldest entry first. Entries keep the runner's JIT config,
// which is a credential, so they only ever live in memory.
type retryQueue struct {
	mu        sync.Mutex
	items     []pendingInstance
	notBefore time.Time
	delay     time.Duration
}

func (q *retryQueue) push(p pendingInstance, retryAfter time.Duration) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.items = append(q.items, p)
	if time.Now().After(q.notBefore) {
		q.backoff(retryAfter)
	}
}

func (q *retryQueue) backoff(retryAfter time.Duration) {
	q.delay = min(max(q.delay*2, minRetryDelay), maxRetryDelay)
	q.notBefore = time.Now().Add(max(q.delay, retryAfter))
}

func (q *retryQueue) remove(name string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	i := slices.IndexFunc(q.items, func(p pendingInstance) bool { return *p.req.Name == name })
	if i < 0 {
		return false
	}
	q.items = slices.Delete(q.items, i, i+1)
	return true
}

func (q *retryQueue) has(name string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return slices.ContainsFunc(q.items, func(p pendingInstance) bool { return *p.req.Name == name })
}

func (c *Controller) retryLoop(ctx context.Context) {
	ticker := time.NewTicker(retryInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.retryQueued(ctx)
		}
	}
}

// retryQueued works through the queue in order and stops at the first 429.
// An entry stays in the queue while its request is in flight, so reconcile
// does not mistake its runner for one whose instance was lost.
func (c *Controller) retryQueued(ctx context.Context) {
	c.retries.mu.Lock()
	if time.Now().Before(c.retries.notBefore) {
		c.retries.mu.Unlock()
		return
	}
	due := slices.Clone(c.retries.items)
	c.retries.mu.Unlock()

	for _, p := range due {
		name := *p.req.Name
		uuid, err := c.createInstance(ctx, p.req)
		if ctx.Err() != nil {
			return
		}

		var rl *rateLimitError
		if errors.As(err, &rl) {
			c.retries.mu.Lock()
			c.retries.backoff(rl.retryAfter)
			c.retries.mu.Unlock()
			c.log.Warn("still rate limited, retrying queued runners later")
			return
		}
		if !c.retries.remove(name) {
			// Scale down dropped the runner while the request was in flight.
			// Reconcile deletes the instance if one was created.
			continue
		}

		c.retries.mu.Lock()
		c.retries.delay = 0
		c.retries.mu.Unlock()

		if err != nil {
			c.removeRunner(ctx, p.runnerID)
			if ferr := c.store.FinishRunner(ctx, name, store.RunnerFailed, err.Error(), time.Now()); ferr != nil {
				c.log.Error("recording failed runner", "runner", name, "error", ferr)
			}
			c.event(ctx, p.scaleSet, store.LevelError, fmt.Sprintf("starting queued runner %s: %v", name, err))
			continue
		}
		if err := c.store.SetRunnerInstance(ctx, name, uuid); err != nil {
			c.log.Error("recording instance", "runner", name, "error", err)
			continue
		}
		c.log.Info("started queued runner", "runner", name, "scale_set", p.scaleSet)
	}
}

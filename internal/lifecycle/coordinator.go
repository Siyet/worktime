// Package lifecycle coordinates ordinary store use with exclusive maintenance.
package lifecycle

import (
	"context"
	"net/http"
	"sync"
)

// Coordinator is a closeable admission gate. Requests admitted before CloseAdmission
// are allowed to finish; WaitDrained observes when all of them have released.
type Coordinator struct {
	mu       sync.Mutex
	open     bool
	inflight int
	drained  chan struct{}
}

func New() *Coordinator {
	drained := make(chan struct{})
	close(drained)
	return &Coordinator{open: true, drained: drained}
}

func (c *Coordinator) acquire() (func(), bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.open {
		return nil, false
	}
	if c.inflight == 0 {
		c.drained = make(chan struct{})
	}
	c.inflight++
	var once sync.Once
	return func() {
		once.Do(func() {
			c.mu.Lock()
			defer c.mu.Unlock()
			c.inflight--
			if c.inflight == 0 {
				close(c.drained)
			}
		})
	}, true
}

// Lease admits a non-HTTP store user such as a background reconciliation job.
// Callers must invoke the returned release function exactly once. Using the same
// gate for requests and jobs makes WaitDrained a real database-write barrier.
func (c *Coordinator) Lease() (func(), bool) {
	return c.acquire()
}

// Middleware rejects new store-bound traffic while maintenance is active. It must be
// installed before authentication because authentication itself reads and sometimes
// writes the store.
func (c *Coordinator) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		release, ok := c.acquire()
		if !ok {
			w.Header().Set("Retry-After", "5")
			http.Error(w, "maintenance in progress", http.StatusServiceUnavailable)
			return
		}
		defer release()
		next.ServeHTTP(w, r)
	})
}

// CloseAdmission prevents new leases. It is idempotent.
func (c *Coordinator) CloseAdmission() {
	c.mu.Lock()
	c.open = false
	c.mu.Unlock()
}

// OpenAdmission resumes ordinary traffic after a failed or completed maintenance run.
func (c *Coordinator) OpenAdmission() {
	c.mu.Lock()
	c.open = true
	c.mu.Unlock()
}

func (c *Coordinator) WaitDrained(ctx context.Context) error {
	c.mu.Lock()
	drained := c.drained
	c.mu.Unlock()
	select {
	case <-drained:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *Coordinator) Maintenance() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return !c.open
}

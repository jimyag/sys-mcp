// Package registry provides the in-memory agent registry for sysplane-center.
package registry

import (
	"context"
	"sync"
	"time"

	pkgstream "github.com/jimyag/sysplane/internal/pkg/stream"
	"github.com/jimyag/sysplane/internal/sysplane-center/metrics"
)

// Status represents the online/offline state of an agent.
type Status string

const (
	StatusOnline  Status = "online"
	StatusOffline Status = "offline"
)

// AgentRecord holds metadata about a registered agent.
type AgentRecord struct {
	Hostname      string
	IP            string
	OS            string
	AgentVersion  string
	NodeType      string // "agent" or "proxy"
	ProxyPath     []string
	RegisteredAt  time.Time
	LastHeartbeat time.Time
	Status        Status
	RouteStream   pkgstream.TunnelStream // the stream to send tool requests on
}

// Registry is the in-memory store of registered agents.
type Registry struct {
	mu      sync.RWMutex
	records map[string]*AgentRecord // key: hostname
}

// New creates a new Registry.
func New() *Registry {
	return &Registry{records: make(map[string]*AgentRecord)}
}

// Register adds or replaces a record for the given hostname.
func (r *Registry) Register(rec *AgentRecord) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.records[rec.Hostname] = rec
	online := 0
	for _, v := range r.records {
		if v.Status == StatusOnline {
			online++
		}
	}
	metrics.AgentsOnline.Set(float64(online))
}

// Unregister removes the record for hostname.
func (r *Registry) Unregister(hostname string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.records, hostname)
}

// UnregisterByStream removes all records whose RouteStream matches s.
// Returns the hostnames that were removed.
func (r *Registry) UnregisterByStream(s pkgstream.TunnelStream) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var removed []string
	for h, rec := range r.records {
		if rec.RouteStream == s {
			delete(r.records, h)
			removed = append(removed, h)
		}
	}
	return removed
}

// Lookup returns the record for hostname, or nil if not found.
func (r *Registry) Lookup(hostname string) *AgentRecord {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.records[hostname]
}

// All returns a snapshot of all records.
func (r *Registry) All() []*AgentRecord {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*AgentRecord, 0, len(r.records))
	for _, rec := range r.records {
		cp := *rec
		out = append(out, &cp)
	}
	return out
}

// OnlineCount returns the number of online agents.
func (r *Registry) OnlineCount() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	n := 0
	for _, rec := range r.records {
		if rec.Status == StatusOnline {
			n++
		}
	}
	return n
}

// UpdateHeartbeat updates the LastHeartbeat timestamp for hostname.
func (r *Registry) UpdateHeartbeat(hostname string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if rec, ok := r.records[hostname]; ok {
		rec.LastHeartbeat = time.Now()
		rec.Status = StatusOnline
	}
}

// StartOfflineChecker runs a background goroutine that marks agents offline
// if they haven't sent a heartbeat within timeout. It stops when ctx is done.
// onOffline callbacks (if any) are called outside the lock for each hostname
// that transitions from online to offline.
func (r *Registry) StartOfflineChecker(ctx context.Context, timeout time.Duration, onOffline ...func(context.Context, string)) {
	go func() {
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				r.checkOffline(ctx, timeout, onOffline)
			}
		}
	}()
}

func (r *Registry) checkOffline(ctx context.Context, timeout time.Duration, onOffline []func(context.Context, string)) {
	r.mu.Lock()
	var justOffline []string
	now := time.Now()
	for _, rec := range r.records {
		if rec.Status == StatusOnline && now.Sub(rec.LastHeartbeat) > timeout {
			rec.Status = StatusOffline
			justOffline = append(justOffline, rec.Hostname)
		}
	}
	// 统计在线数并更新 gauge
	online := 0
	for _, rec := range r.records {
		if rec.Status == StatusOnline {
			online++
		}
	}
	r.mu.Unlock()
	metrics.AgentsOnline.Set(float64(online))

	// 回调在锁外执行，避免死锁；跳过 nil 函数（调用者误传时不 panic）
	for _, h := range justOffline {
		for _, fn := range onOffline {
			if fn == nil {
				continue
			}
			fn(ctx, h)
		}
	}
}

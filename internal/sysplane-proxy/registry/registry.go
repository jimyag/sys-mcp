// Package registry provides the in-memory downstream agent registry for sysplane-proxy.
package registry

import (
	"context"
	"sync"
	"time"

	pkgstream "github.com/jimyag/sysplane/internal/pkg/stream"
)

// Status represents the online/offline state of an agent.
type Status string

const (
	StatusOnline  Status = "online"
	StatusOffline Status = "offline"
)

// AgentRecord holds metadata about a registered downstream agent or proxy.
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
	RouteStream   pkgstream.TunnelStream
}

// Registry is the in-memory store for downstream agents.
type Registry struct {
	mu      sync.RWMutex
	records map[string]*AgentRecord
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
}

// Unregister removes the record for hostname.
func (r *Registry) Unregister(hostname string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.records, hostname)
}

// UnregisterByStream removes all records whose RouteStream matches s.
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

// UpdateHeartbeat updates the LastHeartbeat timestamp for hostname.
func (r *Registry) UpdateHeartbeat(hostname string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if rec, ok := r.records[hostname]; ok {
		rec.LastHeartbeat = time.Now()
		rec.Status = StatusOnline
	}
}

// StartOfflineChecker marks agents offline if heartbeat is stale.
func (r *Registry) StartOfflineChecker(ctx context.Context, timeout time.Duration) {
	go func() {
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				r.checkOffline(timeout)
			}
		}
	}()
}

func (r *Registry) checkOffline(timeout time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	for _, rec := range r.records {
		if rec.Status == StatusOnline && now.Sub(rec.LastHeartbeat) > timeout {
			rec.Status = StatusOffline
		}
	}
}

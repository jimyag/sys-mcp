package registry_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jimyag/sysplane/api/tunnel"
	pkgstream "github.com/jimyag/sysplane/internal/pkg/stream"
	"github.com/jimyag/sysplane/internal/sysplane-center/registry"
)

// streamStub satisfies pkgstream.TunnelStream for testing.
type streamStub struct{ id string }

func (s *streamStub) Send(*tunnel.TunnelMessage) error     { return nil }
func (s *streamStub) Recv() (*tunnel.TunnelMessage, error) { return nil, context.Canceled }
func (s *streamStub) ID() string                           { return s.id }
func (s *streamStub) RemoteAddr() string                   { return "127.0.0.1:0" }
func (s *streamStub) Context() context.Context             { return context.Background() }

var _ pkgstream.TunnelStream = (*streamStub)(nil)

func makeRecord(hostname string, stream pkgstream.TunnelStream) *registry.AgentRecord {
	return &registry.AgentRecord{
		Hostname:      hostname,
		Status:        registry.StatusOnline,
		LastHeartbeat: time.Now(),
		RouteStream:   stream,
	}
}

func TestRegistry_RegisterAndLookup(t *testing.T) {
	r := registry.New()
	r.Register(makeRecord("web-01", &streamStub{id: "s1"}))

	rec := r.Lookup("web-01")
	if rec == nil {
		t.Fatal("expected record for web-01")
	}
	if rec.Hostname != "web-01" {
		t.Fatalf("wrong hostname: %s", rec.Hostname)
	}
}

func TestRegistry_UnregisterByStream(t *testing.T) {
	r := registry.New()
	s1 := &streamStub{id: "s1"}
	s2 := &streamStub{id: "s2"}
	r.Register(makeRecord("web-01", s1))
	r.Register(makeRecord("web-02", s1))
	r.Register(makeRecord("db-01", s2))

	removed := r.UnregisterByStream(s1)
	if len(removed) != 2 {
		t.Fatalf("expected 2 removed, got %d", len(removed))
	}
	if r.Lookup("web-01") != nil {
		t.Fatal("expected web-01 to be removed")
	}
	if r.Lookup("db-01") == nil {
		t.Fatal("db-01 should still exist")
	}
}

func TestRegistry_UpdateHeartbeat(t *testing.T) {
	r := registry.New()
	rec := makeRecord("web-01", &streamStub{id: "s1"})
	rec.Status = registry.StatusOffline
	r.Register(rec)

	r.UpdateHeartbeat("web-01")
	updated := r.Lookup("web-01")
	if updated.Status != registry.StatusOnline {
		t.Fatal("expected status online after heartbeat update")
	}
}

func TestRegistry_ConcurrentAccess(t *testing.T) {
	r := registry.New()
	done := make(chan struct{}, 10)
	for i := 0; i < 10; i++ {
		go func(n int) {
			s := &streamStub{id: fmt.Sprintf("s%d", n)}
			hostname := fmt.Sprintf("host-%d", n)
			r.Register(makeRecord(hostname, s))
			r.UpdateHeartbeat(hostname)
			r.Lookup(hostname)
			r.All()
			done <- struct{}{}
		}(i)
	}
	for i := 0; i < 10; i++ {
		<-done
	}
}

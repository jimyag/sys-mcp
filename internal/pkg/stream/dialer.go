// Package stream provides a reconnecting gRPC stream dialer with heartbeat support.
package stream

import (
	"context"
	"fmt"
	"math/rand"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/jimyag/sys-mcp/api/tunnel"
)

var globalCounter atomic.Uint64

// NewRequestID generates a unique request ID with the given prefix.
func NewRequestID(prefix string) string {
	return fmt.Sprintf("%s-%d-%d", prefix, time.Now().UnixNano(), globalCounter.Add(1))
}

// DialerConfig holds the configuration for the Dialer.
type DialerConfig struct {
	// Endpoint is the gRPC server address (host:port).
	Endpoint string
	// TLSCredentials, if non-nil, enables TLS. If nil, insecure transport is used.
	TLSCredentials credentials.TransportCredentials
	// RegisterMsg is the first message sent after connecting (must be a REGISTER_REQ).
	RegisterMsg *tunnel.TunnelMessage
	// HeartbeatInterval is how often to send heartbeats. Default: 30s.
	HeartbeatInterval time.Duration
	// ReconnectMaxDelay is the max backoff wait. Default: 5s.
	ReconnectMaxDelay time.Duration
	// OnMessage is called for every incoming message (except REGISTER_ACK and HEARTBEAT_ACK).
	OnMessage func(msg *tunnel.TunnelMessage)
	// OnRegisterAck is called when a REGISTER_ACK is received (success or failure).
	OnRegisterAck func(ack *tunnel.RegisterAck)
}

// Dialer manages a single bidirectional stream with automatic reconnect.
type Dialer struct {
	cfg    DialerConfig
	mu     sync.Mutex
	stream tunnel.TunnelService_ConnectClient
}

// NewDialer creates a Dialer with the given config.
func NewDialer(cfg DialerConfig) *Dialer {
	if cfg.HeartbeatInterval <= 0 {
		cfg.HeartbeatInterval = 30 * time.Second
	}
	if cfg.ReconnectMaxDelay <= 0 {
		cfg.ReconnectMaxDelay = 5 * time.Second
	}
	return &Dialer{cfg: cfg}
}

// Run dials, registers, and reads the stream until ctx is cancelled.
// On disconnection, it reconnects with exponential backoff.
func (d *Dialer) Run(ctx context.Context) error {
	delay := 200 * time.Millisecond
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := d.runOnce(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			// exponential backoff with jitter
			jitter := time.Duration(rand.Int63n(int64(200 * time.Millisecond)))
			wait := delay + jitter
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(wait):
			}
			delay = min(delay*2, d.cfg.ReconnectMaxDelay)
		}
	}
}

// Send sends a message on the current stream. Returns an error if no stream is active.
func (d *Dialer) Send(msg *tunnel.TunnelMessage) error {
	d.mu.Lock()
	s := d.stream
	d.mu.Unlock()
	if s == nil {
		return fmt.Errorf("stream: no active connection")
	}
	return s.Send(msg)
}

func (d *Dialer) runOnce(ctx context.Context) error {
	var creds credentials.TransportCredentials
	if d.cfg.TLSCredentials != nil {
		creds = d.cfg.TLSCredentials
	} else {
		creds = insecure.NewCredentials()
	}

	conn, err := grpc.NewClient(d.cfg.Endpoint, grpc.WithTransportCredentials(creds))
	if err != nil {
		return fmt.Errorf("stream: dial %s: %w", d.cfg.Endpoint, err)
	}
	defer conn.Close()

	client := tunnel.NewTunnelServiceClient(conn)
	stream, err := client.Connect(ctx)
	if err != nil {
		return fmt.Errorf("stream: open stream: %w", err)
	}

	// Send register message.
	if err := stream.Send(d.cfg.RegisterMsg); err != nil {
		return fmt.Errorf("stream: send register: %w", err)
	}

	// Wait for REGISTER_ACK.
	ackMsg, err := stream.Recv()
	if err != nil {
		return fmt.Errorf("stream: recv ack: %w", err)
	}
	ack := ackMsg.GetRegisterAck()
	if ack == nil {
		return fmt.Errorf("stream: expected RegisterAck, got %T", ackMsg.Payload)
	}
	if d.cfg.OnRegisterAck != nil {
		d.cfg.OnRegisterAck(ack)
	}
	if !ack.Success {
		// Don't reconnect on explicit rejection (bad token etc).
		return nil
	}

	d.mu.Lock()
	d.stream = stream
	d.mu.Unlock()

	defer func() {
		d.mu.Lock()
		d.stream = nil
		d.mu.Unlock()
	}()

	// Start heartbeat goroutine.
	hbCtx, hbCancel := context.WithCancel(ctx)
	defer hbCancel()
	go d.heartbeat(hbCtx, stream)

	// Read loop.
	for {
		msg, err := stream.Recv()
		if err != nil {
			return fmt.Errorf("stream: recv: %w", err)
		}
		switch msg.Payload.(type) {
		case *tunnel.TunnelMessage_HeartbeatAck:
			// ignore
		default:
			if d.cfg.OnMessage != nil {
				d.cfg.OnMessage(msg)
			}
		}
	}
}

func (d *Dialer) heartbeat(ctx context.Context, stream tunnel.TunnelService_ConnectClient) {
	ticker := time.NewTicker(d.cfg.HeartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = stream.Send(&tunnel.TunnelMessage{
				Payload: &tunnel.TunnelMessage_Heartbeat{
					Heartbeat: &tunnel.Heartbeat{
						TimestampMs: time.Now().UnixMilli(),
					},
				},
			})
		}
	}
}


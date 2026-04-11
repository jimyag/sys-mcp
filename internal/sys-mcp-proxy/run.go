package proxy

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net"
	"os"
	"time"

	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"

	apitunnel "github.com/jimyag/sys-mcp/api/tunnel"
	pkgstream "github.com/jimyag/sys-mcp/internal/pkg/stream"
	"github.com/jimyag/sys-mcp/internal/pkg/tlsconf"
	"github.com/jimyag/sys-mcp/internal/pkg/logutil"
	proxycfg "github.com/jimyag/sys-mcp/internal/sys-mcp-proxy/config"
	proxyreg "github.com/jimyag/sys-mcp/internal/sys-mcp-proxy/registry"
	proxytunnel "github.com/jimyag/sys-mcp/internal/sys-mcp-proxy/tunnel"
)

func Run(ctx context.Context, configPath string) error {
	cfg, err := proxycfg.Load(configPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: logutil.ParseLevel(cfg.Logging.Level),
	}))

	proxyHostname := cfg.Hostname
	if proxyHostname == "" {
		proxyHostname, _ = os.Hostname()
	}
	if proxyHostname == "" {
		proxyHostname = "proxy"
	}

	reg := proxyreg.New()
	reg.StartOfflineChecker(ctx, 90*time.Second)

	dialerHolder := &dialerAdapter{}
	downstreamSvc := proxytunnel.NewDownstreamService(
		reg,
		cfg.Auth.AgentTokens,
		dialerHolder,
		proxyHostname,
		logger,
	)

	upstreamCreds, err := buildUpstreamCreds(cfg)
	if err != nil {
		return fmt.Errorf("upstream TLS config: %w", err)
	}

	dialerCfg := pkgstream.DialerConfig{
		Endpoint:       cfg.Upstream.Address,
		TLSCredentials: upstreamCreds,
		RegisterMsg: &apitunnel.TunnelMessage{
			Payload: &apitunnel.TunnelMessage_RegisterRequest{
				RegisterRequest: &apitunnel.RegisterRequest{
					Hostname:     proxyHostname,
					Os:           "proxy",
					AgentVersion: "1.0.0",
					NodeType:     apitunnel.NodeType_NODE_TYPE_PROXY,
					Token:        cfg.Upstream.Token,
				},
			},
		},
		HeartbeatInterval: 30 * time.Second,
		ReconnectMaxDelay: 30 * time.Second,
		OnMessage: func(msg *apitunnel.TunnelMessage) {
			switch msg.Payload.(type) {
			case *apitunnel.TunnelMessage_ToolRequest:
				downstreamSvc.DeliverToolRequest(msg)
			case *apitunnel.TunnelMessage_CancelRequest:
				downstreamSvc.DeliverCancelRequest(msg)
			default:
				logger.Warn("proxy received unexpected upstream message", "type", fmt.Sprintf("%T", msg.Payload))
			}
		},
		OnRegisterAck: func(ack *apitunnel.RegisterAck) {
			if !ack.Success {
				logger.Error("upstream rejected registration", "message", ack.Message)
				return
			}
			logger.Info("registered with upstream", "address", cfg.Upstream.Address)
			downstreamSvc.ReregisterAll(ctx)
		},
	}

	dialer := pkgstream.NewDialer(dialerCfg)
	dialerHolder.d = dialer

	g, gCtx := errgroup.WithContext(ctx)

	g.Go(func() error {
		return dialer.Run(gCtx)
	})

	g.Go(func() error {
		serverCreds, err := buildDownstreamCreds(cfg)
		if err != nil {
			return fmt.Errorf("downstream TLS config: %w", err)
		}
		grpcServer := grpc.NewServer(grpc.Creds(serverCreds))
		apitunnel.RegisterTunnelServiceServer(grpcServer, downstreamSvc)

		lis, err := net.Listen("tcp", cfg.Listen.GRPCAddress)
		if err != nil {
			return fmt.Errorf("downstream listen %s: %w", cfg.Listen.GRPCAddress, err)
		}
		logger.Info("downstream gRPC listening", "address", cfg.Listen.GRPCAddress)
		go func() {
			<-gCtx.Done()
			grpcServer.GracefulStop()
		}()
		return grpcServer.Serve(lis)
	})

	if err := g.Wait(); err != nil && err != context.Canceled {
		logger.Error("proxy exited with error", "error", err)
		return err
	}
	return nil
}

type dialerAdapter struct {
	d *pkgstream.Dialer
}

func (a *dialerAdapter) Send(msg *apitunnel.TunnelMessage) error {
	if a.d == nil {
		return fmt.Errorf("proxy: upstream dialer not ready")
	}
	return a.d.Send(msg)
}

func buildUpstreamCreds(cfg *proxycfg.ProxyConfig) (credentials.TransportCredentials, error) {
	t := cfg.Upstream.TLS
	if cfg.Upstream.InsecureTLS {
		return credentials.NewTLS(&tls.Config{InsecureSkipVerify: true}), nil //nolint:gosec
	}
	if t.CertFile != "" || t.CAFile != "" {
		tlsCfg, err := tlsconf.LoadClientTLS(t.CertFile, t.KeyFile, t.CAFile)
		if err != nil {
			return nil, err
		}
		return credentials.NewTLS(tlsCfg), nil
	}
	return nil, nil
}

func buildDownstreamCreds(cfg *proxycfg.ProxyConfig) (credentials.TransportCredentials, error) {
	t := cfg.Listen.TLS
	if t.CertFile != "" {
		tlsCfg, err := tlsconf.LoadServerTLS(t.CertFile, t.KeyFile, t.CAFile)
		if err != nil {
			return nil, err
		}
		return credentials.NewTLS(tlsCfg), nil
	}
	return insecure.NewCredentials(), nil
}


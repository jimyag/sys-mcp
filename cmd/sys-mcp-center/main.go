package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/jimyag/sys-mcp/api/tunnel"
	"github.com/jimyag/sys-mcp/internal/pkg/tlsconf"
	center "github.com/jimyag/sys-mcp/internal/sys-mcp-center"
	centercfg "github.com/jimyag/sys-mcp/internal/sys-mcp-center/config"
	"github.com/jimyag/sys-mcp/internal/sys-mcp-center/ha"
	centermcp "github.com/jimyag/sys-mcp/internal/sys-mcp-center/mcp"
	"github.com/jimyag/sys-mcp/internal/sys-mcp-center/registry"
	"github.com/jimyag/sys-mcp/internal/sys-mcp-center/router"
	"github.com/jimyag/sys-mcp/internal/sys-mcp-center/store"
)

var defaultConfigPaths = []string{
	"./sys-mcp-center.yaml",
	"/etc/sys-mcp/center.yaml",
}

func main() {
	var configPath string
	flag.StringVar(&configPath, "config", "", "path to center config file")
	flag.Parse()

	if configPath == "" {
		for _, p := range defaultConfigPaths {
			if _, err := os.Stat(p); err == nil {
				configPath = p
				break
			}
		}
	}
	if configPath == "" {
		fmt.Fprintln(os.Stderr, "error: no config file found; use --config")
		os.Exit(1)
	}

	cfg, err := centercfg.Load(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: load config: %v\n", err)
		os.Exit(1)
	}

	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: parseLogLevel(cfg.Logging.Level),
	}))

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	reg := registry.New()

	// 若数据库启用，offline checker 写回 PG
	var pgOfflineCallback func(context.Context, string)

	rtr := router.New(cfg.Router.RequestTimeoutSec)

	// 计算 center 实例 ID（hostname + grpc port）
	hostname, _ := os.Hostname()
	instanceID := hostname + cfg.Listen.GRPCAddress

	// PostgreSQL 存储层（可选）
	var st *store.Store
	var routerBridge *ha.RouterBridge
	if cfg.Database.Enable {
		var stErr error
		st, stErr = store.New(ctx, cfg.Database.DSN, int32(cfg.Database.MaxConns))
		if stErr != nil {
			fmt.Fprintf(os.Stderr, "error: connect to PostgreSQL: %v\n", stErr)
			os.Exit(1)
		}
		defer st.Close()
		if migrateErr := st.Migrate(ctx); migrateErr != nil {
			fmt.Fprintf(os.Stderr, "error: database migration: %v\n", migrateErr)
			os.Exit(1)
		}
		logger.Info("PostgreSQL 存储层已启用")

		pgOfflineCallback = func(cbCtx context.Context, hostname string) {
			offCtx, cancel := context.WithTimeout(cbCtx, 5*time.Second)
			defer cancel()
			if err := st.SetAgentOffline(offCtx, hostname); err != nil {
				logger.Warn("SetAgentOffline (offline checker) failed", "hostname", hostname, "error", err)
			}
		}

		internalAddr := cfg.Listen.HTTPAddress // 内部转发使用同一 HTTP 端口
		registrar := ha.NewCenterRegistrar(st, instanceID, internalAddr, logger)
		if regErr := registrar.Start(ctx); regErr != nil {
			fmt.Fprintf(os.Stderr, "error: register center instance: %v\n", regErr)
			os.Exit(1)
		}
		routerBridge = ha.NewRouterBridge(st, instanceID, cfg.HA.InternalSecret, cfg.HA.InternalUseTLS, cfg.HA.InternalSkipVerify)
	}

	if pgOfflineCallback != nil {
		reg.StartOfflineChecker(ctx, 90*time.Second, pgOfflineCallback)
	} else {
		reg.StartOfflineChecker(ctx, 90*time.Second)
	}

	// 可选 AgentPersister（store.Store 实现了该接口）
	var persister center.AgentPersister
	if st != nil {
		persister = st
	}

	tunnelSvc := center.NewTunnelServiceServer(reg, rtr, cfg.Auth.AgentTokens, logger, persister, instanceID)

	// gRPC server — TLS if cert/key configured, else insecure.
	var grpcCreds grpc.ServerOption
	tlsCfg := cfg.Listen.TLS
	if tlsCfg.CertFile != "" && tlsCfg.KeyFile != "" {
		serverTLS, err := tlsconf.LoadServerTLS(tlsCfg.CertFile, tlsCfg.KeyFile, tlsCfg.CAFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: load TLS: %v\n", err)
			os.Exit(1)
		}
		grpcCreds = grpc.Creds(credentials.NewTLS(serverTLS))
		logger.Info("TLS enabled", "cert", tlsCfg.CertFile)
	} else {
		grpcCreds = grpc.Creds(insecure.NewCredentials())
		logger.Warn("TLS disabled — running in insecure mode")
	}
	grpcServer := grpc.NewServer(grpcCreds)
	tunnel.RegisterTunnelServiceServer(grpcServer, tunnelSvc)

	var callLogger centermcp.CallLogger
	if st != nil {
		callLogger = st
	}

	mcpHandler := centermcp.NewMCPHandler(reg, rtr, cfg.Auth.ClientTokens, routerBridge, callLogger, instanceID)

	// HTTP 路由：/internal/forward 用于跨 center 实例转发，其余走 MCP handler
	httpMux := http.NewServeMux()
	httpMux.HandleFunc("/internal/forward", makeInternalForwardHandler(reg, rtr, cfg.HA.InternalSecret, logger))
	httpMux.Handle("/", mcpHandler)

	g, gCtx := errgroup.WithContext(ctx)

	g.Go(func() error {
		lis, err := net.Listen("tcp", cfg.Listen.GRPCAddress)
		if err != nil {
			return fmt.Errorf("gRPC listen %s: %w", cfg.Listen.GRPCAddress, err)
		}
		logger.Info("gRPC server listening", "address", cfg.Listen.GRPCAddress)
		go func() {
			<-gCtx.Done()
			grpcServer.GracefulStop()
		}()
		return grpcServer.Serve(lis)
	})

	g.Go(func() error {
		httpSrv := &http.Server{
			Addr:    cfg.Listen.HTTPAddress,
			Handler: httpMux,
		}
		logger.Info("HTTP/MCP server listening", "address", cfg.Listen.HTTPAddress)
		go func() {
			<-gCtx.Done()
			shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = httpSrv.Shutdown(shutCtx)
		}()
		if tlsCfg.CertFile != "" && tlsCfg.KeyFile != "" {
			if err := httpSrv.ListenAndServeTLS(tlsCfg.CertFile, tlsCfg.KeyFile); err != nil && err != http.ErrServerClosed {
				return fmt.Errorf("HTTPS server: %w", err)
			}
		} else {
			if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				return fmt.Errorf("HTTP server: %w", err)
			}
		}
		return nil
	})

	// Prometheus metrics 端点（可选，独立端口）
	if cfg.Metrics.Enable {
		g.Go(func() error {
			metricsMux := http.NewServeMux()
			metricsMux.Handle("/metrics", promhttp.Handler())
			metricsSrv := &http.Server{
				Addr:    cfg.Metrics.Address,
				Handler: metricsMux,
			}
			logger.Info("Prometheus metrics server listening", "address", cfg.Metrics.Address)
			go func() {
				<-gCtx.Done()
				shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_ = metricsSrv.Shutdown(shutCtx)
			}()
			if err := metricsSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				return fmt.Errorf("metrics server: %w", err)
			}
			return nil
		})
	}

	if err := g.Wait(); err != nil && err != context.Canceled {
		logger.Error("center exited with error", "error", err)
		os.Exit(1)
	}
}

func parseLogLevel(level string) slog.Level {
	switch level {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// makeInternalForwardHandler 创建内部工具转发 HTTP 处理器。
// 其他 center 实例通过 POST /internal/forward 将工具请求转发到本实例。
// secret 若非空，则要求请求携带 X-Internal-Auth: <secret> 头。
func makeInternalForwardHandler(reg *registry.Registry, rtr *router.Router, secret string, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		// 鉴权：非空 secret 要求 header 匹配
		if secret != "" {
			if r.Header.Get("X-Internal-Auth") != secret {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}
		var req ha.ForwardRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}

		rec := reg.Lookup(req.TargetHost)
		if rec == nil {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(ha.ForwardResponse{Error: fmt.Sprintf("agent %q not found", req.TargetHost)})
			return
		}

		result, err := rtr.Send(r.Context(), rec, req.RequestID, req.ToolName, req.ArgsJSON)
		w.Header().Set("Content-Type", "application/json")
		if err != nil {
			json.NewEncoder(w).Encode(ha.ForwardResponse{Error: err.Error()})
			return
		}
		json.NewEncoder(w).Encode(ha.ForwardResponse{ResultJSON: result})
		logger.Debug("内部转发完成", "target", req.TargetHost, "tool", req.ToolName)
	}
}

package center

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/jimyag/sysplane/api/tunnel"
	"github.com/jimyag/sysplane/internal/pkg/logutil"
	"github.com/jimyag/sysplane/internal/pkg/tlsconf"
	"github.com/jimyag/sysplane/internal/pkg/tokenauth"
	"github.com/jimyag/sysplane/internal/sysplane-center/admin"
	centercfg "github.com/jimyag/sysplane/internal/sysplane-center/config"
	"github.com/jimyag/sysplane/internal/sysplane-center/ha"
	"github.com/jimyag/sysplane/internal/sysplane-center/httpapi"
	"github.com/jimyag/sysplane/internal/sysplane-center/registry"
	"github.com/jimyag/sysplane/internal/sysplane-center/router"
	"github.com/jimyag/sysplane/internal/sysplane-center/store"
	"github.com/jimyag/sysplane/internal/sysplane-center/webui"
)

func Run(ctx context.Context, configPath string) error {
	cfg, err := centercfg.Load(configPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: logutil.ParseLevel(cfg.Logging.Level),
	}))

	reg := registry.New()
	rtr := router.New(cfg.Router.RequestTimeoutSec)
	tokenCatalog, err := tokenauth.NewCatalog(
		cfg.Auth.ClientTokens,
		cfg.Auth.AdminTokens,
		cfg.Auth.AgentTokens,
		cfg.Auth.ProxyTokens,
	)
	if err != nil {
		return fmt.Errorf("build token catalog: %w", err)
	}

	var pgOfflineCallback func(context.Context, string)

	hostname, err := os.Hostname()
	if err != nil {
		logger.Warn("os.Hostname() failed, instanceID will use empty hostname", "error", err)
	}
	instanceID := hostname + cfg.Listen.GRPCAddress

	var st *store.Store
	var routerBridge *ha.RouterBridge
	if cfg.Database.Enable {
		st, routerBridge, pgOfflineCallback, err = setupStore(ctx, cfg, instanceID, logger)
		if err != nil {
			return err
		}
		defer st.Close()
	}

	if pgOfflineCallback != nil {
		reg.StartOfflineChecker(ctx, 90*time.Second, pgOfflineCallback)
	} else {
		reg.StartOfflineChecker(ctx, 90*time.Second)
	}

	var persister AgentPersister
	if st != nil {
		persister = st
	}

	tunnelSvc := NewTunnelServiceServer(reg, rtr, tokenCatalog, logger, persister, instanceID)

	grpcCreds, err := buildGRPCServerOption(cfg, logger)
	if err != nil {
		return err
	}
	grpcServer := grpc.NewServer(grpcCreds)
	tunnel.RegisterTunnelServiceServer(grpcServer, tunnelSvc)

	var callLogger admin.CallLogger
	if st != nil {
		callLogger = st
	}

	// Wrap routerBridge in a non-nil interface only when HA is actually configured.
	// Assigning a nil *ha.RouterBridge directly to RemoteForwarder would produce a
	// non-nil interface value (type != nil, pointer == nil), causing a nil-pointer
	// panic the first time ForwardIfNeeded is called.
	var fwd admin.RemoteForwarder
	if routerBridge != nil {
		fwd = routerBridge
	}

	adminSvc := admin.NewService(reg, rtr, fwd, callLogger, instanceID)
	apiHandler := httpapi.NewHandler(reg, tokenCatalog, adminSvc)
	webUIHandler := webui.NewHandler()
	httpMux := http.NewServeMux()
	httpMux.HandleFunc("/internal/forward", makeInternalForwardHandler(reg, rtr, cfg.HA.InternalSecret, logger, callLogger, instanceID))
	httpMux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/web/", http.StatusTemporaryRedirect)
	})
	httpMux.HandleFunc("/web", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/web/", http.StatusTemporaryRedirect)
	})
	httpMux.Handle("/web/", http.StripPrefix("/web/", webUIHandler))
	httpMux.Handle("/v1/", apiHandler)

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
		logger.Info("HTTP server listening", "address", cfg.Listen.HTTPAddress)
		go func() {
			<-gCtx.Done()
			shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = httpSrv.Shutdown(shutCtx)
		}()
		if cfg.Listen.TLS.CertFile != "" && cfg.Listen.TLS.KeyFile != "" {
			if err := httpSrv.ListenAndServeTLS(cfg.Listen.TLS.CertFile, cfg.Listen.TLS.KeyFile); err != nil && err != http.ErrServerClosed {
				return fmt.Errorf("HTTPS server: %w", err)
			}
			return nil
		}
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			return fmt.Errorf("HTTP server: %w", err)
		}
		return nil
	})

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
		return err
	}
	return nil
}

func setupStore(ctx context.Context, cfg *centercfg.CenterConfig, instanceID string, logger *slog.Logger) (*store.Store, *ha.RouterBridge, func(context.Context, string), error) {
	st, err := store.New(ctx, cfg.Database.DSN, int32(cfg.Database.MaxConns))
	if err != nil {
		return nil, nil, nil, fmt.Errorf("connect to PostgreSQL: %w", err)
	}
	if err := st.Migrate(ctx); err != nil {
		st.Close()
		return nil, nil, nil, fmt.Errorf("database migration: %w", err)
	}
	logger.Info("PostgreSQL 存储层已启用")

	pgOfflineCallback := func(cbCtx context.Context, hostname string) {
		offCtx, cancel := context.WithTimeout(cbCtx, 5*time.Second)
		defer cancel()
		if err := st.SetAgentOffline(offCtx, hostname); err != nil {
			logger.Warn("SetAgentOffline (offline checker) failed", "hostname", hostname, "error", err)
		}
	}

	registrar := ha.NewCenterRegistrar(st, instanceID, cfg.HA.InternalAddress, logger)
	if err := registrar.Start(ctx); err != nil {
		st.Close()
		return nil, nil, nil, fmt.Errorf("register center instance: %w", err)
	}

	routerBridge := ha.NewRouterBridge(st, instanceID, cfg.HA.InternalSecret, cfg.HA.InternalUseTLS, cfg.HA.InternalSkipVerify)
	return st, routerBridge, pgOfflineCallback, nil
}

func buildGRPCServerOption(cfg *centercfg.CenterConfig, logger *slog.Logger) (grpc.ServerOption, error) {
	tlsCfg := cfg.Listen.TLS
	if tlsCfg.CertFile != "" && tlsCfg.KeyFile != "" {
		serverTLS, err := tlsconf.LoadServerTLS(tlsCfg.CertFile, tlsCfg.KeyFile, tlsCfg.CAFile)
		if err != nil {
			return nil, fmt.Errorf("load TLS: %w", err)
		}
		logger.Info("TLS enabled", "cert", tlsCfg.CertFile)
		return grpc.Creds(credentials.NewTLS(serverTLS)), nil
	}
	logger.Warn("TLS disabled — running in insecure mode")
	return grpc.Creds(insecure.NewCredentials()), nil
}

// makeInternalForwardHandler 创建内部工具转发 HTTP 处理器。
// 其他 center 实例通过 POST /internal/forward 将工具请求转发到本实例。
// secret 若非空，则要求请求携带 X-Internal-Auth: <secret> 头。
func makeInternalForwardHandler(reg *registry.Registry, rtr *router.Router, secret string, logger *slog.Logger, callLogger admin.CallLogger, instanceID string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if secret != "" && r.Header.Get("X-Internal-Auth") != secret {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
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

		if callLogger != nil {
			_ = callLogger.InsertToolCallLog(r.Context(), req.RequestID, instanceID, req.TargetHost, req.ToolName, req.ArgsJSON)
		}
		result, err := rtr.Send(r.Context(), rec, req.RequestID, req.ToolName, req.ArgsJSON)
		w.Header().Set("Content-Type", "application/json")
		if callLogger != nil {
			errMsg := ""
			if err != nil {
				errMsg = err.Error()
			}
			_ = callLogger.CompleteToolCallLog(r.Context(), req.RequestID, result, errMsg)
		}
		if err != nil {
			json.NewEncoder(w).Encode(ha.ForwardResponse{Error: err.Error()})
			return
		}
		json.NewEncoder(w).Encode(ha.ForwardResponse{ResultJSON: result})
		logger.Debug("内部转发完成", "target", req.TargetHost, "tool", req.ToolName)
	}
}

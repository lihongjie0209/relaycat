package relay

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	httppprof "net/http/pprof"
	"time"

	relayv1 "github.com/lihongjie0209/relaycat/gen/relay/v1"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/health"
	healthv1 "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/reflection"
)

type ServerConfig struct {
	Listen          string
	TLSCert         string
	TLSKey          string
	H2C             bool
	AllowPublicH2C  bool
	Token           string
	NoAuth          bool
	Reflection      bool
	MetricsListen   string
	Pprof           bool
	ShutdownTimeout time.Duration
	Service         Config
	Logger          *slog.Logger
}

func RunServer(ctx context.Context, cfg ServerConfig, ready func(net.Addr)) error {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.ShutdownTimeout <= 0 {
		cfg.ShutdownTimeout = 15 * time.Second
	}
	if cfg.H2C && !cfg.AllowPublicH2C && !isLoopbackListen(cfg.Listen) {
		return errors.New("h2c may only bind loopback unless --allow-public-h2c is set")
	}
	if !cfg.H2C && (cfg.TLSCert == "" || cfg.TLSKey == "") {
		return errors.New("--tls-cert and --tls-key are required unless --h2c is set")
	}
	if cfg.Pprof && (cfg.MetricsListen == "" || !isLoopbackListen(cfg.MetricsListen)) {
		return errors.New("pprof requires a loopback --metrics-listen address")
	}
	lis, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return fmt.Errorf("listening: %w", err)
	}
	defer func() { _ = lis.Close() }()
	var opts []grpc.ServerOption
	if !cfg.H2C {
		cert, err := tls.LoadX509KeyPair(cfg.TLSCert, cfg.TLSKey)
		if err != nil {
			return fmt.Errorf("loading TLS certificate: %w", err)
		}
		opts = append(opts, grpc.Creds(credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}})))
	}
	opts = append(opts,
		grpc.StatsHandler(otelgrpc.NewServerHandler()),
		grpc.ChainStreamInterceptor(StreamAuthInterceptor(cfg.Token, cfg.NoAuth)),
		grpc.ChainUnaryInterceptor(UnaryAuthInterceptor(cfg.Token, cfg.NoAuth)),
		grpc.MaxRecvMsgSize(128<<10), grpc.MaxSendMsgSize(128<<10),
		grpc.KeepaliveParams(keepalive.ServerParameters{Time: 30 * time.Second, Timeout: 10 * time.Second}),
	)
	grpcServer := grpc.NewServer(opts...)
	relayv1.RegisterRelayServiceServer(grpcServer, NewService(cfg.Logger, cfg.Service))
	healthServer := health.NewServer()
	healthv1.RegisterHealthServer(grpcServer, healthServer)
	healthServer.SetServingStatus("", healthv1.HealthCheckResponse_SERVING)
	if cfg.Reflection {
		reflection.Register(grpcServer)
	}
	var metrics *http.Server
	if cfg.MetricsListen != "" {
		mux := http.NewServeMux()
		mux.Handle("/metrics", promhttp.Handler())
		mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ok\n"))
		})
		if cfg.Pprof {
			mux.HandleFunc("/debug/pprof/", httppprof.Index)
			mux.HandleFunc("/debug/pprof/cmdline", httppprof.Cmdline)
			mux.HandleFunc("/debug/pprof/profile", httppprof.Profile)
			mux.HandleFunc("/debug/pprof/symbol", httppprof.Symbol)
			mux.HandleFunc("/debug/pprof/trace", httppprof.Trace)
		}
		metrics = &http.Server{Addr: cfg.MetricsListen, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
		go func() {
			if err := metrics.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				cfg.Logger.Error("metrics server stopped", "error", err)
			}
		}()
	}
	errCh := make(chan error, 1)
	go func() { errCh <- grpcServer.Serve(lis) }()
	if ready != nil {
		ready(lis.Addr())
	}
	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		healthServer.SetServingStatus("", healthv1.HealthCheckResponse_NOT_SERVING)
		if metrics != nil {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
			_ = metrics.Shutdown(shutdownCtx)
			cancel()
		}
		done := make(chan struct{})
		go func() { grpcServer.GracefulStop(); close(done) }()
		select {
		case <-done:
		case <-time.After(cfg.ShutdownTimeout):
			grpcServer.Stop()
		}
		return nil
	}
}

func isLoopbackListen(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

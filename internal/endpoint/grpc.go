package endpoint

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"

	relayv1 "github.com/lihongjie0209/relaycat/gen/relay/v1"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"golang.org/x/crypto/x509roots/fallback/bundle"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
)

const (
	initialStreamWindowSize = 1 << 20
	initialConnWindowSize   = 4 << 20
)

var publicRootPool struct {
	once sync.Once
	pool *x509.CertPool
	err  error
}

type DialConfig struct {
	RelayURL      string
	CAFile        string
	Token         string
	AllowInsecure bool
}

func Dial(cfg DialConfig) (*grpc.ClientConn, relayv1.RelayServiceClient, error) {
	u, err := url.Parse(cfg.RelayURL)
	if err != nil || u.Host == "" {
		return nil, nil, errors.New("invalid relay URL")
	}
	var creds credentials.TransportCredentials
	switch u.Scheme {
	case "https":
		pool, err := rootCertPool(cfg.CAFile)
		if err != nil {
			return nil, nil, err
		}
		tlsCfg := &tls.Config{
			MinVersion: tls.VersionTLS12,
			ServerName: u.Hostname(),
			RootCAs:    pool,
		}
		creds = credentials.NewTLS(tlsCfg)
	case "http":
		if !cfg.AllowInsecure {
			return nil, nil, errors.New("insecure relay requires --allow-insecure-relay")
		}
		creds = insecure.NewCredentials()
	default:
		return nil, nil, errors.New("relay URL scheme must be https or http")
	}
	conn, err := grpc.NewClient(u.Host,
		grpc.WithTransportCredentials(creds),
		grpc.WithStatsHandler(otelgrpc.NewClientHandler()),
		grpc.WithInitialWindowSize(initialStreamWindowSize),
		grpc.WithInitialConnWindowSize(initialConnWindowSize),
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(128<<10), grpc.MaxCallSendMsgSize(128<<10)),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("creating gRPC client: %w", err)
	}
	return conn, relayv1.NewRelayServiceClient(conn), nil
}

func rootCertPool(caFile string) (*x509.CertPool, error) {
	publicRootPool.once.Do(loadPublicRootPool)
	if publicRootPool.err != nil {
		return nil, publicRootPool.err
	}
	if caFile == "" {
		return publicRootPool.pool, nil
	}

	pool := publicRootPool.pool.Clone()
	pem, err := os.ReadFile(caFile) // #nosec G304 -- CA path is explicitly selected by the local operator.
	if err != nil {
		return nil, fmt.Errorf("reading CA file: %w", err)
	}
	if !pool.AppendCertsFromPEM(pem) {
		return nil, errors.New("CA file contains no certificates")
	}
	return pool, nil
}

func loadPublicRootPool() {
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if err := addMozillaRoots(pool); err != nil {
		publicRootPool.err = err
		return
	}
	publicRootPool.pool = pool
}

func addMozillaRoots(pool *x509.CertPool) error {
	for root := range bundle.Roots() {
		cert, err := x509.ParseCertificate(root.Certificate)
		if err != nil {
			return fmt.Errorf("parsing embedded Mozilla root: %w", err)
		}
		if root.Constraint == nil {
			pool.AddCert(cert)
		} else {
			pool.AddCertWithConstraint(cert, root.Constraint)
		}
	}
	return nil
}

func AuthContext(ctx context.Context, token string) context.Context {
	if token == "" {
		return ctx
	}
	return metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+token)
}

func ReadToken(path, envName string) (string, error) {
	if path == "" {
		return strings.TrimSpace(os.Getenv(envName)), nil
	}
	b, err := os.ReadFile(path) // #nosec G304 -- token path is explicitly selected by the local operator.
	if err != nil {
		return "", fmt.Errorf("reading token file: %w", err)
	}
	return strings.TrimSpace(string(b)), nil
}

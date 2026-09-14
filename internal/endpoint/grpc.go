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

	relayv1 "github.com/lihongjie0209/relaycat/gen/relay/v1"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
)

const (
	initialStreamWindowSize = 1 << 20
	initialConnWindowSize   = 4 << 20
)

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
		tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: u.Hostname()}
		if cfg.CAFile != "" {
			pem, err := os.ReadFile(cfg.CAFile)
			if err != nil {
				return nil, nil, fmt.Errorf("reading CA file: %w", err)
			}
			pool, err := x509.SystemCertPool()
			if err != nil {
				pool = x509.NewCertPool()
			}
			if !pool.AppendCertsFromPEM(pem) {
				return nil, nil, errors.New("CA file contains no certificates")
			}
			tlsCfg.RootCAs = pool
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

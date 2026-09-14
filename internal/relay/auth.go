package relay

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"strings"

	"github.com/lihongjie0209/relaycat/internal/observability"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func StreamAuthInterceptor(expected string, noAuth bool) grpc.StreamServerInterceptor {
	want := sha256.Sum256([]byte(expected))
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		if noAuth {
			return handler(srv, ss)
		}
		md, ok := metadata.FromIncomingContext(ss.Context())
		if !ok {
			observability.AuthRejects.Inc()
			return status.Error(codes.Unauthenticated, "missing authorization")
		}
		values := md.Get("authorization")
		if len(values) != 1 || !strings.HasPrefix(values[0], "Bearer ") {
			observability.AuthRejects.Inc()
			return status.Error(codes.Unauthenticated, "invalid authorization")
		}
		got := sha256.Sum256([]byte(strings.TrimPrefix(values[0], "Bearer ")))
		if subtle.ConstantTimeCompare(got[:], want[:]) != 1 {
			observability.AuthRejects.Inc()
			return status.Error(codes.Unauthenticated, "invalid authorization")
		}
		return handler(srv, ss)
	}
}

func UnaryAuthInterceptor(expected string, noAuth bool) grpc.UnaryServerInterceptor {
	want := sha256.Sum256([]byte(expected))
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if noAuth {
			return handler(ctx, req)
		}
		md, ok := metadata.FromIncomingContext(ctx)
		if !ok {
			observability.AuthRejects.Inc()
			return nil, status.Error(codes.Unauthenticated, "missing authorization")
		}
		values := md.Get("authorization")
		if len(values) != 1 || !strings.HasPrefix(values[0], "Bearer ") {
			observability.AuthRejects.Inc()
			return nil, status.Error(codes.Unauthenticated, "invalid authorization")
		}
		got := sha256.Sum256([]byte(strings.TrimPrefix(values[0], "Bearer ")))
		if subtle.ConstantTimeCompare(got[:], want[:]) != 1 {
			observability.AuthRejects.Inc()
			return nil, status.Error(codes.Unauthenticated, "invalid authorization")
		}
		return handler(ctx, req)
	}
}

package relay

import (
	"context"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func TestUnaryAuthInterceptor(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		authorization string
		want          codes.Code
	}{
		{name: "missing", want: codes.Unauthenticated},
		{name: "wrong", authorization: "Bearer wrong", want: codes.Unauthenticated},
		{name: "valid", authorization: "Bearer secret", want: codes.OK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			if tt.authorization != "" {
				ctx = metadata.NewIncomingContext(ctx, metadata.Pairs("authorization", tt.authorization))
			}
			_, err := UnaryAuthInterceptor("secret", false)(ctx, nil, &grpc.UnaryServerInfo{}, func(context.Context, any) (any, error) { return struct{}{}, nil })
			if got := status.Code(err); got != tt.want {
				t.Fatalf("code = %s, want %s", got, tt.want)
			}
		})
	}
}

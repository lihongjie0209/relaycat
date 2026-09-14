package endpoint

import (
	"context"
	"io"
	"net"
	"testing"
	"time"
)

func TestRunAgentRequiresExactlyOneTarget(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		cfg  AgentConfig
	}{
		{name: "neither", cfg: AgentConfig{}},
		{name: "both", cfg: AgentConfig{Target: "127.0.0.1:22", Handler: func(context.Context, net.Conn) error { return nil }}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if err := RunAgent(context.Background(), test.cfg); err == nil {
				t.Fatal("RunAgent accepted an invalid target configuration")
			}
		})
	}
}

func TestOpenTargetUsesConnectionHandler(t *testing.T) {
	t.Parallel()
	const payload = "built-in-service"
	cfg := AgentConfig{Handler: func(_ context.Context, conn net.Conn) error {
		defer func() { _ = conn.Close() }()
		_, err := io.WriteString(conn, payload)
		return err
	}}
	conn, done, err := openTarget(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if done == nil {
		t.Fatal("handler completion channel is nil")
	}
	if err := conn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(conn)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != payload {
		t.Fatalf("payload = %q, want %q", got, payload)
	}
	if err := <-done; err != nil {
		t.Fatalf("handler returned %v", err)
	}
}

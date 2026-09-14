//go:build integration

package endpoint

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/lihongjie0209/relaycat/internal/accesscode"
	"github.com/lihongjie0209/relaycat/internal/relay"
)

func TestConcurrentEndToEndTCP(t *testing.T) {
	local := startStabilityStack(t)
	const (
		connections = 32
		rounds      = 8
	)

	start := make(chan struct{})
	errCh := make(chan error, connections)
	var wg sync.WaitGroup
	for id := range connections {
		wg.Go(func() {
			<-start
			conn, err := net.DialTimeout("tcp", local, 3*time.Second)
			if err != nil {
				errCh <- fmt.Errorf("connection %d: %w", id, err)
				return
			}
			defer conn.Close()
			if err := conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
				errCh <- fmt.Errorf("connection %d deadline: %w", id, err)
				return
			}
			payload := bytes.Repeat([]byte(fmt.Sprintf("conn-%02d-", id)), 512)
			got := make([]byte, len(payload))
			for round := range rounds {
				if _, err := conn.Write(payload); err != nil {
					errCh <- fmt.Errorf("connection %d round %d write: %w", id, round, err)
					return
				}
				if _, err := io.ReadFull(conn, got); err != nil {
					errCh <- fmt.Errorf("connection %d round %d read: %w", id, round, err)
					return
				}
				if !bytes.Equal(got, payload) {
					errCh <- fmt.Errorf("connection %d round %d: payload mismatch", id, round)
					return
				}
			}
		})
	}
	close(start)
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Error(err)
	}
}

func TestRepeatedConnectDisconnect(t *testing.T) {
	local := startStabilityStack(t)
	const connections = 100
	for id := range connections {
		payload := []byte(fmt.Sprintf("short-lived-connection-%d", id))
		if err := echoRoundTrip(local, payload, 3*time.Second); err != nil {
			t.Fatalf("connection %d: %v", id, err)
		}
	}
}

func TestEndpointsReconnectAfterRelayRestart(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	reservation, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	relayAddress := reservation.Addr().String()
	if err := reservation.Close(); err != nil {
		t.Fatal(err)
	}

	startRelay := func() (context.CancelFunc, <-chan error) {
		relayCtx, stop := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() {
			done <- relay.RunServer(relayCtx, relay.ServerConfig{
				Listen: relayAddress, H2C: true, Logger: log, ShutdownTimeout: 500 * time.Millisecond,
			}, nil)
		}()
		return stop, done
	}
	stopRelay, relayDone := startRelay()

	target, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	go echoServer(target)
	code, err := accesscode.New("http://" + relayAddress)
	if err != nil {
		t.Fatal(err)
	}
	agentDone := make(chan error, 1)
	go func() {
		agentDone <- RunAgent(ctx, AgentConfig{
			Code: code, Target: target.Addr().String(), AllowInsecure: true, Logger: log,
		})
	}()
	localReady := make(chan net.Addr, 1)
	clientDone := make(chan error, 1)
	go func() {
		clientDone <- RunClient(ctx, ClientConfig{
			Code: code, Listen: "127.0.0.1:0", AllowInsecure: true, Logger: log,
		}, func(addr net.Addr) { localReady <- addr })
	}()
	var local net.Addr
	select {
	case local = <-localReady:
	case err := <-clientDone:
		t.Fatalf("client exited during startup: %v", err)
	case <-ctx.Done():
		t.Fatal("client startup timed out")
	}
	if err := waitForEcho(local.String(), 8*time.Second); err != nil {
		t.Fatalf("initial connection: %v", err)
	}

	stopRelay()
	select {
	case err := <-relayDone:
		if err != nil {
			t.Fatalf("stopping relay: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("relay did not stop")
	}

	stopRelay, relayDone = startRelay()
	defer func() {
		stopRelay()
		<-relayDone
	}()
	if err := waitForEcho(local.String(), 12*time.Second); err != nil {
		t.Fatalf("connection after relay restart: %v", err)
	}

	cancel()
	for name, done := range map[string]<-chan error{"agent": agentDone, "client": clientDone} {
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Errorf("%s did not stop after cancellation", name)
		}
	}
}

func BenchmarkEndToEndTCP(b *testing.B) {
	local := startStabilityStack(b)
	payload := bytes.Repeat([]byte("relaycat-benchmark"), 2048)
	b.ReportAllocs()
	b.SetBytes(int64(len(payload)))
	for b.Loop() {
		if err := echoRoundTrip(local, payload, 5*time.Second); err != nil {
			b.Fatal(err)
		}
	}
}

func startStabilityStack(t testing.TB) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	relayReady := make(chan net.Addr, 1)
	relayDone := make(chan error, 1)
	go func() {
		relayDone <- relay.RunServer(ctx, relay.ServerConfig{
			Listen: "127.0.0.1:0", H2C: true, Logger: log,
		}, func(addr net.Addr) { relayReady <- addr })
	}()

	var relayAddr net.Addr
	select {
	case relayAddr = <-relayReady:
	case err := <-relayDone:
		cancel()
		t.Fatalf("relay exited during startup: %v", err)
	case <-ctx.Done():
		cancel()
		t.Fatal("relay startup timed out")
	}

	target, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	go echoServer(target)
	code, err := accesscode.New("http://" + relayAddr.String())
	if err != nil {
		cancel()
		_ = target.Close()
		t.Fatal(err)
	}
	agentDone := make(chan error, 1)
	go func() {
		agentDone <- RunAgent(ctx, AgentConfig{
			Code: code, Target: target.Addr().String(), AllowInsecure: true, Logger: log,
		})
	}()
	localReady := make(chan net.Addr, 1)
	clientDone := make(chan error, 1)
	go func() {
		clientDone <- RunClient(ctx, ClientConfig{
			Code: code, Listen: "127.0.0.1:0", AllowInsecure: true, Logger: log,
		}, func(addr net.Addr) { localReady <- addr })
	}()

	var local net.Addr
	select {
	case local = <-localReady:
	case err := <-clientDone:
		cancel()
		_ = target.Close()
		t.Fatalf("client exited during startup: %v", err)
	case <-ctx.Done():
		cancel()
		_ = target.Close()
		t.Fatal("client startup timed out")
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		if err := echoRoundTrip(local.String(), []byte("ready"), time.Second); err == nil {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			_ = target.Close()
			t.Fatal("agent did not become ready")
		}
		time.Sleep(20 * time.Millisecond)
	}

	t.Cleanup(func() {
		cancel()
		_ = target.Close()
		for name, done := range map[string]<-chan error{
			"relay": relayDone, "agent": agentDone, "client": clientDone,
		} {
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Errorf("%s did not stop after cancellation", name)
			}
		}
	})
	return local.String()
}

func echoRoundTrip(address string, payload []byte, timeout time.Duration) error {
	conn, err := net.DialTimeout("tcp", address, timeout)
	if err != nil {
		return err
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(timeout)); err != nil {
		return err
	}
	if _, err := conn.Write(payload); err != nil {
		return err
	}
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, got); err != nil {
		return err
	}
	if !bytes.Equal(got, payload) {
		return fmt.Errorf("payload mismatch: got %d bytes", len(got))
	}
	return nil
}

func waitForEcho(address string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		lastErr = echoRoundTrip(address, []byte("reconnect-probe"), time.Second)
		if lastErr == nil {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return lastErr
}

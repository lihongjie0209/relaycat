//go:build integration

package endpoint

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lihongjie0209/relaycat/internal/accesscode"
	"github.com/lihongjie0209/relaycat/internal/relay"
	"github.com/lihongjie0209/relaycat/internal/sshserver"
	gossh "golang.org/x/crypto/ssh"
)

func TestBuiltInSSHEndToEnd(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
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
		t.Fatalf("relay exited during startup: %v", err)
	case <-ctx.Done():
		t.Fatal("relay startup timed out")
	}

	clientSigner := newIntegrationSigner(t)
	server, err := sshserver.New(sshserver.Config{
		HostKeyPath:    t.TempDir() + "/host_key",
		AuthorizedKeys: []string{string(gossh.MarshalAuthorizedKey(clientSigner.PublicKey()))},
		Logger:         log,
	})
	if err != nil {
		t.Fatal(err)
	}
	code, err := accesscode.New("http://" + relayAddr.String())
	if err != nil {
		t.Fatal(err)
	}
	agentDone := make(chan error, 1)
	go func() {
		agentDone <- RunAgent(ctx, AgentConfig{
			Code: code, AllowInsecure: true, Logger: log, Handler: server.HandleConn,
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

	sshClient := waitForSSH(t, local.String(), clientSigner, 8*time.Second)
	session, err := sshClient.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	output, err := session.Output("printf relaycat-ssh-e2e")
	if err != nil {
		t.Fatalf("running SSH command: %v", err)
	}
	if string(output) != "relaycat-ssh-e2e" {
		t.Fatalf("unexpected SSH output %q", output)
	}
	_ = sshClient.Close()

	const concurrentConnections = 16
	var wg sync.WaitGroup
	errs := make(chan error, concurrentConnections)
	for id := range concurrentConnections {
		wg.Go(func() {
			want := fmt.Sprintf("session-%d", id)
			got, err := sshCommand(local.String(), clientSigner, "printf "+want)
			if err != nil {
				errs <- fmt.Errorf("session %d: %w", id, err)
				return
			}
			if got != want {
				errs <- fmt.Errorf("session %d output = %q, want %q", id, got, want)
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}

	cancel()
	for name, done := range map[string]<-chan error{
		"relay": relayDone, "agent": agentDone, "client": clientDone,
	} {
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Errorf("%s did not stop after cancellation", name)
		}
	}
}

func TestBuiltInSSHReconnectsAfterRelayRestart(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
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

	signer := newIntegrationSigner(t)
	server, err := sshserver.New(sshserver.Config{
		HostKeyPath:    t.TempDir() + "/host_key",
		AuthorizedKeys: []string{string(gossh.MarshalAuthorizedKey(signer.PublicKey()))},
		Logger:         log,
	})
	if err != nil {
		t.Fatal(err)
	}
	code, err := accesscode.New("http://" + relayAddress)
	if err != nil {
		t.Fatal(err)
	}
	agentDone := make(chan error, 1)
	go func() {
		agentDone <- RunAgent(ctx, AgentConfig{
			Code: code, AllowInsecure: true, Logger: log, Handler: server.HandleConn,
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
	initialClient := waitForSSH(t, local.String(), signer, 8*time.Second)
	_ = initialClient.Close()

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
	reconnectedClient := waitForSSH(t, local.String(), signer, 15*time.Second)
	session, err := reconnectedClient.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	output, err := session.Output("printf reconnected")
	if err != nil {
		t.Fatalf("command after Relay restart: %v", err)
	}
	if string(output) != "reconnected" {
		t.Fatalf("output after Relay restart = %q", output)
	}
	_ = reconnectedClient.Close()

	cancel()
	for name, done := range map[string]<-chan error{"agent": agentDone, "client": clientDone} {
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Errorf("%s did not stop after cancellation", name)
		}
	}
}

func sshCommand(address string, signer gossh.Signer, command string) (string, error) {
	client, err := gossh.Dial("tcp", address, &gossh.ClientConfig{
		User:            "relaycat-test",
		Auth:            []gossh.AuthMethod{gossh.PublicKeys(signer)},
		HostKeyCallback: gossh.InsecureIgnoreHostKey(), //nolint:gosec // Test-only ephemeral host key.
		Timeout:         3 * time.Second,
	})
	if err != nil {
		return "", err
	}
	defer func() { _ = client.Close() }()
	session, err := client.NewSession()
	if err != nil {
		return "", err
	}
	output, err := session.Output(command)
	return string(output), err
}

func newIntegrationSigner(t *testing.T) gossh.Signer {
	t.Helper()
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := gossh.NewSignerFromKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	return signer
}

func waitForSSH(t *testing.T, address string, signer gossh.Signer, timeout time.Duration) *gossh.Client {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		conn, err := gossh.Dial("tcp", address, &gossh.ClientConfig{
			User: "relaycat-test",
			Auth: []gossh.AuthMethod{gossh.PublicKeys(signer)},
			// Test-only ephemeral host key.
			HostKeyCallback: gossh.InsecureIgnoreHostKey(), //nolint:gosec
			Timeout:         time.Second,
		})
		if err == nil {
			return conn
		}
		lastErr = err
		if !strings.Contains(err.Error(), "connection refused") && ctxError(err) {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("SSH server did not become ready: %v", lastErr)
	return nil
}

func ctxError(err error) bool {
	return strings.Contains(err.Error(), "context canceled") || strings.Contains(err.Error(), "deadline exceeded")
}

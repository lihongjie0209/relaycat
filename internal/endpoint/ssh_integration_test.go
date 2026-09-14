//go:build integration

package endpoint

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"io"
	"log/slog"
	"net"
	"strings"
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
